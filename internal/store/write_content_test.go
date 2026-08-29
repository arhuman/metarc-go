package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zeebo/blake3"
)

// blobRow mirrors one blobs table row, minus the offsets that depend on write order.
type blobRow struct {
	sha        []byte
	ulen       int64
	clen       int64
	compressed int
}

func readBlobRows(t *testing.T, marcPath string) []blobRow {
	t.Helper()
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	rows, err := r.db.Query(`SELECT sha, ulen, clen, compressed FROM blobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var out []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.sha, &b.ulen, &b.clen, &b.compressed); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestWriteEntryContent_matchesWriteEntryWithSHA pins the contract that passing
// the already-read content produces the same blob rows as letting the store
// reopen the file.
func TestWriteEntryContent_matchesWriteEntryWithSHA(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("line of content for the write path\n"), 200)
	e := createTestFile(t, srcDir, "sample.txt", content)

	h := blake3.New()
	_, _ = h.Write(content)
	var sha [32]byte
	copy(sha[:], h.Sum(nil))

	ctx := context.Background()

	shaPath := filepath.Join(tmp, "sha.marc")
	w, err := OpenWriter(shaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEntryWithSHA(ctx, e, sha); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	dataPath := filepath.Join(tmp, "data.marc")
	w, err = OpenWriter(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEntryContent(ctx, e, sha, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := readBlobRows(t, shaPath)
	got := readBlobRows(t, dataPath)
	if len(want) != len(got) {
		t.Fatalf("blob row count: WriteEntryWithSHA %d, WriteEntryContent %d", len(want), len(got))
	}
	if len(want) == 0 {
		t.Fatal("no blob rows written")
	}
	for i := range want {
		if !bytes.Equal(want[i].sha, got[i].sha) {
			t.Errorf("row %d sha mismatch: %x vs %x", i, want[i].sha, got[i].sha)
		}
		if want[i].ulen != got[i].ulen || want[i].clen != got[i].clen || want[i].compressed != got[i].compressed {
			t.Errorf("row %d mismatch: %+v vs %+v", i, want[i], got[i])
		}
	}
}

// TestWriteEntryContent_nilDataReadsFromDisk pins that a nil data falls back to
// the reopen path rather than archiving an empty blob.
func TestWriteEntryContent_nilDataReadsFromDisk(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("nil data falls back\n"), 50)
	e := createTestFile(t, srcDir, "fallback.txt", content)

	marcPath := filepath.Join(tmp, "nil.marc")
	w, err := OpenWriter(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEntryContent(context.Background(), e, [32]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	rows := readBlobRows(t, marcPath)
	if len(rows) != 1 {
		t.Fatalf("expected 1 blob row, got %d", len(rows))
	}
	if rows[0].ulen != int64(len(content)) {
		t.Errorf("ulen = %d, want %d", rows[0].ulen, len(content))
	}
}
