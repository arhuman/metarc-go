package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/zeebo/blake3"

	marcruntime "github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"

	_ "modernc.org/sqlite"
)

// catalogTarget is one catalog cell a fuzz input may rewrite: the column, and
// the row picked for it.
type catalogTarget struct {
	name   string
	update string // UPDATE statement taking the fuzzed value as its only parameter
	asBlob bool   // pass the value as raw bytes rather than as an integer
}

// catalogTargets lists the untrusted catalog fields the read path turns into
// allocations, offsets and decoder bounds. Rewriting exactly one of them keeps
// the archive structurally valid, which is what lets a fuzz input reach the
// reader's own guards instead of dying in zstd or SQLite.
var catalogTargets = []catalogTarget{
	{
		name:   "blobs.clen",
		update: `UPDATE blobs SET clen = ? WHERE id = (SELECT MIN(id) FROM blobs)`,
	},
	{
		name:   "blobs.ulen",
		update: `UPDATE blobs SET ulen = ? WHERE id = (SELECT MIN(id) FROM blobs)`,
	},
	{
		name:   "blobs.offset",
		update: `UPDATE blobs SET offset = ? WHERE id = (SELECT MIN(id) FROM blobs)`,
	},
	{
		name:   "blobs.block_offset",
		update: `UPDATE blobs SET block_offset = ? WHERE id = (SELECT MIN(id) FROM blobs)`,
	},
	{
		name:   "entries.size",
		update: `UPDATE entries SET size = ? WHERE id = (SELECT MIN(id) FROM entries WHERE size IS NOT NULL)`,
	},
	{
		name:   "entries.params",
		update: `UPDATE entries SET params = ? WHERE id = (SELECT MIN(id) FROM entries WHERE transform IS NOT NULL AND transform != '')`,
		asBlob: true,
	},
}

// catalogAllocBudget is the extra heap a single open + verify + extract of a
// tampered archive may allocate. The seed archives are a few kilobytes, so any
// allocation sized from the fuzzed value (up to 2^63) blows straight past it.
const catalogAllocBudget = 128 << 20

// FuzzReader_catalogColumn rewrites one column of one catalog row with a fuzzed
// value, rebuilds a structurally valid container around it, and runs open,
// verify and extract over the result.
//
// This is the semantic counterpart to FuzzReader_corrupt: mutating raw container
// bytes cannot reach a catalog field, because the mutation would have to survive
// zstd decompression and SQLite parsing first. Assertions are that the read path
// neither panics nor allocates in proportion to the value it was handed; a
// mismatch error is the expected outcome.
func FuzzReader_catalogColumn(f *testing.F) {
	plain := buildCatalogSeed(f, 0)
	solid := buildCatalogSeed(f, 512)

	// The boundary values the new guards reject, plus a benign one.
	values := []int64{
		int64(len(plain)) + 1,   // one byte more than the archive can hold
		math.MaxUint32,          // widest length the chunk header can express
		math.MaxInt64,           // overflow bait for offset+length arithmetic
		-1,                      // negative, i.e. a huge unsigned value
		marc.MaxBlobPayload + 1, // one byte over the format's payload invariant
		1 << 40,
		0,
	}
	for col := range catalogTargets {
		for _, v := range values {
			f.Add(false, uint8(col), v)
			f.Add(true, uint8(col), v)
		}
	}

	f.Fuzz(func(t *testing.T, useSolid bool, col uint8, value int64) {
		base := plain
		if useSolid {
			base = solid
		}
		target := catalogTargets[int(col)%len(catalogTargets)]

		data, err := rewriteCatalogCell(base, target, value)
		if err != nil {
			t.Fatalf("rewrite %s: %v", target.name, err)
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "fuzzed.marc")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write archive: %v", err)
		}

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		exerciseArchive(path, filepath.Join(dir, "out"))

		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew > catalogAllocBudget {
			t.Fatalf("%s = %d: allocated %d bytes, budget is %d", target.name, value, grew, catalogAllocBudget)
		}
	})
}

// TestReader_catalogColumnBounds pins the boundary values the fuzz corpus is
// seeded with: each one must be refused by a bound rather than reaching an
// allocation, a slice index or a decoder.
func TestReader_catalogColumnBounds(t *testing.T) {
	plain := buildCatalogSeed(t, 0)
	solid := buildCatalogSeed(t, 512)

	tests := []struct {
		name    string
		solid   bool
		column  string
		value   int64
		wantErr string
	}{
		{name: "clen past end of archive", column: "blobs.clen", value: int64(len(plain)) + 1, wantErr: "exceed the"},
		{name: "clen max uint32", column: "blobs.clen", value: math.MaxUint32, wantErr: "exceed the"},
		{name: "clen max int64", column: "blobs.clen", value: math.MaxInt64, wantErr: "exceed the"},
		{name: "clen negative", column: "blobs.clen", value: -1, wantErr: "negative extent"},
		{name: "offset max int64", column: "blobs.offset", value: math.MaxInt64, wantErr: "chunk offset"},
		{name: "offset negative", column: "blobs.offset", value: -1, wantErr: "negative extent"},
		{name: "ulen negative", column: "blobs.ulen", value: -1, wantErr: "negative extent"},
		{name: "ulen one byte over the payload limit", column: "blobs.ulen", value: marc.MaxBlobPayload + 1, wantErr: "over the 536870912 byte payload limit"},
		{name: "ulen max int64", column: "blobs.ulen", value: math.MaxInt64, wantErr: "payload limit"},
		{name: "solid clen past end", solid: true, column: "blobs.clen", value: int64(len(solid)) + 1, wantErr: "exceed the"},
		{name: "solid ulen max int64", solid: true, column: "blobs.ulen", value: math.MaxInt64, wantErr: "payload limit"},
		{name: "solid block_offset max int64", solid: true, column: "blobs.block_offset", value: math.MaxInt64, wantErr: "out of bounds"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := plain
			if tc.solid {
				base = solid
			}
			target, ok := targetByName(tc.column)
			if !ok {
				t.Fatalf("unknown column %q", tc.column)
			}

			data, err := rewriteCatalogCell(base, target, tc.value)
			if err != nil {
				t.Fatalf("rewrite: %v", err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "tampered.marc")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)

			r, err := store.OpenReader(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = r.Close() }()

			ids, err := r.QueryBlobIDs()
			if err != nil {
				t.Fatalf("blob ids: %v", err)
			}
			if len(ids) == 0 {
				t.Fatal("seed archive has no blobs")
			}

			rc, err := r.OpenBlob(ids[0])
			if err == nil {
				_, err = io.Copy(io.Discard, rc)
				_ = rc.Close()
			}
			if err == nil {
				t.Fatalf("reading blob %d succeeded, want a bound to reject it", ids[0])
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.wantErr)) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}

			runtime.ReadMemStats(&after)
			if grew := after.TotalAlloc - before.TotalAlloc; grew > catalogAllocBudget {
				t.Fatalf("allocated %d bytes, budget is %d", grew, catalogAllocBudget)
			}
		})
	}
}

// TestReader_blobPayloadExceedsUlen checks the other direction: a ulen smaller
// than the bytes actually stored must be reported, never silently truncated.
func TestReader_blobPayloadExceedsUlen(t *testing.T) {
	target, ok := targetByName("blobs.ulen")
	if !ok {
		t.Fatal("missing blobs.ulen target")
	}
	data, err := rewriteCatalogCell(buildCatalogSeed(t, 0), target, 1)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "short-ulen.marc")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = r.Close() }()

	ids, err := r.QueryBlobIDs()
	if err != nil {
		t.Fatalf("blob ids: %v", err)
	}
	rc, err := r.OpenBlob(ids[0])
	if err != nil {
		t.Fatalf("open blob: %v", err)
	}
	defer func() { _ = rc.Close() }()

	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("read succeeded, want the ulen bound to reject the extra bytes")
	} else if !bytes.Contains([]byte(err.Error()), []byte("exceeds the 1 bytes recorded")) {
		t.Fatalf("error %q does not mention the ulen bound", err)
	}
}

func targetByName(name string) (catalogTarget, bool) {
	for _, t := range catalogTargets {
		if t.name == name {
			return t, true
		}
	}
	return catalogTarget{}, false
}

// exerciseArchive runs the three untrusted-input entry points over path. Every
// error is expected: the harness asserts on panics and allocation, not outcome.
func exerciseArchive(path, destDir string) {
	ctx := context.Background()

	if r, err := store.OpenReader(path); err == nil {
		_ = r.WalkEntries(func(_ string, e store.EntryRow) error {
			if e.BlobID == 0 {
				return nil
			}
			rc, err := r.OpenBlob(e.BlobID)
			if err != nil {
				return nil
			}
			_, _ = io.Copy(io.Discard, rc)
			_ = rc.Close()
			return nil
		})
		_ = r.Close()
	}

	_, _ = marcruntime.Verify(ctx, path)
	_ = marcruntime.Extract(ctx, path, destDir)
}

// buildCatalogSeed writes one small valid archive and returns its bytes.
// solidBlockSize of 0 disables solid blocks, so callers can seed both the
// standalone-blob read path and the solid-block one.
func buildCatalogSeed(tb testing.TB, solidBlockSize int64) []byte {
	tb.Helper()

	tmp := tb.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		tb.Fatal(err)
	}
	files := map[string]string{
		"a.txt":       "hello catalog fuzz\n",
		"dup.txt":     "hello catalog fuzz\n", // dedups onto a.txt
		"sub/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "seed.marc")
	opts := marcruntime.ArchiveOpts{SolidBlockSize: solidBlockSize}
	if err := marcruntime.ArchiveWithOpts(context.Background(), marcPath, src, "zstd", false, opts); err != nil {
		tb.Fatal(err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// rewriteCatalogCell decompresses the catalog of base, applies target's update
// with value, then reassembles a container whose magic, blob region, chunk
// header and footer checksum are all consistent. The returned archive therefore
// differs from base in exactly one catalog cell.
func rewriteCatalogCell(base []byte, target catalogTarget, value int64) ([]byte, error) {
	if len(base) < marc.FooterSize+len(marc.Magic) {
		return nil, fmt.Errorf("seed archive too small: %d bytes", len(base))
	}
	footer, err := marc.ReadFooter(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		return nil, fmt.Errorf("read footer: %w", err)
	}
	catalogOff := int64(footer.CatalogOffset)
	if catalogOff < 0 || catalogOff+chunkHeaderLen > int64(len(base)) {
		return nil, fmt.Errorf("catalog offset %d outside seed archive", catalogOff)
	}
	payloadLen := int64(binary.BigEndian.Uint32(base[catalogOff+1 : catalogOff+chunkHeaderLen]))
	payloadEnd := catalogOff + chunkHeaderLen + payloadLen
	if payloadEnd > int64(len(base)) {
		return nil, fmt.Errorf("catalog payload of %d bytes outside seed archive", payloadLen)
	}

	dbBytes, err := zstdDecodeAll(base[catalogOff+chunkHeaderLen : payloadEnd])
	if err != nil {
		return nil, fmt.Errorf("decompress catalog: %w", err)
	}

	dbBytes, err = applyCatalogUpdate(dbBytes, target, value)
	if err != nil {
		return nil, err
	}

	payload, err := zstdEncodeAll(dbBytes)
	if err != nil {
		return nil, fmt.Errorf("recompress catalog: %w", err)
	}

	var out bytes.Buffer
	out.Write(base[:catalogOff])
	var hdr [chunkHeaderLen]byte
	hdr[0] = marc.ChunkTypeCatalog
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	out.Write(hdr[:])
	out.Write(payload)

	h := blake3.New()
	if _, err := h.Write(out.Bytes()); err != nil {
		return nil, fmt.Errorf("hash archive: %w", err)
	}
	rebuilt := marc.Footer{
		CatalogOffset:    uint64(catalogOff),
		BlobRegionOffset: footer.BlobRegionOffset,
	}
	copy(rebuilt.Checksum[:], h.Sum(nil))
	if err := rebuilt.WriteFooter(&out); err != nil {
		return nil, fmt.Errorf("write footer: %w", err)
	}
	return out.Bytes(), nil
}

// chunkHeaderLen mirrors the [Type 1B][Len 4B] chunk prefix of the format.
const chunkHeaderLen = 5

// applyCatalogUpdate runs target's UPDATE against the serialized catalog and
// returns the modified SQLite file.
func applyCatalogUpdate(dbBytes []byte, target catalogTarget, value int64) ([]byte, error) {
	dir, err := os.MkdirTemp("", "marc-catalog-rewrite-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	dbPath := filepath.Join(dir, "catalog.db")
	if err := os.WriteFile(dbPath, dbBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write catalog: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open catalog: %w", err)
	}
	var arg any = value
	if target.asBlob {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(value))
		arg = buf[:]
	}
	_, err = db.Exec(target.update, arg)
	if closeErr := db.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("update %s: %w", target.name, err)
	}

	return os.ReadFile(dbPath)
}

func zstdDecodeAll(payload []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(512<<20))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(payload, nil)
}

func zstdEncodeAll(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(data, nil), nil
}
