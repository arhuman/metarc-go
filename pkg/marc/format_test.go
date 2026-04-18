package marc

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestReadFooter_fileTooSmall(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{"zero bytes", 0},
		{"one byte", 1},
		{"just under minimum", int64(FooterSize) + int64(len(Magic)) - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A bytes.Reader of any content will do; size drives the check.
			r := bytes.NewReader(make([]byte, tt.size))
			_, err := ReadFooter(r, tt.size)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "too small") {
				t.Fatalf("expected 'too small' in error, got: %v", err)
			}
		})
	}
}

func TestWriteFooter_ReadFooter_roundtrip(t *testing.T) {
	want := Footer{
		CatalogOffset:    0xDEADBEEF_CAFEBABE,
		BlobRegionOffset: 8,
		Checksum:         [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	}

	// Build a buffer: enough padding so size >= FooterSize + len(Magic), then the footer bytes.
	padding := make([]byte, len(Magic)) // exactly 8 bytes of padding
	var buf bytes.Buffer
	buf.Write(padding)
	if err := want.WriteFooter(&buf); err != nil {
		t.Fatalf("WriteFooter: %v", err)
	}

	data := buf.Bytes()
	r := bytes.NewReader(data)
	got, err := ReadFooter(r, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadFooter: %v", err)
	}

	if got.CatalogOffset != want.CatalogOffset {
		t.Errorf("CatalogOffset: got %d, want %d", got.CatalogOffset, want.CatalogOffset)
	}
	if got.BlobRegionOffset != want.BlobRegionOffset {
		t.Errorf("BlobRegionOffset: got %d, want %d", got.BlobRegionOffset, want.BlobRegionOffset)
	}
	if got.Checksum != want.Checksum {
		t.Errorf("Checksum: got %x, want %x", got.Checksum, want.Checksum)
	}
}

func TestReadFooter_exactMinimumSize(t *testing.T) {
	// Exactly FooterSize + len(Magic) bytes — should succeed.
	f := Footer{
		CatalogOffset:    42,
		BlobRegionOffset: 8,
		Checksum:         [8]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22},
	}

	padding := make([]byte, len(Magic))
	var buf bytes.Buffer
	buf.Write(padding)
	if err := f.WriteFooter(&buf); err != nil {
		t.Fatalf("WriteFooter: %v", err)
	}

	data := buf.Bytes()
	if len(data) != FooterSize+len(Magic) {
		t.Fatalf("setup: expected %d bytes, got %d", FooterSize+len(Magic), len(data))
	}

	r := bytes.NewReader(data)
	got, err := ReadFooter(r, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadFooter on minimum-size buffer: %v", err)
	}
	if got.CatalogOffset != f.CatalogOffset {
		t.Errorf("CatalogOffset: got %d, want %d", got.CatalogOffset, f.CatalogOffset)
	}
}

func TestDetectFormat_singleFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/test.marc"

	// Write magic bytes followed by dummy content.
	content := make([]byte, 32)
	copy(content[:8], Magic[:])
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatSingleFile {
		t.Errorf("format: got %q, want %q", format, FormatSingleFile)
	}
}

func TestDetectFormat_splitFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/legacy.marc"

	// Write a valid SQLite header ("SQLite format 3\000" = 16 bytes).
	header := "SQLite format 3\x00"
	content := make([]byte, 32)
	copy(content, header)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatSplitFile {
		t.Errorf("format: got %q, want %q", format, FormatSplitFile)
	}
}

func TestDetectFormat_unknownHeader(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/unknown.bin"

	// Random bytes that match neither magic.
	content := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := DetectFormat(path)
	if err == nil {
		t.Fatal("expected error for unknown header, got nil")
	}
}
