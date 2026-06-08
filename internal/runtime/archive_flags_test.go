package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// --final-compressor none used to be silently ignored in solid mode (the sink
// routed every blob into the zstd solid accumulator before checking it).
func TestArchive_CompressorNoneBypassesZstd(t *testing.T) {
	src := t.TempDir()
	payload := bytes.Repeat([]byte("metarc-none-marker/"), 512)
	if err := os.WriteFile(filepath.Join(src, "data.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	marcPath := filepath.Join(t.TempDir(), "out.marc")
	ctx := context.Background()
	err := ArchiveWithOpts(ctx, marcPath, src, "none", false, ArchiveOpts{
		SolidBlockSize: DefaultSolidBlockSize,
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	raw, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, payload) {
		t.Fatal("blob bytes must be stored raw under --final-compressor none")
	}
	dest := t.TempDir()
	if err := Extract(ctx, marcPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("roundtrip mismatch under --final-compressor none")
	}
}
