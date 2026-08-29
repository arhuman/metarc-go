package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	marcruntime "github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"

	_ "modernc.org/sqlite"
)

// buildArchive writes a small archive and returns its path. solidBlockSize of 0
// disables solid blocks, so callers can pick the blob layout they need.
func buildArchive(t *testing.T, solidBlockSize int64) string {
	t.Helper()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"a.txt":       "hello reader errors\n",
		"dup.txt":     "hello reader errors\n",
		"sub/main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "seed.marc")
	opts := marcruntime.ArchiveOpts{SolidBlockSize: solidBlockSize}
	if err := marcruntime.ArchiveWithOpts(context.Background(), marcPath, src, "zstd", true, opts); err != nil {
		t.Fatalf("archive: %v", err)
	}
	return marcPath
}

// writeTemp writes data to a fresh file under t.TempDir and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestOpenReader_containerRejected covers the container-level guards in
// openReaderSingle: the checks that run before any catalog cell is trusted.
//
// These are distinct from the catalog-tampering cases in fuzz_catalog_test.go:
// there the container stays valid and a catalog value is forged, here the
// container itself is malformed and the reader must refuse it up front.
func TestOpenReader_containerRejected(t *testing.T) {
	valid, err := os.ReadFile(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	footer, err := marc.ReadFooter(bytes.NewReader(valid), int64(len(valid)))
	if err != nil {
		t.Fatalf("read seed footer: %v", err)
	}
	catalogOff := int64(footer.CatalogOffset)

	// setFooterCatalogOffset rewrites the catalog offset the footer advertises,
	// leaving every other byte intact.
	setFooterCatalogOffset := func(base []byte, off uint64) []byte {
		out := append([]byte(nil), base...)
		binary.BigEndian.PutUint64(out[len(out)-marc.FooterSize:len(out)-marc.FooterSize+8], off)
		return out
	}

	tests := []struct {
		name    string
		data    []byte
		wantMsg string
	}{
		{
			name:    "bad magic",
			data:    append([]byte("NOTMARC!"), valid[8:]...),
			wantMsg: "unrecognized file header", // DetectFormat rejects before OpenReader
		},
		{
			name:    "truncated below footer size",
			data:    valid[:marc.FooterSize],
			wantMsg: "too small",
		},
		{
			name:    "catalog offset past end of file",
			data:    setFooterCatalogOffset(valid, uint64(len(valid))+1<<20),
			wantMsg: "catalog",
		},
		{
			name:    "catalog offset inside the footer",
			data:    setFooterCatalogOffset(valid, uint64(len(valid)-marc.FooterSize+1)),
			wantMsg: "catalog",
		},
		{
			name: "catalog chunk type is not 0x02",
			data: func() []byte {
				out := append([]byte(nil), valid...)
				out[catalogOff] = marc.ChunkTypeCatalog ^ 0xFF
				return out
			}(),
			wantMsg: "catalog chunk type",
		},
		{
			name: "catalog length exceeds remaining bytes",
			data: func() []byte {
				out := append([]byte(nil), valid...)
				binary.BigEndian.PutUint32(out[catalogOff+1:catalogOff+5], ^uint32(0))
				return out
			}(),
			wantMsg: "catalog chunk",
		},
		{
			name: "catalog payload is not valid zstd",
			data: func() []byte {
				out := append([]byte(nil), valid...)
				// Keep the declared length, destroy the frame it points at.
				for i := catalogOff + 5; i < int64(len(out))-marc.FooterSize && i < catalogOff+64; i++ {
					out[i] = 0xFF
				}
				return out
			}(),
			wantMsg: "catalog",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, "bad.marc", tc.data)

			r, err := store.OpenReader(path)
			if err == nil {
				_ = r.Close()
				t.Fatalf("OpenReader accepted a malformed archive")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestOpenReader_missingAndUnreadable covers the open/stat failures at the top
// of the read path.
func TestOpenReader_missingAndUnreadable(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if r, err := store.OpenReader(filepath.Join(t.TempDir(), "absent.marc")); err == nil {
			_ = r.Close()
			t.Fatal("expected an error for a missing archive")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		if r, err := store.OpenReader(writeTemp(t, "empty.marc", nil)); err == nil {
			_ = r.Close()
			t.Fatal("expected an error for an empty archive")
		}
	})

	t.Run("shorter than the magic", func(t *testing.T) {
		if r, err := store.OpenReader(writeTemp(t, "tiny.marc", []byte("MET"))); err == nil {
			_ = r.Close()
			t.Fatal("expected an error for a 3-byte archive")
		}
	})
}

// TestOpenReader_splitFile covers openReaderSplit, the legacy backward-compat
// path: no writer produces split-file archives anymore, so the catalog is
// extracted from a single-file archive and paired with a .blobs sidecar.
//
// The blob offsets in that catalog address the single-file layout, where an
// offset points at a chunk header rather than at payload bytes. The sidecar is
// therefore built as a byte-identical copy of the archive, which keeps every
// recorded offset valid while exercising the split-file branch.
func TestOpenReader_splitFile(t *testing.T) {
	archivePath := buildArchive(t, 0)
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	catalogDB := extractCatalogDB(t, raw)

	dir := t.TempDir()
	splitPath := filepath.Join(dir, "legacy.marc")
	if err := os.WriteFile(splitPath, catalogDB, 0o644); err != nil {
		t.Fatalf("write split catalog: %v", err)
	}
	if err := os.WriteFile(splitPath+marc.BlobsExt, raw, 0o644); err != nil {
		t.Fatalf("write blobs sidecar: %v", err)
	}

	// DetectFormat must classify a bare SQLite file as the legacy format.
	format, err := marc.DetectFormat(splitPath)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != marc.FormatSplitFile {
		t.Fatalf("DetectFormat = %q, want %q", format, marc.FormatSplitFile)
	}

	r, err := store.OpenReader(splitPath)
	if err != nil {
		t.Fatalf("OpenReader on a split-file archive: %v", err)
	}
	defer func() { _ = r.Close() }()

	ov, err := r.QueryOverview()
	if err != nil {
		t.Fatalf("QueryOverview: %v", err)
	}
	if ov.EntryCount == 0 || ov.BlobCount == 0 {
		t.Errorf("split-file overview looks empty: %+v", ov)
	}

	// A legacy archive carries no footer, so VerifyFooter must report
	// "not checked" rather than failing.
	checked, ok, err := r.VerifyFooter()
	if err != nil {
		t.Fatalf("VerifyFooter: %v", err)
	}
	if checked {
		t.Errorf("VerifyFooter: checked = true for a split-file archive, want false")
	}
	if ok {
		t.Errorf("VerifyFooter: ok = true when nothing was checked")
	}

	t.Run("missing blobs sidecar", func(t *testing.T) {
		lonely := filepath.Join(t.TempDir(), "lonely.marc")
		if err := os.WriteFile(lonely, catalogDB, 0o644); err != nil {
			t.Fatalf("write catalog: %v", err)
		}
		if r, err := store.OpenReader(lonely); err == nil {
			_ = r.Close()
			t.Error("expected an error when the .blobs sidecar is absent")
		}
	})
}

// extractCatalogDB pulls the decompressed SQLite catalog out of a single-file
// archive, so tests can rebuild other container shapes around it.
func extractCatalogDB(t *testing.T, archive []byte) []byte {
	t.Helper()

	footer, err := marc.ReadFooter(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("read footer: %v", err)
	}
	off := int64(footer.CatalogOffset)
	if archive[off] != marc.ChunkTypeCatalog {
		t.Fatalf("chunk at %d is type 0x%02x, want catalog", off, archive[off])
	}
	payloadLen := int64(binary.BigEndian.Uint32(archive[off+1 : off+5]))

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer dec.Close()

	db, err := dec.DecodeAll(archive[off+5:off+5+payloadLen], nil)
	if err != nil {
		t.Fatalf("decompress catalog: %v", err)
	}
	return db
}

// TestVerifyFooter_detectsCorruption covers the footer-hash comparison on the
// single-file path: an intact archive verifies, and a flipped byte in the blob
// region is reported as a mismatch rather than an error.
func TestVerifyFooter_detectsCorruption(t *testing.T) {
	raw, err := os.ReadFile(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	t.Run("intact archive verifies", func(t *testing.T) {
		r, err := store.OpenReader(writeTemp(t, "good.marc", raw))
		if err != nil {
			t.Fatalf("OpenReader: %v", err)
		}
		defer func() { _ = r.Close() }()

		checked, ok, err := r.VerifyFooter()
		if err != nil {
			t.Fatalf("VerifyFooter: %v", err)
		}
		if !checked || !ok {
			t.Errorf("VerifyFooter = (checked %v, ok %v), want (true, true)", checked, ok)
		}
	})

	t.Run("flipped byte is a mismatch", func(t *testing.T) {
		bad := append([]byte(nil), raw...)
		bad[len(marc.Magic)+2] ^= 0xFF

		r, err := store.OpenReader(writeTemp(t, "bad.marc", bad))
		if err != nil {
			t.Fatalf("OpenReader: %v", err)
		}
		defer func() { _ = r.Close() }()

		checked, ok, err := r.VerifyFooter()
		if err != nil {
			t.Fatalf("VerifyFooter: %v", err)
		}
		if !checked {
			t.Error("VerifyFooter: checked = false on a single-file archive")
		}
		if ok {
			t.Error("VerifyFooter: ok = true despite a corrupted blob region")
		}
	})
}

// TestQueryMeta covers the meta-table accessor, including the absent-key path
// that older archives rely on.
func TestQueryMeta(t *testing.T) {
	r, err := store.OpenReader(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	t.Run("present key", func(t *testing.T) {
		v, ok, err := r.QueryMeta("zstd_level_blob")
		if err != nil {
			t.Fatalf("QueryMeta: %v", err)
		}
		if !ok || v == "" {
			t.Errorf("zstd_level_blob: got (%q, %v), want a recorded value", v, ok)
		}
	})

	t.Run("absent key is not an error", func(t *testing.T) {
		v, ok, err := r.QueryMeta("no-such-key-exists")
		if err != nil {
			t.Fatalf("QueryMeta on an absent key should not error: %v", err)
		}
		if ok {
			t.Errorf("absent key reported ok = true (value %q)", v)
		}
	})
}

// TestOpenBlob_rejectsUnknownID checks a blob id with no catalog row is an
// error rather than a zero-value read.
func TestOpenBlob_rejectsUnknownID(t *testing.T) {
	r, err := store.OpenReader(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	for _, id := range []int64{-1, 0, 1 << 40} {
		if rc, err := r.OpenBlob(id); err == nil {
			_ = rc.Close()
			t.Errorf("OpenBlob(%d) succeeded on a nonexistent blob", id)
		}
	}
}

// TestVerifyBlob_roundTrip covers VerifyBlob over every blob in an intact
// archive, plus the unknown-id path.
func TestVerifyBlob_roundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		solid int64
	}{
		{"per-blob compression", 0},
		{"solid blocks", 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := store.OpenReader(buildArchive(t, tc.solid))
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer func() { _ = r.Close() }()

			ids, err := r.QueryBlobIDs()
			if err != nil {
				t.Fatalf("QueryBlobIDs: %v", err)
			}
			if len(ids) == 0 {
				t.Fatal("archive has no blobs")
			}
			for _, id := range ids {
				if err := r.VerifyBlob(id); err != nil {
					t.Errorf("VerifyBlob(%d) on an intact archive: %v", id, err)
				}
			}

			if err := r.VerifyBlob(1 << 40); err == nil {
				t.Error("VerifyBlob on a nonexistent blob should fail")
			}
		})
	}
}

// TestVerifyBlob_rejectsShortStoredHash pins the hash-width floor. The
// comparison is prefix-based, so a catalog hash narrower than StoredSHALen
// would otherwise "verify" against an arbitrary blob on a short prefix: one
// byte matches roughly 1 time in 256. Such a catalog must be rejected instead.
func TestVerifyBlob_rejectsShortStoredHash(t *testing.T) {
	raw, err := os.ReadFile(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	dir := t.TempDir()
	splitPath := filepath.Join(dir, "short.marc")
	if err := os.WriteFile(splitPath, extractCatalogDB(t, raw), 0o644); err != nil {
		t.Fatalf("write split catalog: %v", err)
	}
	if err := os.WriteFile(splitPath+marc.BlobsExt, raw, 0o644); err != nil {
		t.Fatalf("write blobs sidecar: %v", err)
	}

	// Truncate every stored hash to one byte, below the StoredSHALen floor.
	db, err := sql.Open("sqlite", splitPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	if _, err := db.Exec(`UPDATE blobs SET sha = substr(sha, 1, 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("truncate stored hashes: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close catalog: %v", err)
	}

	r, err := store.OpenReader(splitPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	ids, err := r.QueryBlobIDs()
	if err != nil {
		t.Fatalf("QueryBlobIDs: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("archive has no blobs")
	}
	for _, id := range ids {
		err := r.VerifyBlob(id)
		if err == nil {
			t.Fatalf("VerifyBlob(%d) accepted a 1-byte catalog hash", id)
		}
		if !strings.Contains(err.Error(), "want at least") {
			t.Fatalf("VerifyBlob(%d) failed for the wrong reason: %v", id, err)
		}
	}
}

// TestOpenBlob_readsPayload checks the happy path of both blob layouts: the
// bytes read back through OpenBlob match what was archived.
func TestOpenBlob_readsPayload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		solid int64
	}{
		{"per-blob compression", 0},
		{"solid blocks", 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := store.OpenReader(buildArchive(t, tc.solid))
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer func() { _ = r.Close() }()

			ids, err := r.QueryBlobIDs()
			if err != nil {
				t.Fatalf("QueryBlobIDs: %v", err)
			}

			var seen []string
			for _, id := range ids {
				rc, err := r.OpenBlob(id)
				if err != nil {
					t.Fatalf("OpenBlob(%d): %v", id, err)
				}
				data, err := io.ReadAll(rc)
				_ = rc.Close()
				if err != nil {
					t.Fatalf("read blob %d: %v", id, err)
				}
				seen = append(seen, string(data))
			}

			joined := strings.Join(seen, "\n")
			if !strings.Contains(joined, "hello reader errors") {
				t.Errorf("archived content missing from blob payloads: %q", joined)
			}
		})
	}
}

// TestReader_CloseIsIdempotent checks a second Close does not panic, which
// matters because callers routinely defer Close after an error path already
// closed the reader.
func TestReader_CloseIsIdempotent(t *testing.T) {
	r, err := store.OpenReader(buildArchive(t, 0))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// The second Close reports an error from the already-closed handles; the
	// contract under test is only that it returns instead of panicking.
	_ = r.Close()
}
