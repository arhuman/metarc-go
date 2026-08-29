package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
)

// TestRoundTrip_multipleSolidBlocks archives a tree that spans many solid
// blocks and extracts it, so the parallel compression pool is exercised
// end to end: blocks are compressed out of order but must be written, indexed
// and read back in order.
func TestRoundTrip_multipleSolidBlocks(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A block size well below the total keeps several blocks in flight at once,
	// which is what the reorder stage has to survive.
	const blockSize = 64 * 1024
	want := make(map[string][]byte)

	for _, ext := range []string{".txt", ".go", ".md"} {
		sub := filepath.Join(srcDir, "d"+ext[1:])
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range 30 {
			name := fmt.Sprintf("f%02d%s", i, ext)
			content := bytes.Repeat([]byte(fmt.Sprintf("solid parallel %s %d\n", ext, i)), 200)
			path := filepath.Join(sub, name)
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(srcDir, path)
			if err != nil {
				t.Fatal(err)
			}
			want[rel] = content
		}
	}

	marcPath := filepath.Join(tmp, "parallel.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.ArchiveWithOpts(ctx, marcPath, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: blockSize}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(restoreDir, rel))
		if err != nil {
			t.Fatalf("read restored %s: %v", rel, err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("%s: content mismatch after round-trip (%d bytes restored, %d archived)",
				rel, len(got), len(content))
		}
	}
}
