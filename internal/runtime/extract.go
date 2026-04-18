package runtime

import (
	"context"
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
func Extract(ctx context.Context, marcPath, destDir string) error {
	slog.Info("extracting", "archive", marcPath, "dest", destDir)

	r, err := store.OpenReader(marcPath)
	if err != nil {
		return fmt.Errorf("runtime.Extract: %w", err)
	}
	defer func() { _ = r.Close() }()

	var count int64
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

		fullPath := filepath.Join(destDir, relPath)

		// Prevent path traversal (Zip Slip): ensure resolved path stays within destDir.
		if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("extract: path traversal attempt: %s", relPath)
		}

		mode := e.Mode

		if mode&fs.ModeSymlink != 0 {
			// Validate symlink target doesn't escape destDir.
			resolvedTarget := e.LinkTarget
			if !filepath.IsAbs(resolvedTarget) {
				resolvedTarget = filepath.Join(filepath.Dir(fullPath), resolvedTarget)
			}
			if !strings.HasPrefix(filepath.Clean(resolvedTarget), filepath.Clean(destDir)+string(os.PathSeparator)) {
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

		return extractFile(ctx, r, fullPath, relPath, e, &count)
	})

	if err != nil {
		return fmt.Errorf("runtime.Extract: %w", err)
	}

	// Second pass: restore directory mtimes (children may have updated them).
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	err = r.WalkEntries(func(relPath string, e store.EntryRow) error {
		if relPath == "." || !e.Mode.IsDir() {
			return nil
		}
		fullPath := filepath.Join(destDir, relPath)
		if !strings.HasPrefix(filepath.Clean(fullPath), cleanDest) {
			return nil
		}
		restoreMetadata(fullPath, e.Mode, e.MtimeNs, e.UID, e.GID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("runtime.Extract: restore dir times: %w", err)
	}

	slog.Info("extract complete", "entries", count)
	return nil
}

func extractFile(ctx context.Context, r *store.Reader, fullPath, relPath string, e store.EntryRow, count *int64) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("extract: mkdirall for %s: %w", relPath, err)
	}

	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, e.Mode.Perm())
	if err != nil {
		return fmt.Errorf("extract: create %s: %w", relPath, err)
	}
	defer func() { _ = f.Close() }()

	if e.BlobID != 0 {
		t, err := resolveTransform(e.Transform)
		if err != nil {
			return fmt.Errorf("extract: %s: %w", relPath, err)
		}
		if t != nil {
			// Use the transform's Reverse to reconstruct the file.
			result := marc.Result{
				BlobIDs: []marc.BlobID{marc.BlobID(e.BlobID)},
				Params:  e.Params,
			}
			if err := t.Reverse(ctx, result, r.BlobReaderAdapter(), f); err != nil {
				return fmt.Errorf("extract: reverse %s for %s: %w", e.Transform, relPath, err)
			}
		} else {
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

	restoreMetadata(fullPath, e.Mode, e.MtimeNs, e.UID, e.GID)
	*count++
	return nil
}

// resolveTransform returns the Transform for the given ID, or nil for raw (empty string).
// Returns an error if the archive requires a transform that is not registered.
func resolveTransform(id string) (marc.Transform, error) {
	if id == "" {
		return nil, nil
	}
	for _, t := range plan.Registry {
		if string(t.ID()) == id {
			return t, nil
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
