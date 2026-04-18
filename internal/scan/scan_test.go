package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc/pkg/marc"
)

func TestWalk_basicFiles(t *testing.T) {
	tmp := t.TempDir()

	// Create a simple tree: root/a.txt, root/sub/b.txt
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ch := Walk(ctx, tmp)

	var entries []marc.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	// Expect: root dir, a.txt, sub dir, b.txt (order: WalkDir traversal)
	if len(entries) < 4 {
		t.Fatalf("expected at least 4 entries, got %d", len(entries))
	}

	// Verify all entries have absolute paths.
	for _, e := range entries {
		if !filepath.IsAbs(e.Path) {
			t.Errorf("entry path not absolute: %q", e.Path)
		}
	}

	// Verify RelPath is relative.
	for _, e := range entries {
		if filepath.IsAbs(e.RelPath) {
			t.Errorf("entry RelPath should be relative, got %q", e.RelPath)
		}
	}
}

func TestWalk_symlink(t *testing.T) {
	tmp := t.TempDir()

	// Create a file and a symlink to it.
	if err := os.WriteFile(filepath.Join(tmp, "real.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(tmp, "link.txt")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ch := Walk(ctx, tmp)

	var entries []marc.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	// Find the symlink entry.
	var symEntry *marc.Entry
	for i := range entries {
		if entries[i].LinkTarget != "" {
			symEntry = &entries[i]
			break
		}
	}

	if symEntry == nil {
		t.Fatal("expected a symlink entry with non-empty LinkTarget")
	}
	if symEntry.LinkTarget != "real.txt" {
		t.Errorf("LinkTarget: got %q, want %q", symEntry.LinkTarget, "real.txt")
	}
}

func TestWalk_contextCancellation(t *testing.T) {
	tmp := t.TempDir()

	// Create some files.
	for i := range 20 {
		name := filepath.Join(tmp, "file"+string(rune('a'+i%26))+".txt")
		_ = os.WriteFile(name, []byte("content"), 0o644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := Walk(ctx, tmp)

	// Cancel immediately and drain.
	cancel()
	for range ch {
		// drain
	}
	// Channel must be closed (no hang).
}

func TestWalk_emptyDir(t *testing.T) {
	tmp := t.TempDir()

	ctx := context.Background()
	ch := Walk(ctx, tmp)

	var entries []marc.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	// Only the root directory itself is walked.
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (root dir), got %d", len(entries))
	}
	if entries[0].RelPath != "." {
		t.Errorf("root entry RelPath: got %q, want %q", entries[0].RelPath, ".")
	}
}

func TestWalk_invalidRoot(t *testing.T) {
	ctx := context.Background()
	ch := Walk(ctx, "/nonexistent/path/that/does/not/exist/xyz")

	var entries []marc.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	// Channel is closed, no panic; entries may be empty.
	_ = entries
}

func TestWalk_relativeRoot(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Change to tmp so that a relative path "." resolves.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	ctx := context.Background()
	ch := Walk(ctx, ".")

	var entries []marc.Entry
	for e := range ch {
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		t.Fatal("expected entries for relative root, got none")
	}
	for _, e := range entries {
		if !filepath.IsAbs(e.Path) {
			t.Errorf("Path not absolute even with relative root: %q", e.Path)
		}
	}
}
