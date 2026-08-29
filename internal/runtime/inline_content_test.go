package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setInlineFileLimit overrides the inline read threshold for one test.
func setInlineFileLimit(t *testing.T, limit int64) {
	t.Helper()
	prev := inlineFileLimit
	inlineFileLimit = limit
	t.Cleanup(func() { inlineFileLimit = prev })
}

// setInFlightBudget overrides the in-flight budget for one test.
func setInFlightBudget(t *testing.T, budget int64) {
	t.Helper()
	prev := inFlightBudget
	inFlightBudget = budget
	t.Cleanup(func() { inFlightBudget = prev })
}

// stampTree gives every path the same mtime so two archives of the same tree
// are byte-identical.
func stampTree(t *testing.T, root string) {
	t.Helper()
	ts := time.Unix(1700000000, 0)
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, ts, ts)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// compareContentTrees checks that every dir and file under want exists under
// got with identical content.
func compareContentTrees(t *testing.T, want, got string) {
	t.Helper()
	err := filepath.Walk(want, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(want, path)
		if err != nil {
			return err
		}
		other := filepath.Join(got, rel)
		if info.IsDir() {
			st, statErr := os.Stat(other)
			if statErr != nil {
				t.Errorf("missing dir %s: %v", rel, statErr)
				return nil
			}
			if !st.IsDir() {
				t.Errorf("%s is not a dir", rel)
			}
			return nil
		}
		wantData, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		gotData, readErr := os.ReadFile(other)
		if readErr != nil {
			t.Errorf("missing file %s: %v", rel, readErr)
			return nil
		}
		if !bytes.Equal(wantData, gotData) {
			t.Errorf("content mismatch for %s: %d vs %d bytes", rel, len(wantData), len(gotData))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchive_inlineAndStreamingContentRoundTrip(t *testing.T) {
	setInlineFileLimit(t, 4096)

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Below, at and above the inline limit, plus an empty file and a symlink.
	files := map[string][]byte{
		"small.go":            bytes.Repeat([]byte("package a\n"), 8),
		"exact.txt":           bytes.Repeat([]byte("x"), 4096),
		"over.txt":            bytes.Repeat([]byte("abcdefgh"), 4096),
		"empty.txt":           nil,
		"sub/mid.json":        []byte(`{"a":1}`),
		"sub/deep/large.data": bytes.Repeat([]byte("0123456789"), 2000),
	}
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(src, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("small.go", filepath.Join(src, "link.go")); err != nil {
		t.Fatal(err)
	}
	stampTree(t, src)

	out := t.TempDir()
	marcPath := filepath.Join(out, "a.marc")
	if err := Archive(context.Background(), marcPath, src, "zstd", false, 4); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(out, "x")
	if err := Extract(context.Background(), marcPath, dst); err != nil {
		t.Fatal(err)
	}
	compareContentTrees(t, src, dst)

	target, err := os.Readlink(filepath.Join(dst, "link.go"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "small.go" {
		t.Errorf("symlink target = %q, want %q", target, "small.go")
	}
}

func TestArchive_inlineAndStreamingProduceIdenticalBytes(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		rel := filepath.Join("pkg", fmt.Sprintf("f%02d.go", i))
		data := fmt.Appendf(nil, "package pkg\n\nfunc F%d() int { return %d }\n", i, i)
		if err := os.WriteFile(filepath.Join(src, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), bytes.Repeat([]byte("note\n"), 500), 0o644); err != nil {
		t.Fatal(err)
	}
	stampTree(t, src)

	out := t.TempDir()

	// inlineFileLimit 0 forces every file down the streaming path.
	setInlineFileLimit(t, 0)
	streamPath := filepath.Join(out, "stream.marc")
	if err := Archive(context.Background(), streamPath, src, "zstd", false, 1); err != nil {
		t.Fatal(err)
	}

	setInlineFileLimit(t, 1<<20)
	inlinePath := filepath.Join(out, "inline.marc")
	if err := Archive(context.Background(), inlinePath, src, "zstd", false, 1); err != nil {
		t.Fatal(err)
	}

	streamBytes, err := os.ReadFile(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	inlineBytes, err := os.ReadFile(inlinePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamBytes, inlineBytes) {
		t.Errorf("archive bytes differ between content paths: streaming %d bytes, inline %d bytes", len(streamBytes), len(inlineBytes))
	}
}

func TestArchive_tinyInFlightBudgetDoesNotDeadlock(t *testing.T) {
	setInlineFileLimit(t, 2048)
	// Three entries' worth of budget against 300 files: the sequencer must
	// block and resume, not deadlock or reorder.
	setInFlightBudget(t, 3*(entryCostOverhead+2048))

	src := t.TempDir()
	for d := range 5 {
		dir := filepath.Join(src, fmt.Sprintf("d%d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range 60 {
			name := fmt.Sprintf("f%03d.txt", i)
			data := fmt.Appendf(nil, "dir %d file %d\n%s", d, i, bytes.Repeat([]byte("payload "), i+1))
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	stampTree(t, src)

	out := t.TempDir()
	marcPath := filepath.Join(out, "b.marc")

	done := make(chan error, 1)
	go func() { done <- Archive(context.Background(), marcPath, src, "zstd", false, 4) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("archive did not finish: in-flight budget deadlocked")
	}

	dst := filepath.Join(out, "x")
	if err := Extract(context.Background(), marcPath, dst); err != nil {
		t.Fatal(err)
	}
	compareContentTrees(t, src, dst)
}

func TestEntryCost_neverExceedsBudget(t *testing.T) {
	if inlineFileLimit+entryCostOverhead > inFlightBudget {
		t.Fatalf("one entry (%d bytes) exceeds the in-flight budget (%d): the sequencer can wait forever",
			inlineFileLimit+entryCostOverhead, inFlightBudget)
	}
}
