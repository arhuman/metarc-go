package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
	"github.com/zeebo/blake3"
)

// soleStreamBlockSize is small enough that a fixture above it becomes a
// standalone solid block, which is the shape the streaming read path handles.
const soleStreamBlockSize = 64 << 10

// compressibleBytes builds n bytes that zstd shrinks substantially while still
// carrying enough variation to catch a misaligned or truncated read.
func compressibleBytes(n int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	var buf bytes.Buffer
	buf.Grow(n + 32)
	for buf.Len() < n {
		fmt.Fprintf(&buf, "%s %d %s\n", words[rng.Intn(len(words))], rng.Intn(1000), words[rng.Intn(len(words))])
	}
	return buf.Bytes()[:n]
}

// writeNamedSolidFixture archives files into a solid-block archive at blockSize and
// returns the archive path.
func writeNamedSolidFixture(t *testing.T, name string, blockSize int64, files map[string][]byte) string {
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
	for fname, content := range files {
		e := createTestFile(t, srcDir, fname, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return marcPath
}

// readBlob reads a blob whole through OpenBlob.
func readBlob(t *testing.T, r *Reader, blobID int64) []byte {
	t.Helper()
	rc, err := r.OpenBlob(blobID)
	if err != nil {
		t.Fatalf("OpenBlob(%d): %v", blobID, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read blob %d: %v", blobID, err)
	}
	return got
}

// TestSoleMemberBlock_streamsRoundTrip pins the streaming read path: a file
// larger than the solid block size becomes a sole-member block, and its bytes
// must come back exactly, twice in a row, without the block being cached.
func TestSoleMemberBlock_streamsRoundTrip(t *testing.T) {
	content := compressibleBytes(soleStreamBlockSize*3, 11)
	marcPath := writeNamedSolidFixture(t, "sole.marc", soleStreamBlockSize, map[string][]byte{
		"big.dat": content,
	})

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	if len(r.soleBlocks) != 1 {
		t.Fatalf("sole-member block set has %d entries, want 1", len(r.soleBlocks))
	}

	ids, err := r.QueryBlobIDs()
	if err != nil {
		t.Fatalf("QueryBlobIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("archive holds %d blobs, want 1", len(ids))
	}

	// Read twice: the streamed reader must be rebuilt per OpenBlob, not a
	// single-use decoder over a shared section.
	for attempt := range 2 {
		if got := readBlob(t, r, ids[0]); !bytes.Equal(got, content) {
			t.Fatalf("attempt %d: got %d bytes, want %d", attempt, len(got), len(content))
		}
	}

	if len(r.solidCache) != 0 {
		t.Fatalf("solid cache holds %d blocks after streaming a sole-member block, want 0", len(r.solidCache))
	}

	if err := r.VerifyBlob(ids[0]); err != nil {
		t.Fatalf("VerifyBlob on a streamed sole-member blob: %v", err)
	}
}

// TestSharedBlock_stillCached verifies the multi-blob path is untouched: such a
// block is decoded whole into the cache and every member reads correctly.
func TestSharedBlock_stillCached(t *testing.T) {
	files := map[string][]byte{}
	want := map[string][]byte{}
	for i := range 4 {
		name := fmt.Sprintf("part%d.dat", i)
		content := compressibleBytes(4<<10, int64(100+i))
		files[name] = content
		want[name] = content
	}

	marcPath := writeNamedSolidFixture(t, "shared.marc", soleStreamBlockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	if len(r.soleBlocks) != 0 {
		t.Fatalf("sole-member block set has %d entries, want 0", len(r.soleBlocks))
	}

	seen := 0
	if err := r.WalkEntries(func(relPath string, e EntryRow) error {
		if e.BlobID == 0 {
			return nil
		}
		expect, ok := want[relPath]
		if !ok {
			return fmt.Errorf("unexpected entry %q", relPath)
		}
		if got := readBlob(t, r, e.BlobID); !bytes.Equal(got, expect) {
			t.Errorf("%s: content mismatch (%d bytes, want %d)", relPath, len(got), len(expect))
		}
		seen++
		return nil
	}); err != nil {
		t.Fatalf("WalkEntries: %v", err)
	}
	if seen != len(want) {
		t.Fatalf("read %d blobs, want %d", seen, len(want))
	}

	// Two blobs out of the same block means the cached path served at least one
	// of them from memory rather than decoding twice.
	if len(r.solidCache) != 1 {
		t.Fatalf("solid cache holds %d blocks, want 1", len(r.solidCache))
	}
}

// TestSoleMemberBlock_rejectsNonZeroBlockOffset pins the forged-catalog case.
// The writer places the single blob of a sole-member block at offset 0, so a
// catalog claiming otherwise must error rather than stream from a position the
// zstd frame does not begin at.
func TestSoleMemberBlock_rejectsNonZeroBlockOffset(t *testing.T) {
	content := compressibleBytes(soleStreamBlockSize*3, 21)
	marcPath := writeNamedSolidFixture(t, "forged.marc", soleStreamBlockSize, map[string][]byte{
		"big.dat": content,
	})

	raw, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	forged := rewriteCatalog(t, raw, `UPDATE blobs SET block_offset = 4096 WHERE block_id IS NOT NULL`)

	forgedPath := filepath.Join(t.TempDir(), "forged.marc")
	if err := os.WriteFile(forgedPath, forged, 0o644); err != nil {
		t.Fatalf("write forged archive: %v", err)
	}

	r, err := OpenReader(forgedPath)
	if err != nil {
		t.Fatalf("OpenReader on the forged archive: %v", err)
	}
	defer func() { _ = r.Close() }()

	ids, err := r.QueryBlobIDs()
	if err != nil {
		t.Fatalf("QueryBlobIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("forged archive holds %d blobs, want 1", len(ids))
	}

	rc, err := r.OpenBlob(ids[0])
	if err == nil {
		_ = rc.Close()
		t.Fatal("OpenBlob accepted a sole-member block at a nonzero block_offset")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("want 0")) {
		t.Fatalf("OpenBlob failed for the wrong reason: %v", err)
	}
}

// TestSoleMemberBlock_matchesCachedPath archives the same file twice, once as
// the sole member of its block and once inside a block it shares, and asserts
// both read paths return identical bytes.
func TestSoleMemberBlock_matchesCachedPath(t *testing.T) {
	content := compressibleBytes(soleStreamBlockSize*3, 31)
	filler := compressibleBytes(8<<10, 32)

	// Small block size: content exceeds it and lands in its own block.
	solePath := writeNamedSolidFixture(t, "as_sole.marc", soleStreamBlockSize, map[string][]byte{
		"big.dat":    content,
		"filler.dat": filler,
	})
	// Large block size: the same bytes now share a block with the filler.
	sharedPath := writeNamedSolidFixture(t, "as_shared.marc", 8<<20, map[string][]byte{
		"big.dat":    content,
		"filler.dat": filler,
	})

	soleBytes := readNamedEntry(t, solePath, "big.dat", true)
	sharedBytes := readNamedEntry(t, sharedPath, "big.dat", false)

	if !bytes.Equal(soleBytes, content) {
		t.Fatalf("streamed path: got %d bytes, want %d", len(soleBytes), len(content))
	}
	if !bytes.Equal(sharedBytes, soleBytes) {
		t.Fatal("streamed and cached read paths disagree on the same content")
	}
}

// readNamedEntry opens archive, reads the blob of entry relPath, and asserts
// whether that blob was the sole member of its block.
func readNamedEntry(t *testing.T, archive, relPath string, wantSole bool) []byte {
	t.Helper()
	r, err := OpenReader(archive)
	if err != nil {
		t.Fatalf("OpenReader %s: %v", archive, err)
	}
	defer func() { _ = r.Close() }()

	var blobID, blockID int64
	if err := r.WalkEntries(func(p string, e EntryRow) error {
		if p == relPath {
			blobID = e.BlobID
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkEntries: %v", err)
	}
	if blobID == 0 {
		t.Fatalf("%s: no blob for %s", archive, relPath)
	}
	if err := r.db.QueryRow(`SELECT COALESCE(block_id, -1) FROM blobs WHERE id = ?`, blobID).Scan(&blockID); err != nil {
		t.Fatalf("lookup block_id: %v", err)
	}
	if got := r.soleBlocks[blockID]; got != wantSole {
		t.Fatalf("%s: blob %d sole-member = %v, want %v", archive, blobID, got, wantSole)
	}
	return readBlob(t, r, blobID)
}

// rewriteCatalog applies update to the catalog of a single-file archive and
// returns a rebuilt archive with a matching footer checksum, so the result is a
// well-formed container carrying a forged catalog.
func rewriteCatalog(t *testing.T, base []byte, update string) []byte {
	t.Helper()

	footer, err := marc.ReadFooter(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		t.Fatalf("read footer: %v", err)
	}
	catalogOff := int64(footer.CatalogOffset)
	payloadLen := int64(binary.BigEndian.Uint32(base[catalogOff+1 : catalogOff+chunkHeaderSize]))
	payloadEnd := catalogOff + chunkHeaderSize + payloadLen

	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecompressedSize))
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	dbBytes, err := dec.DecodeAll(base[catalogOff+chunkHeaderSize:payloadEnd], nil)
	dec.Close()
	if err != nil {
		t.Fatalf("decompress catalog: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	if err := os.WriteFile(dbPath, dbBytes, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	_, err = db.Exec(update)
	if closeErr := db.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("apply %q: %v", update, err)
	}
	dbBytes, err = os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("reread catalog: %v", err)
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	payload := enc.EncodeAll(dbBytes, nil)
	_ = enc.Close()

	var out bytes.Buffer
	out.Write(base[:catalogOff])
	var hdr [chunkHeaderSize]byte
	hdr[0] = marc.ChunkTypeCatalog
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	out.Write(hdr[:])
	out.Write(payload)

	h := blake3.New()
	if _, err := h.Write(out.Bytes()); err != nil {
		t.Fatalf("hash archive: %v", err)
	}
	rebuilt := marc.Footer{
		CatalogOffset:    uint64(catalogOff),
		BlobRegionOffset: footer.BlobRegionOffset,
	}
	copy(rebuilt.Checksum[:], h.Sum(nil))
	if err := rebuilt.WriteFooter(&out); err != nil {
		t.Fatalf("write footer: %v", err)
	}
	return out.Bytes()
}
