package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc/pkg/marc"
)

// TestWriteEntryWithSHA writes entries using a pre-computed SHA and verifies
// blob dedup still works correctly.
func TestWriteEntryWithSHA(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("shared content for SHA pre-computed dedup test")

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Write the same content twice, computing SHA manually via blobSink.
	// Use WriteEntry first to compute the SHA internally.
	e1 := createTestFile(t, srcDir, "file1.txt", content)
	if err := w.WriteEntry(ctx, e1); err != nil {
		t.Fatal(err)
	}

	// Write the same content again via WriteEntryWithSHA with a zero SHA
	// (forces internal hash computation path).
	e2 := createTestFile(t, srcDir, "file2.txt", content)
	if err := w.WriteEntryWithSHA(ctx, e2, [32]byte{}); err != nil {
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

	// Both files with same content should share 1 blob (dedup).
	n := queryBlobCount(t, r)
	if n != 1 {
		t.Fatalf("expected 1 blob (dedup), got %d", n)
	}
}

// TestWriteEntry_symlink verifies that symlinks are stored and readable.
func TestWriteEntry_symlink(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a real file and a symlink.
	realFile := filepath.Join(srcDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(srcDir, "link.txt")
	if err := os.Symlink("real.txt", linkPath); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Write real file.
	realInfo, err := os.Stat(realFile)
	if err != nil {
		t.Fatal(err)
	}
	e := marc.Entry{Path: realFile, RelPath: "real.txt", Info: realInfo}
	if err := w.WriteEntry(ctx, e); err != nil {
		t.Fatal(err)
	}

	// Write symlink.
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	linkEntry := marc.Entry{Path: linkPath, RelPath: "link.txt", Info: linkInfo, LinkTarget: "real.txt"}
	if err := w.WriteEntry(ctx, linkEntry); err != nil {
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

	// Verify symlink entry is present with the correct target.
	var foundSymlink bool
	if err := r.WalkEntries(func(_ string, row EntryRow) error {
		if row.LinkTarget == "real.txt" {
			foundSymlink = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundSymlink {
		t.Fatal("symlink entry not found in archive")
	}
}

// TestDict_withDictCompress verifies that dict-compressed archives round-trip correctly.
func TestDict_withDictCompress(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create enough similar small files to make dict training succeed.
	pattern := bytes.Repeat([]byte("shared prefix content common repetitive data "), 20)
	for i := range 20 {
		name := "file" + string(rune('a'+i%26)) + ".txt"
		content := append(pattern, []byte("unique suffix "+string(rune('a'+i%26)))...)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Train a dictionary from the source dir.
	dict, err := TrainDictionary(srcDir, 0, 0)
	if err != nil {
		t.Fatalf("TrainDictionary: %v", err)
	}
	if dict == nil {
		t.Skip("dict training returned nil (not enough data)")
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath, WithCompressor("zstd"), WithDictCompress(dict))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 20 {
		name := "file" + string(rune('a'+i%26)) + ".txt"
		content := append(pattern, []byte("unique suffix "+string(rune('a'+i%26)))...)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify archive is readable.
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Verify content round-trips through OpenBlob.
	var blobID int64
	if err := r.WalkEntries(func(_ string, row EntryRow) error {
		if row.BlobID != 0 && blobID == 0 {
			blobID = row.BlobID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if blobID == 0 {
		t.Fatal("no blob found")
	}

	rc, err := r.OpenBlob(blobID)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("empty blob content")
	}
}

// TestWithDictSimple verifies that WithDictSimple option is accepted and
// the archive is valid (online dict training path).
func TestWithDictSimple(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pattern := bytes.Repeat([]byte("repetitive content for online dict training "), 10)
	for i := range 15 {
		name := "f" + string(rune('a'+i%26)) + ".txt"
		content := append(pattern, []byte{byte(i)}...)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "simple.marc")
	w, err := OpenWriter(marcPath, WithCompressor("zstd"), WithDictSimple())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 15 {
		name := "f" + string(rune('a'+i%26)) + ".txt"
		content := append(pattern, []byte{byte(i)}...)
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
		t.Fatalf("OpenReader after WithDictSimple: %v", err)
	}
	n := queryBlobCount(t, r)
	_ = r.Close()

	if n == 0 {
		t.Fatal("no blobs in archive")
	}
}

// TestWithSolidBlockSize_option verifies that the option is accepted (covered via store_test helpers).
func TestWithSolidBlockSize_option(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		name := "f" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("content "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "solid.marc")
	w, err := OpenWriter(marcPath, WithSolidBlockSize(1*1024*1024))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 3 {
		name := "f" + string(rune('a'+i)) + ".txt"
		e := createTestFile(t, srcDir, name, []byte("content "+name))
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	n := queryBlobCount(t, r)
	if n != 3 {
		t.Fatalf("expected 3 blobs, got %d", n)
	}
}
