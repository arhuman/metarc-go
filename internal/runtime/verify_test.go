package runtime_test

// TODO: uncomment and validate — added by an agent on 2026-08-17 with the new
// `marc verify` verb. The assertions define what verify reports on an intact
// archive and on a corrupted one; the repository owner should confirm them.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
)

// TestVerify_intactArchive checks that a freshly written archive passes both the
// footer checksum and every per-blob hash.
func TestVerify_intactArchive(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeTree(t, src, map[string]string{
		"a.txt":         "alpha",
		"sub/b.txt":     "beta",
		"sub/c/d.txt":   "delta",
		"duplicate.txt": "alpha",
	})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.Archive(ctx, marcPath, src, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	rep, err := runtime.Verify(ctx, marcPath)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("intact archive reported mismatches: %v", rep.Mismatches)
	}
	if !rep.FooterChecked || !rep.FooterOK {
		t.Fatalf("footer not verified: checked=%v ok=%v", rep.FooterChecked, rep.FooterOK)
	}
	if rep.BlobsChecked == 0 {
		t.Fatal("no blob was verified")
	}
}

// TestVerify_corruptBlob flips one byte of a verbatim blob payload: the blob
// hash and the footer checksum must both be reported as mismatched.
func TestVerify_corruptBlob(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	const marker = "VERIFY-ME-0123456789-VERIFY-ME"
	writeTree(t, src, map[string]string{"payload.txt": marker})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.Archive(ctx, marcPath, src, "none", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	flipByteInArchive(t, marcPath, marker)

	rep, err := runtime.Verify(ctx, marcPath)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("verify reported ok on a corrupted archive")
	}
	if rep.FooterOK {
		t.Fatal("footer checksum matched a corrupted archive")
	}
	joined := strings.Join(rep.Mismatches, "\n")
	if !strings.Contains(joined, "hash mismatch") {
		t.Fatalf("no blob hash mismatch reported: %v", rep.Mismatches)
	}
}

// TestVerify_truncatedFooter checks that a footer edit alone is caught, with
// every blob still intact.
func TestVerify_truncatedFooter(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeTree(t, src, map[string]string{"a.txt": "alpha"})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.Archive(ctx, marcPath, src, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	data, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte of the stored checksum (the last 8 bytes of the footer).
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(marcPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := runtime.Verify(ctx, marcPath)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.FooterOK {
		t.Fatal("footer checksum matched after the stored value was edited")
	}
	if rep.OK() {
		t.Fatal("verify reported ok after the footer was edited")
	}
	if len(rep.Mismatches) != 1 {
		t.Fatalf("expected only the footer to fail, got %v", rep.Mismatches)
	}
}
