package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// createTestFile creates a file with the given content and returns a marc.Entry.
func createTestFile(t *testing.T, dir, name string, content []byte) marc.Entry {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return marc.Entry{
		Path:    path,
		RelPath: name,
		Info:    info,
	}
}

// queryBlobCount queries the blobs table directly via the reader's db connection.
func queryBlobCount(t *testing.T, r *Reader) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDedup_identicalFiles(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("identical content for dedup test")
	for i := range 5 {
		name := filepath.Join(srcDir, "file"+string(rune('a'+i)))
		if err := os.WriteFile(name, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 5 {
		name := "file" + string(rune('a'+i))
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
	n := queryBlobCount(t, r)
	if n != 1 {
		t.Fatalf("expected 1 blob row (dedup), got %d", n)
	}
}

func TestDedup_differentFiles(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 5 {
		name := "file" + string(rune('a'+i))
		content := make([]byte, 64)
		rand.Read(content)
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
	n := queryBlobCount(t, r)
	if n != 5 {
		t.Fatalf("expected 5 blob rows, got %d", n)
	}
}

func TestDedup_partialDuplicate(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 3 unique files.
	for i := range 3 {
		name := "unique" + string(rune('a'+i))
		content := make([]byte, 64)
		rand.Read(content)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	// 1 shared content, written as 2 files.
	shared := []byte("shared content for partial dedup test")
	for i := range 2 {
		name := "dup" + string(rune('a'+i))
		e := createTestFile(t, srcDir, name, shared)
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
	n := queryBlobCount(t, r)
	if n != 4 {
		t.Fatalf("expected 4 blob rows (3 unique + 1 shared), got %d", n)
	}
}

func TestCompressor_zstd(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Highly compressible content.
	content := bytes.Repeat([]byte("compressible text data "), 1000)

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath, WithCompressor("zstd"))
	if err != nil {
		t.Fatal(err)
	}

	e := createTestFile(t, srcDir, "big.txt", content)
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Check that clen < ulen.
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	var clen, ulen int64
	if err := r.db.QueryRow(`SELECT clen, ulen FROM blobs LIMIT 1`).Scan(&clen, &ulen); err != nil {
		t.Fatal(err)
	}

	if clen >= ulen {
		t.Fatalf("zstd compression did not reduce size: clen=%d, ulen=%d", clen, ulen)
	}
	t.Logf("zstd: clen=%d ulen=%d ratio=%.2f", clen, ulen, float64(clen)/float64(ulen))
}

func TestCompressor_none(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("some content")

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath, WithCompressor("none"))
	if err != nil {
		t.Fatal(err)
	}

	e := createTestFile(t, srcDir, "file.txt", content)
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Check that clen == ulen (no compression).
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	var clen, ulen int64
	if err := r.db.QueryRow(`SELECT clen, ulen FROM blobs LIMIT 1`).Scan(&clen, &ulen); err != nil {
		t.Fatal(err)
	}

	if clen != ulen {
		t.Fatalf("expected clen == ulen with no compression, got clen=%d ulen=%d", clen, ulen)
	}
}

func queryPlanLogCount(t *testing.T, r *Reader) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM plan_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPlanLog_written(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 3 {
		name := "file" + string(rune('a'+i))
		content := []byte("content of " + name)
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

	n := queryPlanLogCount(t, r)
	if n != 3 {
		t.Fatalf("expected 3 plan_log rows, got %d", n)
	}

	// With transform chaining, plain text files are written raw (no transform
	// handles them), so applied=0 for all entries.
	var applied int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM plan_log WHERE applied = 1`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("expected 0 applied plan_log rows (plain text files), got %d", applied)
	}
}

func TestPlanLog_deleted(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	e := createTestFile(t, srcDir, "file.txt", []byte("content"))
	if err := w.WriteEntry(ctx, e); err != nil {
		t.Fatal(err)
	}

	// Delete plan_log before close.
	if err := w.DeletePlanLog(); err != nil {
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

	n := queryPlanLogCount(t, r)
	if n != 0 {
		t.Fatalf("expected 0 plan_log rows after delete, got %d", n)
	}
}

func TestPlanLog_kept(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 5 {
		name := "file" + string(rune('a'+i))
		e := createTestFile(t, srcDir, name, []byte("content "+name))
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Close WITHOUT calling DeletePlanLog (simulates --keep-plan-log).
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	n := queryPlanLogCount(t, r)
	if n != 5 {
		t.Fatalf("expected 5 plan_log rows (kept), got %d", n)
	}
}

func TestBlobIntegrity(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("integrity check content "), 500)

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath, WithCompressor("zstd"))
	if err != nil {
		t.Fatal(err)
	}

	e := createTestFile(t, srcDir, "data.bin", content)
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Read it back.
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	// Find the blob id.
	var blobID int64
	err = r.WalkEntries(func(_ string, row EntryRow) error {
		if row.BlobID != 0 {
			blobID = row.BlobID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if blobID == 0 {
		t.Fatal("no blob found")
	}

	rc, err := r.OpenBlob(blobID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("blob content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}
}
