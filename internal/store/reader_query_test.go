package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// TestQueryBlobSHAs verifies that BLAKE3 hashes stored in the blobs table
// can be retrieved via QueryBlobSHAs.
func TestQueryBlobSHAs(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	contents := [][]byte{
		[]byte("content alpha"),
		[]byte("content beta"),
		[]byte("content gamma"),
	}

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i, c := range contents {
		name := "file" + string(rune('a'+i))
		e := createTestFile(t, srcDir, name, c)
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

	shas, err := r.QueryBlobSHAs()
	if err != nil {
		t.Fatalf("QueryBlobSHAs: %v", err)
	}

	if len(shas) != 3 {
		t.Fatalf("expected 3 blob SHAs, got %d", len(shas))
	}

	// SHAs must be non-zero.
	zero := [32]byte{}
	for i, sha := range shas {
		if sha == zero {
			t.Errorf("blob SHA[%d] is all zeros", i)
		}
	}
}

// TestQueryPlanLog verifies aggregated plan_log statistics are correct.
func TestQueryPlanLog(t *testing.T) {
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
	for i := range 4 {
		name := "file" + string(rune('a'+i))
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
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	stats, err := r.QueryPlanLog()
	if err != nil {
		t.Fatalf("QueryPlanLog: %v", err)
	}

	if len(stats) == 0 {
		t.Fatal("expected at least one plan_log stat group")
	}

	// With transform chaining, plain text files are written raw (no transform
	// handles them), so total applied should be 0.
	var totalApplied int64
	for _, s := range stats {
		totalApplied += s.Applied
	}
	if totalApplied != 0 {
		t.Errorf("total applied entries: got %d, want 0", totalApplied)
	}
}

// TestQueryOverview verifies aggregate statistics from QueryOverview.
func TestQueryOverview(t *testing.T) {
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
	expectedSize := int64(0)
	for i := range 3 {
		content := []byte("content for file " + string(rune('a'+i)))
		name := "file" + string(rune('a'+i))
		e := createTestFile(t, srcDir, name, content)
		expectedSize += int64(len(content))
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

	ov, err := r.QueryOverview()
	if err != nil {
		t.Fatalf("QueryOverview: %v", err)
	}

	if ov.EntryCount != 3 {
		t.Errorf("EntryCount: got %d, want 3", ov.EntryCount)
	}
	if ov.BlobCount != 3 {
		t.Errorf("BlobCount: got %d, want 3", ov.BlobCount)
	}
	if ov.OriginalSize != expectedSize {
		t.Errorf("OriginalSize: got %d, want %d", ov.OriginalSize, expectedSize)
	}
	if ov.TotalUlen == 0 {
		t.Error("TotalUlen should be non-zero")
	}
	if ov.TotalClen == 0 {
		t.Error("TotalClen should be non-zero")
	}
}

// TestBlobReaderAdapter verifies that BlobReaderAdapter correctly wraps OpenBlob.
func TestBlobReaderAdapter(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("adapter test content for blob reading")
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

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	// Get a blob ID from WalkEntries.
	var blobID int64
	if err := r.WalkEntries(func(_ string, row EntryRow) error {
		if row.BlobID != 0 {
			blobID = row.BlobID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if blobID == 0 {
		t.Fatal("no blob found")
	}

	adapter := r.BlobReaderAdapter()
	if adapter == nil {
		t.Fatal("BlobReaderAdapter returned nil")
	}

	rc, err := adapter.Open(marc.BlobID(blobID))
	if err != nil {
		t.Fatalf("adapter.Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}
