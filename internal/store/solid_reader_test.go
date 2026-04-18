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
