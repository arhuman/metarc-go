package runtime_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc/internal/runtime"
	"github.com/arhuman/metarc/internal/store"
)

// TestArchive_workers verifies that archiving with different worker counts
// produces identical blob tables (same count and same SHAs).
func TestArchive_workers(t *testing.T) {
	// Build a source directory with duplicates and unique files.
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sharedContent := bytes.Repeat([]byte("shared data for worker test "), 50)
	for i := range 5 {
		name := "shared" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(srcDir, name), sharedContent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		name := "unique" + string(rune('a'+i)) + ".txt"
		content := make([]byte, 128)
		rand.Read(content)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "nested.txt"), []byte("nested file"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Archive with 1 worker.
	marc1 := filepath.Join(tmp, "w1.marc")
	if err := runtime.Archive(ctx, marc1, srcDir, "zstd", false, 1); err != nil {
		t.Fatalf("archive workers=1: %v", err)
	}

	// Archive with 4 workers.
	marc4 := filepath.Join(tmp, "w4.marc")
	if err := runtime.Archive(ctx, marc4, srcDir, "zstd", false, 4); err != nil {
		t.Fatalf("archive workers=4: %v", err)
	}

	// Compare blob counts and SHAs.
	shas1 := collectBlobSHAs(t, marc1)
	shas4 := collectBlobSHAs(t, marc4)

	if len(shas1) != len(shas4) {
		t.Fatalf("blob count mismatch: workers=1 has %d, workers=4 has %d", len(shas1), len(shas4))
	}

	for sha := range shas1 {
		if !shas4[sha] {
			t.Fatalf("SHA present in workers=1 but missing in workers=4: %x", sha)
		}
	}

	t.Logf("both archives have %d blobs with identical SHAs", len(shas1))

	// Verify both round-trip correctly.
	for _, tc := range []struct {
		name, marcPath string
	}{
		{"workers=1", marc1},
		{"workers=4", marc4},
	} {
		restoreDir := filepath.Join(tmp, "restore-"+tc.name)
		if err := runtime.Extract(ctx, tc.marcPath, restoreDir); err != nil {
			t.Fatalf("extract %s: %v", tc.name, err)
		}
		compareFiles(t, srcDir, restoreDir)
	}
}

// collectBlobSHAs opens an archive and returns the set of blob SHA hashes.
func collectBlobSHAs(t *testing.T, marcPath string) map[[32]byte]bool {
	t.Helper()
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	shas := make(map[[32]byte]bool)
	rows, err := r.QueryBlobSHAs()
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range rows {
		shas[sha] = true
	}
	return shas
}

// compareFiles walks orig and verifies every file exists in restored with matching content.
func compareFiles(t *testing.T, orig, restored string) {
	t.Helper()
	err := filepath.WalkDir(orig, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(orig, path)
		origData, _ := os.ReadFile(path)
		restoredData, err := os.ReadFile(filepath.Join(restored, rel))
		if err != nil {
			t.Errorf("missing in restored: %s", rel)
			return nil
		}
		if !bytes.Equal(origData, restoredData) {
			t.Errorf("content mismatch: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
