//go:build !windows

package runtime_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/arhuman/metarc/internal/runtime"
)

// checkOwnership compares uid/gid between two FileInfo values.
// Called from compareDirectories in roundtrip_test.go.
func checkOwnership(t *testing.T, rel string, origInfo, restoredInfo os.FileInfo) {
	t.Helper()
	origStat, ok1 := origInfo.Sys().(*syscall.Stat_t)
	restoredStat, ok2 := restoredInfo.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return
	}
	if origStat.Uid != restoredStat.Uid {
		t.Errorf("uid mismatch: %s (orig=%d, restored=%d)",
			rel, origStat.Uid, restoredStat.Uid)
	}
	if origStat.Gid != restoredStat.Gid {
		// Non-root users cannot restore arbitrary GID; log instead of fail.
		t.Logf("gid mismatch (non-root): %s (orig=%d, restored=%d)",
			rel, origStat.Gid, restoredStat.Gid)
	}
}

// TestRoundTrip_ownership verifies that uid, gid, and permission bits are
// stored and restored correctly using files owned by the current user.
func TestRoundTrip_ownership(t *testing.T) {
	src := t.TempDir()

	files := []struct {
		name string
		perm fs.FileMode
	}{
		{"exec.sh", 0o755},
		{"readonly.txt", 0o444},
		{"private.key", 0o600},
	}
	for _, f := range files {
		path := filepath.Join(src, f.name)
		if err := os.WriteFile(path, []byte("content of "+f.name), f.perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, f.perm); err != nil {
			t.Fatal(err)
		}
	}

	tmpDir := t.TempDir()
	marcPath := filepath.Join(tmpDir, "own.marc")
	restoreDir := filepath.Join(tmpDir, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, src, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	for _, f := range files {
		orig := filepath.Join(src, f.name)
		restored := filepath.Join(restoreDir, f.name)

		origInfo, err := os.Stat(orig)
		if err != nil {
			t.Fatalf("stat orig %s: %v", f.name, err)
		}
		restoredInfo, err := os.Stat(restored)
		if err != nil {
			t.Fatalf("stat restored %s: %v", f.name, err)
		}

		if origInfo.Mode().Perm() != restoredInfo.Mode().Perm() {
			t.Errorf("perm mismatch %s: orig=%v restored=%v",
				f.name, origInfo.Mode().Perm(), restoredInfo.Mode().Perm())
		}

		origStat, ok1 := origInfo.Sys().(*syscall.Stat_t)
		restoredStat, ok2 := restoredInfo.Sys().(*syscall.Stat_t)
		if ok1 && ok2 {
			if origStat.Uid != restoredStat.Uid {
				t.Errorf("uid mismatch %s: orig=%d restored=%d", f.name, origStat.Uid, restoredStat.Uid)
			}
			if origStat.Gid != restoredStat.Gid {
				t.Errorf("gid mismatch %s: orig=%d restored=%d", f.name, origStat.Gid, restoredStat.Gid)
			}
		}
	}
}
