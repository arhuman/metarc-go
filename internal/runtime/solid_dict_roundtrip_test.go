package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
)

// dictRoundTripBytes builds compressible content that still varies enough for a
// misapplied dictionary to show up as a content mismatch rather than decode
// cleanly by luck.
func dictRoundTripBytes(n int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"}
	var buf bytes.Buffer
	buf.Grow(n + 64)
	for buf.Len() < n {
		fmt.Fprintf(&buf, "%s %d %s %d\n",
			words[rng.Intn(len(words))], rng.Intn(100000),
			words[rng.Intn(len(words))], rng.Intn(100000))
	}
	return buf.Bytes()[:n]
}

// TestRoundTrip_interBlockDictionary archives a tree whose extension groups each
// span several solid blocks, so blocks 1..n of every group are compressed
// against their group's first block, then extracts it and compares content.
//
// This is the end-to-end gate on the inter-block dictionary: the reader has to
// resolve each block's dictionary from the catalog and decode it first, and a
// dictionary resolved to the wrong block corrupts content rather than erroring.
func TestRoundTrip_interBlockDictionary(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 4 MiB blocks so the 1 MiB minimum block size is a genuine fraction of a
	// block: an extension change then flushes and starts a new group instead of
	// pooling every extension into one.
	const blockSize = 4 << 20
	const perFile = 2 << 20

	want := make(map[string][]byte)
	seed := int64(1)
	for _, ext := range []string{".txt", ".go", ".json"} {
		sub := filepath.Join(srcDir, "d"+ext[1:])
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		// Six files of 2 MiB each: three blocks per extension, so every group
		// has a first block and two that depend on it.
		for i := range 6 {
			name := fmt.Sprintf("f%02d%s", i, ext)
			content := dictRoundTripBytes(perFile, seed)
			seed++
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

	marcPath := filepath.Join(tmp, "dict.marc")
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

// TestRoundTrip_noInterBlockDict pins the ablation switch: with
// NoInterBlockDict set, no block references another however large its extension
// group is, and the archive still round-trips.
//
// The tree is the one TestRoundTrip_interBlockDictionary uses, so the only
// difference between a chained archive and this one is the flag.
func TestRoundTrip_noInterBlockDict(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const blockSize = 4 << 20
	const perFile = 2 << 20

	want := make(map[string][]byte)
	seed := int64(1)
	for _, ext := range []string{".txt", ".go", ".json"} {
		sub := filepath.Join(srcDir, "d"+ext[1:])
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			name := fmt.Sprintf("f%02d%s", i, ext)
			content := dictRoundTripBytes(perFile, seed)
			seed++
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

	marcPath := filepath.Join(tmp, "plain.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.ArchiveWithOpts(ctx, marcPath, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: blockSize, NoInterBlockDict: true}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	assertNoDictBlocks(t, marcPath)

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

// assertNoDictBlocks opens the archive's catalog directly and fails if any blob
// row carries a dict_block_id. The column itself stays in the DDL: the flag
// removes the mechanism, not the schema.
func assertNoDictBlocks(t *testing.T, marcPath string) {
	t.Helper()
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = r.Close() }()

	if n := r.QueryDictBlockCount(); n != 0 {
		t.Errorf("%d blob row(s) carry dict_block_id, want 0 under NoInterBlockDict", n)
	}
}
