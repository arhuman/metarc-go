package runtime_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
)

// TestBench_runs verifies the bench workflow: archive with plan log kept,
// then read back transform stats.
func TestBench_runs(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a few files.
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), bytes.Repeat([]byte("bench "), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("unique content"), 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "bench.marc")
	ctx := context.Background()

	// Archive with plan log kept (as bench does).
	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", true, 2); err != nil {
		t.Fatal(err)
	}

	// Verify archive exists.
	info, err := os.Stat(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("archive is empty")
	}

	// Verify plan_log is present and queryable.
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	stats, err := r.QueryPlanLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) == 0 {
		t.Fatal("expected at least one plan_log stat row")
	}
	t.Logf("plan_log stats: %+v", stats)

	// Verify round-trip.
	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}
}
