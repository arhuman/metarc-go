package runtime_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
)

// TestPassthrough_e2e_incompressibleRoundTrip archives random-bytes files
// with allowlisted extensions and verifies they round-trip byte-identical.
// This is the correctness guard.
//
// Lives in internal/runtime/ (not in the passthrough package) because
// passthrough is registered in internal/plan, which is imported by
// runtime, so a passthrough-package test can't import runtime without
// an import cycle.
func TestPassthrough_e2e_incompressibleRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const fileSize = 64 * 1024
	files := []string{"a.png", "b.jpg", "c.zip"}
	for _, name := range files {
		buf := make([]byte, fileSize)
		if _, err := rand.Read(buf); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, name), buf, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	marcPath := filepath.Join(tmp, "out.marc")
	ctx := context.Background()
	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	extractDir := filepath.Join(tmp, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir extract: %v", err)
	}
	if err := runtime.Extract(ctx, marcPath, extractDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	for _, name := range files {
		orig, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatalf("read original %s: %v", name, err)
		}
		extracted, err := os.ReadFile(filepath.Join(extractDir, name))
		if err != nil {
			t.Fatalf("read extracted %s: %v", name, err)
		}
		if !bytes.Equal(orig, extracted) {
			t.Fatalf("round-trip mismatch for %s: %d vs %d bytes", name, len(orig), len(extracted))
		}
	}
}

// TestPassthrough_e2e_didNotCompress verifies that passthrough actually
// fires by writing files of *highly compressible* data (all zeros) but
// with passthrough-allowlisted extensions. With passthrough firing, the
// archive stays close to raw size. If passthrough silently stops firing,
// zstd would crush the zeros to almost nothing and the archive would be
// orders of magnitude smaller than the raw input — that's the signal we
// catch here. This is the "ratio non-regression" test from the roadmap.
func TestPassthrough_e2e_didNotCompress(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const fileSize = 64 * 1024
	files := []string{"compressible.zip", "compressible.png", "compressible.mp4"}
	// Each file gets a distinct constant-byte payload — highly
	// compressible (zstd would crush it to ~80 bytes per file) but with
	// distinct content so dedup doesn't merge the three blobs into one.
	totalRaw := int64(0)
	for i, name := range files {
		buf := bytes.Repeat([]byte{byte('A' + i)}, fileSize)
		if err := os.WriteFile(filepath.Join(srcDir, name), buf, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		totalRaw += int64(fileSize)
	}

	marcPath := filepath.Join(tmp, "out.marc")
	ctx := context.Background()
	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	stat, err := os.Stat(marcPath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}

	// With passthrough firing on these allowlisted extensions, the zeros
	// are stored raw — archive size ≈ raw size + small catalog overhead.
	// Without passthrough, every file is also de-duplicated (all 3 zeros
	// blobs share content) AND zstd-compressed, collapsing the archive to
	// a few KB. The threshold is 50% of raw size: passthrough should keep
	// the archive well above this; a regression that lets zstd compress
	// these files would put the archive far below.
	if stat.Size() < totalRaw/2 {
		t.Fatalf("archive size %d is much smaller than raw %d (ratio %.1f%%) — "+
			"passthrough may not be firing (zstd was allowed to compress allowlisted files)",
			stat.Size(), totalRaw, 100*float64(stat.Size())/float64(totalRaw))
	}
}
