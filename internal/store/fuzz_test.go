package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzReader_corrupt verifies that corrupted archive files return errors
// and never panic.
func FuzzReader_corrupt(f *testing.F) {
	// Seed with a valid archive.
	tmp := f.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello fuzz"), 0o644); err != nil {
		f.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "seed.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		f.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(srcDir, "a.txt"))
	if err := w.WriteEntry(context.Background(), marc.Entry{
		Path:    filepath.Join(srcDir, "a.txt"),
		RelPath: "a.txt",
		Info:    info,
	}); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}

	seedData, err := os.ReadFile(marcPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seedData)

	// Seed with truncated versions.
	for _, n := range []int{0, 4, 8, 16, 24, len(seedData) / 2} {
		if n <= len(seedData) {
			f.Add(seedData[:n])
		}
	}

	// Seed with corrupted magic.
	corrupt := make([]byte, len(seedData))
	copy(corrupt, seedData)
	corrupt[0] = 0xFF
	f.Add(corrupt)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		// Write the fuzzed data to a temp file.
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzzed.marc")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return
		}

		// Opening should either succeed or return an error, never panic.
		r, err := OpenReader(path)
		if err != nil {
			return // expected for corrupt data
		}
		defer func() { _ = r.Close() }()

		// Walking and opening blobs should also not panic.
		_ = r.WalkEntries(func(_ string, e EntryRow) error {
			if e.BlobID != 0 {
				rc, err := r.OpenBlob(e.BlobID)
				if err == nil {
					_ = rc.Close()
				}
			}
			return nil
		})
	})
}
