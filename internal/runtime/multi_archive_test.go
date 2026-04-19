package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
)

// TestArchiveMulti_roundtrip creates an archive from two independent source
// directories and verifies that extracting it restores both trees side by
// side under the destination.
func TestArchiveMulti_roundtrip(t *testing.T) {
	tmp := t.TempDir()

	alphaDir := filepath.Join(tmp, "alpha")
	betaDir := filepath.Join(tmp, "beta")

	if err := os.MkdirAll(filepath.Join(alphaDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(betaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		filepath.Join(alphaDir, "a.txt"):        []byte("alpha root file"),
		filepath.Join(alphaDir, "sub", "c.txt"): []byte("nested in alpha"),
		filepath.Join(betaDir, "b.txt"):         []byte("beta root file"),
		filepath.Join(betaDir, "shared.txt"):    []byte("shared content shared content"),
	}
	// Also include an identical copy in alpha to exercise dedup across roots.
	files[filepath.Join(alphaDir, "shared.txt")] = []byte("shared content shared content")

	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "multi.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.ArchiveMulti(ctx, marcPath, []string{alphaDir, betaDir}, "zstd", false); err != nil {
		t.Fatalf("ArchiveMulti: %v", err)
	}

	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	compareDirectories(t, alphaDir, filepath.Join(restoreDir, "alpha"))
	compareDirectories(t, betaDir, filepath.Join(restoreDir, "beta"))
}

// TestArchiveMulti_basenameCollisionRejected verifies that passing two
// sources that share the same basename returns an error before opening the
// output archive.
func TestArchiveMulti_basenameCollisionRejected(t *testing.T) {
	tmp := t.TempDir()

	a := filepath.Join(tmp, "x", "shared")
	b := filepath.Join(tmp, "y", "shared")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "multi.marc")
	ctx := context.Background()

	err := runtime.ArchiveMulti(ctx, marcPath, []string{a, b}, "zstd", false)
	if err == nil {
		t.Fatal("expected basename collision error, got nil")
	}
	if !strings.Contains(err.Error(), "basename collision") {
		t.Errorf("expected 'basename collision' in error, got %q", err.Error())
	}
	if _, statErr := os.Stat(marcPath); statErr == nil {
		t.Error("archive file should not have been created on collision")
	}
}

// TestArchiveMulti_topLevelLayout verifies that each source surfaces as a
// top-level entry inside the archive, named after its basename, with no
// synthetic "." entry.
func TestArchiveMulti_topLevelLayout(t *testing.T) {
	tmp := t.TempDir()
	alpha := filepath.Join(tmp, "alpha")
	beta := filepath.Join(tmp, "beta")
	for _, d := range []string{alpha, beta} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(alpha, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "multi.marc")
	ctx := context.Background()

	if err := runtime.ArchiveMulti(ctx, marcPath, []string{alpha, beta}, "zstd", false); err != nil {
		t.Fatalf("ArchiveMulti: %v", err)
	}

	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	var paths []string
	if err := r.WalkEntries(func(relPath string, _ store.EntryRow) error {
		paths = append(paths, relPath)
		return nil
	}); err != nil {
		t.Fatalf("WalkEntries: %v", err)
	}

	sort.Strings(paths)

	for _, p := range paths {
		if p == "." {
			t.Errorf("multi-root archive should not contain a synthetic \".\" entry, found one")
		}
	}

	want := []string{"alpha", "alpha/a.txt", "beta", "beta/b.txt"}
	if !equalStrings(paths, want) {
		t.Fatalf("archive paths:\n got: %v\nwant: %v", paths, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
