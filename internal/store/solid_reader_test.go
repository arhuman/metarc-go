package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestQuerySolidBlockCount verifies that QuerySolidBlockCount returns a
// non-zero count after creating an archive with solid blocks.
func TestQuerySolidBlockCount(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create multiple small files so they end up in a solid block.
	for i := range 5 {
		name := "f" + string(rune('a'+i)) + ".txt"
		content := bytes.Repeat([]byte("solid block test content "), 10)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "solid.marc")
	// Use a very small solid block size so files form blocks.
	w, err := OpenWriter(marcPath, WithSolidBlockSize(512))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 5 {
		name := "f" + string(rune('a'+i)) + ".txt"
		content := bytes.Repeat([]byte("solid block test content "), 10)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	count := r.QuerySolidBlockCount()
	if count == 0 {
		t.Fatal("expected solid blocks, got 0")
	}
	t.Logf("solid block count: %d", count)
}

// TestSolidBlock_roundtrip verifies that content written to a solid block archive
// can be fully recovered via OpenBlob.
func TestSolidBlock_roundtrip(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("roundtrip solid "), 50)

	for i := range 3 {
		name := "f" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "solid_rt.marc")
	// Small block size forces each file into its own solid block.
	w, err := OpenWriter(marcPath, WithSolidBlockSize(256))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 3 {
		name := "f" + string(rune('a'+i)) + ".txt"
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	// Walk and read back all file blobs.
	if err := r.WalkEntries(func(_ string, row EntryRow) error {
		if row.BlobID == 0 {
			return nil
		}
		rc, err := r.OpenBlob(row.BlobID)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		got, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, content) {
			t.Errorf("content mismatch: got %d bytes, want %d bytes", len(got), len(content))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// writeSolidArchive writes files into a solid archive and returns its block count.
func writeSolidArchive(t *testing.T, name string, blockSize int64, files []struct {
	name    string
	content []byte
},
) int64 {
	t.Helper()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, name)
	w, err := OpenWriter(marcPath, WithSolidBlockSize(blockSize))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, f := range files {
		e := createTestFile(t, srcDir, f.name, f.content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	return r.QuerySolidBlockCount()
}

// TestSolidBlock_extensionFlushesBlock verifies that a change in file
// extension flushes the current solid block once the block carries at least
// DefaultMinSolidBlockSize bytes, keeping large blocks extension-pure.
func TestSolidBlock_extensionFlushesBlock(t *testing.T) {
	// Each extension contributes more than the minimum, so the boundary
	// flushes. Extensions no transform claims keep this about block formation.
	big := DefaultMinSolidBlockSize + 1024
	files := []struct {
		name    string
		content []byte
	}{
		{"a1.aaa", bytes.Repeat([]byte("alpha content\n"), big/14)},
		{"b1.bbb", bytes.Repeat([]byte("beta content!!\n"), big/15)},
	}
	if got := writeSolidArchive(t, "ext_align.marc", 64<<20, files); got != 2 {
		t.Fatalf("expected 2 solid blocks (one per extension), got %d", got)
	}
}

// TestSolidBlock_smallExtensionsPool verifies the counterpart: extension
// groups too small to be worth their own zstd frame accumulate into a shared
// block instead of producing one undersized frame each.
func TestSolidBlock_smallExtensionsPool(t *testing.T) {
	var files []struct {
		name    string
		content []byte
	}
	for _, ext := range []string{"go", "txt", "md", "yaml", "json", "cfg", "ini", "toml"} {
		files = append(files, struct {
			name    string
			content []byte
		}{"f." + ext, bytes.Repeat([]byte(ext+" content\n"), 64)})
	}
	if got := writeSolidArchive(t, "ext_pool.marc", 1<<20, files); got != 1 {
		t.Fatalf("expected 8 tiny extension groups to pool into 1 solid block, got %d", got)
	}
}

// TestQuerySolidBlockCount_noSolidBlocks verifies that a plain (non-solid) archive
// returns 0.
func TestQuerySolidBlockCount_noSolidBlocks(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "plain.marc")
	w, err := OpenWriter(marcPath) // no WithSolidBlockSize
	if err != nil {
		t.Fatal(err)
	}

	e := createTestFile(t, srcDir, "a.txt", []byte("plain content"))
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	count := r.QuerySolidBlockCount()
	if count != 0 {
		t.Errorf("expected 0 solid blocks for plain archive, got %d", count)
	}
}
