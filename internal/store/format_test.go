package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc/pkg/marc"
	"github.com/zeebo/blake3"

	_ "modernc.org/sqlite"
)

func TestMagic_written(t *testing.T) {
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

	e := createTestFile(t, srcDir, "a.txt", []byte("hello"))
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Read first 8 bytes.
	f, err := os.Open(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var magic [8]byte
	if _, err := f.Read(magic[:]); err != nil {
		t.Fatal(err)
	}
	if magic != marc.Magic {
		t.Fatalf("magic mismatch: got %v, want %v", magic, marc.Magic)
	}
}

func TestFooter_valid(t *testing.T) {
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
		content := make([]byte, 100)
		rand.Read(content)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	footer, err := marc.ReadFooter(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}

	if footer.BlobRegionOffset != 8 {
		t.Fatalf("blob_region_offset: got %d, want 8", footer.BlobRegionOffset)
	}
	if footer.CatalogOffset <= 8 {
		t.Fatalf("catalog_offset should be > 8 (after blobs), got %d", footer.CatalogOffset)
	}
	if footer.CatalogOffset >= uint64(info.Size()-marc.FooterSize) {
		t.Fatalf("catalog_offset %d beyond file boundary %d", footer.CatalogOffset, info.Size()-marc.FooterSize)
	}
}

func TestFooter_checksum(t *testing.T) {
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
		content := make([]byte, 100)
		rand.Read(content)
		e := createTestFile(t, srcDir, name, content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	// Footer is last 24 bytes; checksum covers everything before the last 8 bytes (the checksum itself).
	// The file hash covers everything before the footer.
	fileSize := len(data)
	contentBeforeFooter := data[:fileSize-marc.FooterSize]

	h := blake3.New()
	_, _ = h.Write(contentBeforeFooter)
	var expected [8]byte
	sum := h.Sum(nil)
	copy(expected[:], sum[:8])

	var actual [8]byte
	copy(actual[:], data[fileSize-8:])

	if expected != actual {
		t.Fatalf("checksum mismatch: got %x, want %x", actual, expected)
	}
}

func TestCatalogChunk(t *testing.T) {
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
	e := createTestFile(t, srcDir, "hello.txt", []byte("hello world"))
	if err := w.WriteEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	// Read footer to find catalog offset.
	fileSize := int64(len(data))
	f, err := os.Open(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	footer, err := marc.ReadFooter(f, fileSize)
	if err != nil {
		t.Fatal(err)
	}

	// Verify catalog chunk type.
	catOff := footer.CatalogOffset
	if data[catOff] != marc.ChunkTypeCatalog {
		t.Fatalf("catalog chunk type: got 0x%02x, want 0x%02x", data[catOff], marc.ChunkTypeCatalog)
	}

	// Read payload length.
	payloadLen := binary.BigEndian.Uint32(data[catOff+1 : catOff+5])

	// Decompress and open as SQLite.
	compressed := data[catOff+5 : catOff+5+uint64(payloadLen)]
	_ = compressed // Verified by opening the reader and querying

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	// Query entries from the extracted catalog.
	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no entries in catalog")
	}

	// Query meta table.
	var version string
	if err := r.db.QueryRow(`SELECT value FROM meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != marc.Version {
		t.Fatalf("version: got %q, want %q", version, marc.Version)
	}
}

func TestBlobChunk(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("blob chunk test content for verification")

	marcPath := filepath.Join(tmp, "test.marc")
	w, err := OpenWriter(marcPath, WithCompressor("none"))
	if err != nil {
		t.Fatal(err)
	}

	e := createTestFile(t, srcDir, "test.txt", content)
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatal(err)
	}

	// First blob chunk starts right after magic (offset 8).
	if data[8] != marc.ChunkTypeBlob {
		t.Fatalf("blob chunk type: got 0x%02x, want 0x%02x", data[8], marc.ChunkTypeBlob)
	}

	payloadLen := binary.BigEndian.Uint32(data[9:13])
	payload := data[13 : 13+payloadLen]

	// With compressor=none, payload should be the raw content.
	if !bytes.Equal(payload, content) {
		t.Fatalf("blob payload mismatch: got %d bytes, want %d bytes", len(payload), len(content))
	}
}

func TestDetectFormat_singleFile(t *testing.T) {
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
	e := createTestFile(t, srcDir, "a.txt", []byte("hello"))
	if err := w.WriteEntry(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	format, err := marc.DetectFormat(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if format != marc.FormatSingleFile {
		t.Fatalf("expected %q, got %q", marc.FormatSingleFile, format)
	}
}

func TestDetectFormat_splitFile(t *testing.T) {
	tmp := t.TempDir()
	// Create a file that looks like SQLite.
	sqlitePath := filepath.Join(tmp, "legacy.marc")
	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE test (id INTEGER)`)
	_ = db.Close()

	format, err := marc.DetectFormat(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if format != marc.FormatSplitFile {
		t.Fatalf("expected %q, got %q", marc.FormatSplitFile, format)
	}
}
