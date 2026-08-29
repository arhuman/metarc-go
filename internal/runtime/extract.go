package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhuman/metarc-go/internal/plan"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"
)

// Extract restores a .marc archive into destDir.
//
// Every blob is integrity-checked before it is written out: the BLAKE3 prefix
// recorded in the catalog must match the stored blob bytes, and the restored
// file size must match the size recorded for the entry. The first mismatch
// aborts the extraction, leaving whatever was already restored in place.
func Extract(ctx context.Context, marcPath, destDir string) error {
	slog.Debug("extracting", "archive", marcPath, "dest", destDir)

	// The traversal guard below compares against an absolute destination: a
	// relative destDir such as the default "." cleans to a prefix ("./") that
	// no cleaned relative path ever carries, which would reject every entry.
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("runtime.Extract: resolve destination %s: %w", destDir, err)
	}

	r, err := store.OpenReader(marcPath)
	if err != nil {
		return fmt.Errorf("runtime.Extract: %w", err)
	}
	defer func() { _ = r.Close() }()

	var count int64
	// Blobs are shared by deduplicated entries; hashing each one once keeps
	// verification cost proportional to unique blobs rather than to entries.
	verified := make(map[int64]struct{})
	err = r.WalkEntries(func(relPath string, e store.EntryRow) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip the root "." entry; destDir is the root.
		if relPath == "." {
			return nil
		}

		fullPath, err := safeJoin(absDest, relPath)
		if err != nil {
			return err
		}

		mode := e.Mode

		if mode&fs.ModeSymlink != 0 {
			// Validate symlink target doesn't escape destDir.
			resolvedTarget := e.LinkTarget
			if !filepath.IsAbs(resolvedTarget) {
				resolvedTarget = filepath.Join(filepath.Dir(fullPath), resolvedTarget)
			}
			if !strings.HasPrefix(filepath.Clean(resolvedTarget), absDest+string(os.PathSeparator)) {
				return fmt.Errorf("extract: symlink target escapes destination: %s -> %s", relPath, e.LinkTarget)
			}

			// Ensure parent exists.
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
				return fmt.Errorf("extract: mkdir for symlink %s: %w", relPath, err)
			}
			// Remove any existing entry at this path.
			_ = os.Remove(fullPath)
			if err := os.Symlink(e.LinkTarget, fullPath); err != nil {
				return fmt.Errorf("extract: symlink %s -> %s: %w", relPath, e.LinkTarget, err)
			}
			count++
			return nil
		}

		if mode.IsDir() {
			if err := os.MkdirAll(fullPath, mode.Perm()); err != nil {
				return fmt.Errorf("extract: mkdir %s: %w", relPath, err)
			}
			restoreMetadata(fullPath, mode, e.MtimeNs, e.UID, e.GID)
			count++
			return nil
		}

		return extractFile(ctx, r, fullPath, relPath, e, &count, verified)
	})

	if err != nil {
		return fmt.Errorf("runtime.Extract: %w", err)
	}

	// Second pass: restore directory mtimes (children may have updated them).
	err = r.WalkEntries(func(relPath string, e store.EntryRow) error {
		if relPath == "." || !e.Mode.IsDir() {
			return nil
		}
		fullPath, err := safeJoin(absDest, relPath)
		if err != nil {
			return nil
		}
		restoreMetadata(fullPath, e.Mode, e.MtimeNs, e.UID, e.GID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("runtime.Extract: restore dir times: %w", err)
	}

	slog.Debug("extract complete", "entries", count)
	return nil
}

// safeJoin resolves relPath under absDest, which must already be absolute and
// clean. It rejects anything that would land outside absDest (Zip Slip),
// including absolute entry names, which filepath.Join would otherwise silently
// reinterpret as relative.
func safeJoin(absDest, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("extract: absolute entry name: %s", relPath)
	}
	fullPath := filepath.Join(absDest, relPath)
	if !strings.HasPrefix(fullPath, absDest+string(os.PathSeparator)) {
		return "", fmt.Errorf("extract: path traversal attempt: %s", relPath)
	}
	return fullPath, nil
}

func extractFile(ctx context.Context, r *store.Reader, fullPath, relPath string, e store.EntryRow, count *int64, verified map[int64]struct{}) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("extract: mkdirall for %s: %w", relPath, err)
	}

	if e.BlobID != 0 {
		if _, done := verified[e.BlobID]; !done {
			if err := r.VerifyBlob(e.BlobID); err != nil {
				return fmt.Errorf("extract: %s: %w", relPath, err)
			}
			verified[e.BlobID] = struct{}{}
		}
	}

	// Drop any existing entry first: without this, a symlink already present
	// at this path (from a prior extraction, a checkout, a shared directory)
	// is followed by O_CREATE and the write lands outside the destination.
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("extract: remove existing %s: %w", relPath, err)
	}

	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, e.Mode.Perm())
	if err != nil {
		return fmt.Errorf("extract: create %s: %w", relPath, err)
	}
	defer func() { _ = f.Close() }()

	if e.BlobID != 0 || e.Transform != "" {
		t, err := resolveTransform(e.Transform)
		if err != nil {
			return fmt.Errorf("extract: %s: %w", relPath, err)
		}
		if t != nil {
			// Use the transform's Reverse to reconstruct the file.
			// BlobIDs may be empty for transforms that embed their reference
			// data in the binary (e.g. license-canonical/v1).
			var blobIDs []marc.BlobID
			if e.BlobID != 0 {
				blobIDs = []marc.BlobID{marc.BlobID(e.BlobID)}
			}
			result := marc.Result{
				BlobIDs: blobIDs,
				Params:  e.Params,
			}
			if err := t.Reverse(ctx, result, r.BlobReaderAdapter(), f); err != nil {
				return fmt.Errorf("extract: reverse %s for %s: %w", e.Transform, relPath, err)
			}
		} else if e.BlobID != 0 {
			// Raw (no transform): copy blob directly.
			blob, err := r.OpenBlob(e.BlobID)
			if err != nil {
				return fmt.Errorf("extract: open blob for %s: %w", relPath, err)
			}
			defer func() { _ = blob.Close() }()

			if _, err := io.Copy(f, blob); err != nil {
				return fmt.Errorf("extract: copy blob for %s: %w", relPath, err)
			}
		}
	}

	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("extract: stat restored %s: %w", relPath, err)
	}
	if st.Size() != e.Size {
		return fmt.Errorf("extract: %s: restored %d bytes, catalog records %d", relPath, st.Size(), e.Size)
	}

	restoreMetadata(fullPath, e.Mode, e.MtimeNs, e.UID, e.GID)
	*count++
	return nil
}

// resolveTransform returns the Transform for the given ID, or nil for raw (empty string).
// Returns an error if the archive requires a transform that is not registered.
// The whole registry is searched, decode-only entries included: an archive written
// by an older binary may carry a transform the planner no longer selects.
func resolveTransform(id string) (marc.Transform, error) {
	if id == "" {
		return nil, nil
	}
	for _, entry := range plan.Registry {
		if string(entry.Transform.ID()) == id {
			return entry.Transform, nil
		}
	}
	return nil, fmt.Errorf("archive requires transform %q which is not registered", id)
}

func restoreMetadata(path string, mode fs.FileMode, mtimeNs int64, uid, gid uint32) {
	if err := os.Lchown(path, int(uid), int(gid)); err != nil {
		slog.Debug("extract: chown failed (requires root)", "path", path, "err", err)
	}
	if err := os.Chmod(path, mode.Perm()); err != nil {
		slog.Warn("extract: chmod failed", "path", path, "err", err)
	}
	if mtimeNs != 0 {
		mtime := time.Unix(0, mtimeNs)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			slog.Warn("extract: chtimes failed", "path", path, "err", err)
		}
	}
}
