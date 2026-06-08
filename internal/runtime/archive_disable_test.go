package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dedup/v1 cannot really be disabled (blobs.sha is UNIQUE); the flag used to be
// silently ignored, which invalidated the ablation study's marc_no_dedup variant.
func TestArchive_DisableDedupRejected(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	marcPath := filepath.Join(t.TempDir(), "out.marc")
	err := ArchiveWithOpts(context.Background(), marcPath, src, "zstd", false, ArchiveOpts{
		SolidBlockSize:     DefaultSolidBlockSize,
		DisabledTransforms: []string{"dedup/v1"},
	})
	if err == nil || !strings.Contains(err.Error(), "dedup/v1") {
		t.Fatalf("expected explicit dedup/v1 rejection, got err=%v", err)
	}
}
