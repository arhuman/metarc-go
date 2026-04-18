// Package scan walks the filesystem and collects basic metadata
// (size, mode) without analyzing content.
package scan

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/arhuman/metarc/pkg/marc"
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

			entry := marc.Entry{
				Path:       path,
				RelPath:    rel,
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
	}()

	return ch
}
