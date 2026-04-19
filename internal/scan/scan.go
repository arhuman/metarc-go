// Package scan walks the filesystem and collects basic metadata
// (size, mode) without analyzing content.
package scan

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// Walk returns a channel of Entry for every regular file, directory, and symlink
// under root. The channel is closed when the walk completes or ctx is cancelled.
// A single producer goroutine drives the walk.
func Walk(ctx context.Context, root string) <-chan marc.Entry {
	ch := make(chan marc.Entry, 1024)

	absRoot, err := filepath.Abs(root)
	if err != nil {
		slog.Error("scan: failed to resolve root", "root", root, "err", err)
		close(ch)
		return ch
	}

	go func() {
		defer close(ch)
		walkRoot(ctx, ch, absRoot, "")
	}()

	return ch
}

// ResolveMultiRoots resolves each path to an absolute path and returns the
// basename-prefixed source roots. It returns an error if two sources share
// the same basename, or if a source resolves to a filesystem root / empty
// basename (which would produce ambiguous archive paths).
//
// The returned slice preserves the input order.
func ResolveMultiRoots(roots []string) ([]MultiRoot, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("scan.ResolveMultiRoots: no source roots provided")
	}

	resolved := make([]MultiRoot, 0, len(roots))
	seen := make(map[string]string, len(roots))

	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("scan.ResolveMultiRoots: resolve %q: %w", root, err)
		}
		base := filepath.Base(abs)
		if base == "." || base == string(filepath.Separator) {
			return nil, fmt.Errorf("scan.ResolveMultiRoots: invalid source %q: cannot archive filesystem root or path with empty basename", root)
		}
		if prev, dup := seen[base]; dup {
			return nil, fmt.Errorf("scan.ResolveMultiRoots: basename collision: sources %q and %q share basename %q; rename one or archive them separately", prev, abs, base)
		}
		seen[base] = abs
		resolved = append(resolved, MultiRoot{AbsPath: abs, BaseName: base})
	}

	return resolved, nil
}

// MultiRoot is a source directory resolved to its absolute path together with
// the basename that will be used as its top-level directory inside the archive.
type MultiRoot struct {
	AbsPath  string
	BaseName string
}

// WalkMulti walks multiple source directories, prefixing each entry's RelPath
// with the source's BaseName. This enables archiving several independent
// directories into a single .marc archive: each source becomes a top-level
// directory named after its basename.
//
// Roots are walked sequentially in the order provided. The synthetic "." root
// entry emitted by Walk is not produced here; instead, each source root
// surfaces as a regular top-level directory entry with RelPath equal to its
// basename.
//
// Callers must validate roots first with ResolveMultiRoots to detect basename
// collisions and invalid sources.
func WalkMulti(ctx context.Context, roots []MultiRoot) <-chan marc.Entry {
	ch := make(chan marc.Entry, 1024)

	go func() {
		defer close(ch)
		for _, r := range roots {
			select {
			case <-ctx.Done():
				return
			default:
			}
			walkRoot(ctx, ch, r.AbsPath, r.BaseName)
		}
	}()

	return ch
}

// walkRoot walks absRoot and emits entries on ch. If prefix is non-empty, each
// RelPath is set to prefix joined with the path relative to absRoot; the
// synthetic "." entry for absRoot is emitted with RelPath = prefix. If prefix
// is empty, the single-root behavior is used: RelPath is the path relative to
// absRoot (absRoot itself becomes ".").
func walkRoot(ctx context.Context, ch chan<- marc.Entry, absRoot, prefix string) {
	err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("scan: walk error", "path", path, "err", err)
			return nil // skip entries we can't stat
		}

		// Only regular files, directories, and symlinks.
		if !d.IsDir() && !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		var linkTarget string
		if d.Type()&fs.ModeSymlink != 0 {
			t, err := os.Readlink(path)
			if err != nil {
				slog.Warn("scan: readlink error", "path", path, "err", err)
				return nil
			}
			linkTarget = t
		}

		info, err := d.Info()
		if err != nil {
			slog.Warn("scan: info error", "path", path, "err", err)
			return nil
		}

		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			slog.Warn("scan: rel error", "path", path, "err", err)
			return nil
		}

		relPath := rel
		if prefix != "" {
			if rel == "." {
				relPath = prefix
			} else {
				relPath = filepath.ToSlash(filepath.Join(prefix, rel))
			}
		}

		entry := marc.Entry{
			Path:       path,
			RelPath:    relPath,
			Info:       info,
			LinkTarget: linkTarget,
		}

		select {
		case ch <- entry:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})

	if err != nil && err != context.Canceled {
		slog.Error("scan: walk failed", "err", err)
	}
}
