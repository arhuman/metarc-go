//go:build go1.18

package store_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzExtract_corrupt verifies that corrupted archive files never cause panics
// when opening, walking entries, and reading blobs. Only errors are acceptable.
func FuzzExtract_corrupt(f *testing.F) {
	// Seed: a valid small archive.
	seedData := buildSeedArchive(f)
	f.Add(seedData)

	// Seed: truncated at various points.
	for _, n := range []int{0, 4, 8, 16, 24, len(seedData) / 2, len(seedData) - 1} {
		if n >= 0 && n <= len(seedData) {
			f.Add(seedData[:n])
		}
	}

	// Seed: corrupted magic bytes.
	badMagic := make([]byte, len(seedData))
	copy(badMagic, seedData)
	badMagic[0] = 0xFF
	badMagic[1] = 0xFE
	f.Add(badMagic)

	// Seed: corrupted footer offsets (flip bytes near end).
	if len(seedData) > 24 {
		badFooter := make([]byte, len(seedData))
		copy(badFooter, seedData)
		// Corrupt catalog offset bytes (first 8 bytes of footer).
		footerStart := len(seedData) - 24
		badFooter[footerStart] ^= 0xFF
		badFooter[footerStart+1] ^= 0xFF
		f.Add(badFooter)
	}

	// Seed: flip single byte in the middle of the blob region.
	if len(seedData) > 50 {
		flipped := make([]byte, len(seedData))
		copy(flipped, seedData)
		flipped[len(seedData)/2] ^= 0xFF
		f.Add(flipped)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "fuzzed.marc")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return
		}

		// Opening should either succeed or return an error, never panic.
		r, err := store.OpenReader(path)
		if err != nil {
			return // expected for corrupt data
		}
		defer func() { _ = r.Close() }()

		// Walking entries and reading every blob must not panic.
		_ = r.WalkEntries(func(_ string, e store.EntryRow) error {
			if e.BlobID != 0 {
				rc, err := r.OpenBlob(e.BlobID)
				if err != nil {
					return nil // error is fine, panic is not
				}
				// Drain the blob to exercise decompression.
				_, _ = io.ReadAll(rc)
				_ = rc.Close()
			}
			return nil
		})
	})
}

// buildSeedArchive creates a small valid .marc archive and returns its bytes.
func buildSeedArchive(f *testing.F) []byte {
	f.Helper()
	tmp := f.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello fuzz extract"), 0o644); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("hello fuzz extract"), 0o644); err != nil {
		f.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "c.txt"), []byte("unique content"), 0o644); err != nil {
		f.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "seed.marc")
	w, err := store.OpenWriter(marcPath)
	if err != nil {
		f.Fatal(err)
	}
	ctx := context.Background()

	// Write entries.
	entries := []struct {
		name string
		dir  bool
	}{
		{".", true},
		{"a.txt", false},
		{"b.txt", false},
		{"sub", true},
		{"sub/c.txt", false},
	}
	for _, entry := range entries {
		path := filepath.Join(srcDir, entry.name)
		if entry.name == "." {
			path = srcDir
		}
		info, err := os.Stat(path)
		if err != nil {
			f.Fatal(err)
		}
		if err := w.WriteEntry(ctx, marc.Entry{
			Path:    path,
			RelPath: entry.name,
			Info:    info,
		}); err != nil {
			f.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		f.Fatal(err)
	}
	return data
}
