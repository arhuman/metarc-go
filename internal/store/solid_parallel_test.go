package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zeebo/blake3"
)

// withSolidWorkers pins the solid compression pool size for one test.
func withSolidWorkers(t *testing.T, n int) {
	t.Helper()
	prev := solidCompressWorkers
	solidCompressWorkers = n
	t.Cleanup(func() { solidCompressWorkers = prev })
}

// writeSolidFixture archives a deterministic multi-block fixture and returns
// the archive path. blockSize is small so a modest fixture spans many blocks.
func writeSolidFixture(t *testing.T, dir string, blockSize int64, files int) string {
	t.Helper()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(dir, "solid.marc")
	w, err := OpenWriter(marcPath, WithSolidBlockSize(blockSize))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range files {
		name := fmt.Sprintf("f%03d.txt", i)
		content := []byte(fmt.Sprintf("block fixture line %d\n", i))
		content = bytes.Repeat(content, 40)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return marcPath
}

// TestSolidParallel_bytesIdenticalAcrossWorkerCounts pins the contract that
// parallel block compression changes only when blocks are compressed, never
// what the archive contains: the same fixture must produce the same blob region
// with one worker and with four.
//
// The comparison stops at the catalog chunk, because the catalog records the
// fixture's file mtimes and two runs cannot share them.
func TestSolidParallel_bytesIdenticalAcrossWorkerCounts(t *testing.T) {
	const blockSize = 4096
	const files = 120

	blobRegion := func(workers int) []byte {
		withSolidWorkers(t, workers)
		path := writeSolidFixture(t, t.TempDir(), blockSize, files)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Footer: catalog_offset(8) | blob_region_offset(8) | hash(8).
		catalogOffset := binary.BigEndian.Uint64(data[len(data)-24 : len(data)-16])
		return data[:catalogOffset]
	}

	serial := blobRegion(1)
	parallel := blobRegion(4)

	if !bytes.Equal(serial, parallel) {
		t.Fatalf("blob region differs between 1 and 4 compression workers: %d vs %d bytes",
			len(serial), len(parallel))
	}
}

// solidBlobRow mirrors the blobs columns the parallel completion stage fills in.
type solidBlobRow struct {
	blockID     int64
	offset      int64
	clen        int64
	ulen        int64
	blockOffset int64
}

// querySolidBlobRows reads back every solid-block blob row in block order.
func querySolidBlobRows(t *testing.T, r *Reader) []solidBlobRow {
	t.Helper()
	rows, err := r.db.Query(
		`SELECT block_id, offset, clen, ulen, block_offset FROM blobs WHERE block_id IS NOT NULL ORDER BY block_id, block_offset`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var out []solidBlobRow
	for rows.Next() {
		var b solidBlobRow
		if err := rows.Scan(&b.blockID, &b.offset, &b.clen, &b.ulen, &b.blockOffset); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSolidParallel_blobRowsResolved verifies that the completion stage fills
// in every placeholder: each block's blobs point at one chunk offset, that
// offset grows with the block id, and no blob claims bytes past its block.
func TestSolidParallel_blobRowsResolved(t *testing.T) {
	withSolidWorkers(t, 4)
	const blockSize = 4096

	marcPath := writeSolidFixture(t, t.TempDir(), blockSize, 120)
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	rows := querySolidBlobRows(t, r)
	if len(rows) == 0 {
		t.Fatal("no solid blob rows")
	}

	blockOffsets := make(map[int64]int64)
	var maxBlockID, prevOffset int64 = -1, -1
	for _, b := range rows {
		if b.offset < 0 || b.clen <= 0 {
			t.Fatalf("block %d: unresolved placeholder offset=%d clen=%d", b.blockID, b.offset, b.clen)
		}
		if known, ok := blockOffsets[b.blockID]; ok {
			if known != b.offset {
				t.Fatalf("block %d: blobs disagree on chunk offset: %d vs %d", b.blockID, known, b.offset)
			}
		} else {
			if b.blockID > maxBlockID && b.offset <= prevOffset {
				t.Fatalf("block %d at offset %d does not follow block %d at offset %d",
					b.blockID, b.offset, maxBlockID, prevOffset)
			}
			blockOffsets[b.blockID] = b.offset
			maxBlockID, prevOffset = b.blockID, b.offset
		}
		if b.blockOffset+b.ulen > blockSize && b.ulen <= blockSize {
			t.Fatalf("block %d: blob at %d+%d overruns the %d byte block",
				b.blockID, b.blockOffset, b.ulen, blockSize)
		}
	}
	if len(blockOffsets) < 2 {
		t.Fatalf("fixture produced %d blocks, expected several", len(blockOffsets))
	}
}

// TestSolidParallel_oversizedBlobOrdering verifies that a blob too large for a
// block lands after every block submitted before it. The oversized path writes
// itself, so it is the one place where a deferred block could overtake a chunk.
func TestSolidParallel_oversizedBlobOrdering(t *testing.T) {
	withSolidWorkers(t, 4)
	const blockSize = 4096

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(dir, "mixed.marc")
	w, err := OpenWriter(marcPath, WithSolidBlockSize(blockSize))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	want := make(map[string][]byte)
	writeOne := func(name string, content []byte) {
		e := createTestFile(t, srcDir, name, content)
		sum := blake3.Sum256(content)
		if err := w.WriteEntryWithSHA(ctx, e, sum); err != nil {
			t.Fatal(err)
		}
		want[name] = content
	}

	for i := range 40 {
		writeOne(fmt.Sprintf("a%03d.txt", i), bytes.Repeat([]byte(fmt.Sprintf("small %d\n", i)), 40))
	}
	// Larger than blockSize, so it takes the standalone streamed path.
	writeOne("big.txt", bytes.Repeat([]byte("oversized payload line\n"), 4000))
	for i := range 40 {
		writeOne(fmt.Sprintf("z%03d.txt", i), bytes.Repeat([]byte(fmt.Sprintf("tail %d\n", i)), 40))
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	rows := querySolidBlobRows(t, r)
	var prevID, prevOffset int64 = -1, -1
	for _, b := range rows {
		if b.blockID == prevID {
			continue
		}
		if b.offset <= prevOffset {
			t.Fatalf("block %d at offset %d does not follow block %d at offset %d",
				b.blockID, b.offset, prevID, prevOffset)
		}
		prevID, prevOffset = b.blockID, b.offset
	}

	got := make(map[string][]byte, len(want))
	if err := r.WalkEntries(func(name string, row EntryRow) error {
		if row.BlobID == 0 {
			return nil
		}
		var buf bytes.Buffer
		rc, err := r.OpenBlob(row.BlobID)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		if _, err := buf.ReadFrom(rc); err != nil {
			return err
		}
		got[filepath.Base(name)] = buf.Bytes()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for name, content := range want {
		if !bytes.Equal(got[name], content) {
			t.Fatalf("%s: round-trip mismatch (%d bytes read, %d written)", name, len(got[name]), len(content))
		}
	}
}

// TestSolidParallel_workerErrorSurfaces verifies that a compression failure is
// reported by the next accumulator call and by Close, rather than leaving a
// hole in the blob region that the catalog still points into.
func TestSolidParallel_workerErrorSurfaces(t *testing.T) {
	withSolidWorkers(t, 2)

	dir := t.TempDir()
	marcPath := filepath.Join(dir, "err.marc")
	w, err := OpenWriter(marcPath, WithSolidBlockSize(4096))
	if err != nil {
		t.Fatal(err)
	}

	sa := w.solidAcc
	if err := sa.startWorkers(); err != nil {
		t.Fatal(err)
	}

	// Inject a failed result for a block that was never submitted: the
	// accumulator must keep the error and refuse every later operation.
	sa.inFlight++
	if err := sa.accept(solidResult{blockID: 0, err: os.ErrInvalid}); err == nil {
		t.Fatal("accept: want the worker error, got nil")
	}

	if _, err := sa.addBlob([]byte("payload"), [32]byte{1}, [32]byte{}); err == nil {
		t.Fatal("addBlob after a worker error: want the sticky error, got nil")
	}
	if err := sa.flush(); err == nil {
		t.Fatal("flush after a worker error: want the sticky error, got nil")
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close after a worker error: want the sticky error, got nil")
	}
}
