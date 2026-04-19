package scan

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

func TestResolveMultiRoots_ok(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "alpha")
	b := filepath.Join(tmp, "beta")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveMultiRoots([]string{a, b})
	if err != nil {
		t.Fatalf("ResolveMultiRoots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 resolved roots, got %d", len(got))
	}
	if got[0].BaseName != "alpha" || got[1].BaseName != "beta" {
		t.Errorf("unexpected basenames: %+v", got)
	}
	for _, r := range got {
		if !filepath.IsAbs(r.AbsPath) {
			t.Errorf("AbsPath should be absolute: %q", r.AbsPath)
		}
	}
}

func TestResolveMultiRoots_basenameCollision(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "x", "shared")
	b := filepath.Join(tmp, "y", "shared")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := ResolveMultiRoots([]string{a, b})
	if err == nil {
		t.Fatal("expected basename collision error, got nil")
	}
	if !strings.Contains(err.Error(), "basename collision") {
		t.Errorf("expected 'basename collision' in error, got %q", err.Error())
	}
}

func TestResolveMultiRoots_empty(t *testing.T) {
	if _, err := ResolveMultiRoots(nil); err == nil {
		t.Fatal("expected error for empty roots, got nil")
	}
}

func TestWalkMulti_prefixesBasename(t *testing.T) {
	tmp := t.TempDir()

	alpha := filepath.Join(tmp, "alpha")
	beta := filepath.Join(tmp, "beta")
	if err := os.MkdirAll(filepath.Join(alpha, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alpha, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alpha, "sub", "c.txt"), []byte("C"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots, err := ResolveMultiRoots([]string{alpha, beta})
	if err != nil {
		t.Fatalf("ResolveMultiRoots: %v", err)
	}

	ctx := context.Background()
	var entries []marc.Entry
	for e := range WalkMulti(ctx, roots) {
		entries = append(entries, e)
	}

	var paths []string
	for _, e := range entries {
		paths = append(paths, e.RelPath)
	}
	sort.Strings(paths)

	want := []string{
		"alpha",
		"alpha/a.txt",
		"alpha/sub",
		"alpha/sub/c.txt",
		"beta",
		"beta/b.txt",
	}
	if !equalStrings(paths, want) {
		t.Fatalf("paths:\n got: %v\nwant: %v", paths, want)
	}

	// The synthetic "." entry must not be produced by WalkMulti.
	for _, e := range entries {
		if e.RelPath == "." {
			t.Errorf("WalkMulti should not emit a \".\" entry")
		}
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
