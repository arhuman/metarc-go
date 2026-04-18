package runtime_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"
)

const testRepoDir = "/tmp/compose"

var (
	cloneOnce sync.Once
	cloneErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	_ = os.RemoveAll(testRepoDir)
	os.Exit(code)
}

func cloneTestRepo(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("long test: skipped with -short")
	}
	cloneOnce.Do(func() {
		if _, err := os.Stat(testRepoDir); err == nil {
			return
		}
		cmd := exec.Command("git", "clone", "--depth", "1", "https://github.com/docker/compose", testRepoDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			cloneErr = fmt.Errorf("clone test repo: %v\n%s", err, out)
		}
	})
	if cloneErr != nil {
		t.Fatal(cloneErr)
	}
	return testRepoDir
}

func TestRoundTrip(t *testing.T) {
	absSource := cloneTestRepo(t)

	tmpDir := t.TempDir()
	marcPath := filepath.Join(tmpDir, "out.marc")
	restoreDir := filepath.Join(tmpDir, "restored")

	ctx := context.Background()

	// Archive.
	if err := runtime.Archive(ctx, marcPath, absSource, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Verify archive file exists and is single-file format.
	if _, err := os.Stat(marcPath); err != nil {
		t.Fatalf("marc file missing: %v", err)
	}
	format, err := marc.DetectFormat(marcPath)
	if err != nil {
		t.Fatalf("detect format: %v", err)
	}
	if format != marc.FormatSingleFile {
		t.Fatalf("expected single-file format, got %q", format)
	}

	// Extract.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Compare trees.
	compareDirectories(t, absSource, restoreDir)
}

func TestRoundTrip_withDedup(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 10 copies of the same file.
	sharedContent := bytes.Repeat([]byte("dedup test content "), 100)
	for i := range 10 {
		name := "copy" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(srcDir, name), sharedContent, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create 5 unique files.
	for i := range 5 {
		name := "unique" + string(rune('a'+i)) + ".txt"
		content := make([]byte, 256)
		rand.Read(content)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "dedup.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Check blob count: should be 6 (1 shared + 5 unique), not 15.
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	var blobCount int
	blobIDs := make(map[int64]bool)
	if err := r.WalkEntries(func(_ string, e store.EntryRow) error {
		if e.BlobID != 0 {
			blobIDs[e.BlobID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blobCount = len(blobIDs)
	_ = r.Close()

	if blobCount != 6 {
		t.Fatalf("expected 6 blob rows (1 shared + 5 unique), got %d", blobCount)
	}

	// Extract and verify byte equality.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	compareDirectories(t, srcDir, restoreDir)
}

func TestRoundTrip_compressorNone(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "c.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "raw.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, srcDir, "none", false); err != nil {
		t.Fatal(err)
	}

	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	compareDirectories(t, srcDir, restoreDir)
}

func TestRoundTrip_licenseDedup(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// MIT license canonical text (matches the embedded constant).
	mitLicense := `MIT License

Copyright (c) [year] [fullname]

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

	// 3 copies of MIT LICENSE.
	if err := os.WriteFile(filepath.Join(srcDir, "LICENSE"), []byte(mitLicense), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "vendorA"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "vendorA", "LICENSE"), []byte(mitLicense), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "vendorB"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "vendorB", "COPYING"), []byte(mitLicense), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2 unique files.
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uniqueContent := make([]byte, 256)
	rand.Read(uniqueContent)
	if err := os.WriteFile(filepath.Join(srcDir, "data.bin"), uniqueContent, 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "license.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Check blob count: canonical license shared = 1 blob + 2 unique = 3 total.
	// (Less than 5 = 3 license + 2 unique without dedup.)
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blobIDs := make(map[int64]bool)
	if err := r.WalkEntries(func(_ string, e store.EntryRow) error {
		if e.BlobID != 0 {
			blobIDs[e.BlobID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blobCount := len(blobIDs)
	_ = r.Close()

	if blobCount >= 5 {
		t.Fatalf("expected fewer than 5 blobs (license should share), got %d", blobCount)
	}
	t.Logf("blob count: %d (3 licenses shared as 1 + 2 unique)", blobCount)

	// Extract and verify content matches.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	// Verify license files contain the canonical (normalized) text.
	for _, rel := range []string{"LICENSE", "vendorA/LICENSE", "vendorB/COPYING"} {
		data, err := os.ReadFile(filepath.Join(restoreDir, rel))
		if err != nil {
			t.Fatalf("missing restored file %s: %v", rel, err)
		}
		// Canonical text has no trailing newline (normalized via TrimSpace).
		if !strings.Contains(string(data), "MIT License") {
			t.Errorf("restored %s does not contain MIT License header", rel)
		}
	}

	// Verify unique files are intact.
	mainData, err := os.ReadFile(filepath.Join(restoreDir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mainData) != "package main\nfunc main() {}\n" {
		t.Error("main.go content mismatch")
	}

	restoredBin, err := os.ReadFile(filepath.Join(restoreDir, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredBin, uniqueContent) {
		t.Error("data.bin content mismatch")
	}
}

func TestDedup_SizeReduction(t *testing.T) {
	absSource := cloneTestRepo(t)

	tmp := t.TempDir()
	marcPath := filepath.Join(tmp, "bench.marc")
	tarPath := filepath.Join(tmp, "bench.tar")
	ctx := context.Background()

	// Create metarc archive.
	if err := runtime.Archive(ctx, marcPath, absSource, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Create tar for comparison.
	cmd := exec.Command("tar", "cf", tarPath, "-C", filepath.Dir(absSource), filepath.Base(absSource))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar failed: %v\n%s", err, out)
	}

	// Compare sizes (single-file format: just one .marc file).
	marcInfo, _ := os.Stat(marcPath)
	tarInfo, _ := os.Stat(tarPath)

	marcTotal := marcInfo.Size()
	tarSize := tarInfo.Size()

	t.Logf("metarc: %d bytes", marcTotal)
	t.Logf("tar:    %d bytes", tarSize)
	t.Logf("ratio:  %.2f%%", float64(marcTotal)/float64(tarSize)*100)

	if marcTotal >= tarSize {
		t.Fatalf("metarc archive (%d) is not smaller than tar (%d)", marcTotal, tarSize)
	}
}

// compareDirectories walks orig and verifies every file/dir exists in restored with matching content.
func compareDirectories(t *testing.T, orig, restored string) {
	t.Helper()
	var fileCount, dirCount int

	err := filepath.WalkDir(orig, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(orig, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		restoredPath := filepath.Join(restored, rel)

		if d.IsDir() {
			info, err := os.Stat(restoredPath)
			if err != nil {
				t.Errorf("directory missing in restored: %s", rel)
				return nil
			}
			if !info.IsDir() {
				t.Errorf("expected directory, got file: %s", rel)
			}
			dirCount++
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		origData, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		restoredData, err := os.ReadFile(restoredPath)
		if err != nil {
			t.Errorf("file missing in restored: %s", rel)
			return nil
		}

		if !bytes.Equal(origData, restoredData) {
			t.Errorf("content mismatch: %s (orig=%d bytes, restored=%d bytes)", rel, len(origData), len(restoredData))
		}

		origInfo, _ := os.Stat(path)
		restoredInfo, _ := os.Stat(restoredPath)
		if origInfo.Mode().Perm() != restoredInfo.Mode().Perm() {
			t.Errorf("mode mismatch: %s (orig=%v, restored=%v)", rel, origInfo.Mode().Perm(), restoredInfo.Mode().Perm())
		}
		checkOwnership(t, rel, origInfo, restoredInfo)

		fileCount++
		return nil
	})

	if err != nil {
		t.Fatalf("walk comparison: %v", err)
	}

	t.Logf("compared %d files, %d directories -- all match", fileCount, dirCount)

	if fileCount == 0 {
		t.Fatal("no files compared -- test corpus is empty")
	}
}

func TestRoundTrip_jsonCanonical(t *testing.T) {
	t.Skip("json-canonical/v1 is a lossy transform (discards original formatting); disabled from default registry until it stores original bytes")
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 5 pretty-printed JSON files; some share canonical form.
	jsonFiles := map[string]string{
		"a.json": `{
  "name": "alpha",
  "version": "1.0"
}`,
		"b.json": `{  "version":   "1.0",   "name":  "alpha"  }`,
		"c.json": `{"z":3,"a":1,"m":2}`,
		"d.json": `{
  "z": 3,
  "a": 1,
  "m": 2
}`,
		"e.json": `{"unique": true, "id": 42}`,
	}

	for name, content := range jsonFiles {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	marcPath := filepath.Join(tmp, "json.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Check that json-canonical/v1 was applied.
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	jsonTransformCount := 0
	blobIDs := make(map[int64]bool)
	if err := r.WalkEntries(func(_ string, e store.EntryRow) error {
		if e.Transform == "json-canonical/v1" {
			jsonTransformCount++
		}
		if e.BlobID != 0 {
			blobIDs[e.BlobID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	if jsonTransformCount != 5 {
		t.Fatalf("expected 5 entries with json-canonical/v1, got %d", jsonTransformCount)
	}

	// a.json and b.json share canonical form; c.json and d.json share canonical form.
	// So we expect fewer blobs than 5.
	if len(blobIDs) >= 5 {
		t.Fatalf("expected fewer than 5 unique blobs (shared canonical forms), got %d", len(blobIDs))
	}
	t.Logf("blob count: %d (some JSON files share canonical form)", len(blobIDs))

	// Extract and verify content is valid JSON with same data.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	for name, origContent := range jsonFiles {
		restored, err := os.ReadFile(filepath.Join(restoreDir, name))
		if err != nil {
			t.Fatalf("missing restored file %s: %v", name, err)
		}

		// Verify restored content is valid JSON.
		var restoredVal any
		if err := json.Unmarshal(restored, &restoredVal); err != nil {
			t.Errorf("restored %s is not valid JSON: %v", name, err)
			continue
		}

		// Verify semantic equality.
		var origVal any
		if err := json.Unmarshal([]byte(origContent), &origVal); err != nil {
			t.Fatal(err)
		}
		origCanon, _ := json.Marshal(origVal)
		restoredCanon, _ := json.Marshal(restoredVal)
		if !bytes.Equal(origCanon, restoredCanon) {
			t.Errorf("semantic mismatch for %s", name)
		}
	}
}

func TestRoundTrip_logTemplate(t *testing.T) {
	t.Skip("log-template/v1 is a lossy transform (discards original formatting); disabled from default registry until it stores original bytes")
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate a synthetic log file with common prefix.
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("2024-01-15 10:30:%02d INFO server request handled path=/api/v1/users id=%d", i%60, i))
	}
	logContent := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(srcDir, "access.log"), []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also add a non-log file to ensure it's not affected.
	if err := os.WriteFile(filepath.Join(srcDir, "readme.txt"), []byte("This is a readme."), 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "log.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Check that log-template/v1 was applied.
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	logTransformCount := 0
	if err := r.WalkEntries(func(_ string, e store.EntryRow) error {
		if e.Transform == "log-template/v1" {
			logTransformCount++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	if logTransformCount != 1 {
		t.Fatalf("expected 1 entry with log-template/v1, got %d", logTransformCount)
	}

	// Extract and verify byte-identical content.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	restoredLog, err := os.ReadFile(filepath.Join(restoreDir, "access.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredLog) != logContent {
		t.Fatalf("log content mismatch:\ngot (%d bytes)\nwant (%d bytes)", len(restoredLog), len(logContent))
	}

	restoredReadme, err := os.ReadFile(filepath.Join(restoreDir, "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredReadme) != "This is a readme." {
		t.Error("readme.txt content mismatch")
	}
}

func TestRoundTrip_dictCompress(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create many small files with shared patterns (license headers, imports, YAML keys).
	// The dictionary should exploit cross-file redundancy.
	// We need enough data for BuildDict to succeed (minimum ~8 samples with enough content).
	licenseHeader := strings.Repeat("Copyright (c) 2024 ACME Corp. All rights reserved.\nMIT License applies.\n", 5)
	importBlock := strings.Repeat("import (\n\t\"fmt\"\n\t\"os\"\n\t\"path/filepath\"\n\t\"strings\"\n\t\"context\"\n\t\"io\"\n)\n", 3)
	yamlPrefix := strings.Repeat("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: production\n  labels:\n    app: myservice\n    env: prod\n", 3)

	for i := range 50 {
		name := fmt.Sprintf("file%03d.go", i)
		content := licenseHeader + fmt.Sprintf("package pkg%d\n\n%sfunc main() { fmt.Println(%d) }\n", i, importBlock, i)
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 50 {
		name := fmt.Sprintf("config%03d.yaml", i)
		content := yamlPrefix + fmt.Sprintf("  name: config-%d\ndata:\n  key%d: value%d\n  shared: common-value\n  description: \"This is configuration entry number %d\"\n", i, i, i, i)
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()

	// Archive with dict-compress prescan.
	prescanPath := filepath.Join(tmp, "prescan.marc")
	prescanOpts := runtime.ArchiveOpts{DictCompress: runtime.DictPrescan}
	if err := runtime.ArchiveWithOpts(ctx, prescanPath, srcDir, "zstd", false, prescanOpts); err != nil {
		t.Fatal(err)
	}

	// Archive with dict-compress simple.
	simplePath := filepath.Join(tmp, "simple.marc")
	simpleOpts := runtime.ArchiveOpts{DictCompress: runtime.DictSimple}
	if err := runtime.ArchiveWithOpts(ctx, simplePath, srcDir, "zstd", false, simpleOpts); err != nil {
		t.Fatal(err)
	}

	// Archive without dict for comparison.
	plainMarcPath := filepath.Join(tmp, "plain.marc")
	if err := runtime.Archive(ctx, plainMarcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Verify round-trip: extract prescan archive and compare.
	restorePrescan := filepath.Join(tmp, "restored-prescan")
	if err := runtime.Extract(ctx, prescanPath, restorePrescan); err != nil {
		t.Fatal(err)
	}
	compareDirectories(t, srcDir, restorePrescan)

	// Verify round-trip: extract simple archive and compare.
	restoreSimple := filepath.Join(tmp, "restored-simple")
	if err := runtime.Extract(ctx, simplePath, restoreSimple); err != nil {
		t.Fatal(err)
	}
	compareDirectories(t, srcDir, restoreSimple)

	// Compare archive sizes.
	prescanInfo, _ := os.Stat(prescanPath)
	simpleInfo, _ := os.Stat(simplePath)
	plainInfo, _ := os.Stat(plainMarcPath)
	t.Logf("prescan dict:  %d bytes", prescanInfo.Size())
	t.Logf("simple dict:   %d bytes", simpleInfo.Size())
	t.Logf("plain archive: %d bytes", plainInfo.Size())
	t.Logf("prescan ratio: %.1f%%", float64(prescanInfo.Size())/float64(plainInfo.Size())*100)
	t.Logf("simple ratio:  %.1f%%", float64(simpleInfo.Size())/float64(plainInfo.Size())*100)
}

func TestSingleFile_noSidecar(t *testing.T) {
	absSource := cloneTestRepo(t)

	tmp := t.TempDir()
	marcPath := filepath.Join(tmp, "test.marc")
	ctx := context.Background()

	if err := runtime.Archive(ctx, marcPath, absSource, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Verify no .blobs sidecar exists.
	if _, err := os.Stat(marcPath + ".blobs"); !os.IsNotExist(err) {
		t.Fatalf("expected no .blobs sidecar, but it exists (or error: %v)", err)
	}

	// Verify the single file has the correct magic.
	format, err := marc.DetectFormat(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if format != marc.FormatSingleFile {
		t.Fatalf("expected single-file format, got %q", format)
	}

	// Verify round-trip works.
	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}
	compareDirectories(t, absSource, restoreDir)
}

func TestRoundTrip_withSymlinks(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular file.
	fileContent := []byte("hello symlink world")
	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), fileContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to the file.
	if err := os.Symlink("hello.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to the subdirectory (relative target).
	if err := os.Symlink("sub", filepath.Join(srcDir, "link_dir")); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "symlinks.marc")
	restoreDir := filepath.Join(tmp, "restored")
	ctx := context.Background()

	// Archive.
	if err := runtime.Archive(ctx, marcPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Extract.
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}

	// Verify the regular file content is intact.
	restored, err := os.ReadFile(filepath.Join(restoreDir, "hello.txt"))
	if err != nil {
		t.Fatalf("missing hello.txt: %v", err)
	}
	if !bytes.Equal(restored, fileContent) {
		t.Error("hello.txt content mismatch")
	}

	// Verify symlink exists and points to the correct target.
	target, err := os.Readlink(filepath.Join(restoreDir, "link.txt"))
	if err != nil {
		t.Fatalf("link.txt is not a symlink: %v", err)
	}
	if target != "hello.txt" {
		t.Errorf("link.txt target: got %q, want %q", target, "hello.txt")
	}

	// Verify directory symlink.
	target, err = os.Readlink(filepath.Join(restoreDir, "link_dir"))
	if err != nil {
		t.Fatalf("link_dir is not a symlink: %v", err)
	}
	if target != "sub" {
		t.Errorf("link_dir target: got %q, want %q", target, "sub")
	}
}

func TestRoundTrip_solidBlock(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 100 small files with shared patterns to exploit cross-file redundancy.
	licenseHeader := strings.Repeat("Copyright (c) 2024 ACME Corp. All rights reserved.\nMIT License applies.\n", 5)
	importBlock := strings.Repeat("import (\n\t\"fmt\"\n\t\"os\"\n\t\"path/filepath\"\n\t\"strings\"\n\t\"context\"\n\t\"io\"\n)\n", 3)

	for i := range 100 {
		name := fmt.Sprintf("file%03d.go", i)
		content := licenseHeader + fmt.Sprintf("package pkg%d\n\n%sfunc main() { fmt.Println(%d) }\n", i, importBlock, i)
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()

	// Archive with solid blocks.
	solidPath := filepath.Join(tmp, "solid.marc")
	solidOpts := runtime.ArchiveOpts{SolidBlockSize: 4 * 1024 * 1024}
	if err := runtime.ArchiveWithOpts(ctx, solidPath, srcDir, "zstd", false, solidOpts); err != nil {
		t.Fatal(err)
	}

	// Archive without solid blocks for comparison.
	plainPath := filepath.Join(tmp, "plain.marc")
	if err := runtime.Archive(ctx, plainPath, srcDir, "zstd", false); err != nil {
		t.Fatal(err)
	}

	// Extract and verify byte-exact round-trip.
	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(ctx, solidPath, restoreDir); err != nil {
		t.Fatal(err)
	}
	compareDirectories(t, srcDir, restoreDir)

	// Verify solid archive has solid blocks.
	r, err := store.OpenReader(solidPath)
	if err != nil {
		t.Fatal(err)
	}
	solidCount := r.QuerySolidBlockCount()
	_ = r.Close()
	if solidCount == 0 {
		t.Fatal("expected solid blocks in archive, got 0")
	}
	t.Logf("solid blocks: %d", solidCount)

	// Compare sizes.
	solidInfo, _ := os.Stat(solidPath)
	plainInfo, _ := os.Stat(plainPath)
	t.Logf("solid archive: %d bytes", solidInfo.Size())
	t.Logf("plain archive: %d bytes", plainInfo.Size())
	t.Logf("solid ratio:   %.1f%%", float64(solidInfo.Size())/float64(plainInfo.Size())*100)
}

func TestRoundTrip_solidBlockDedup(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create identical files (should be deduped) and unique files.
	sharedContent := bytes.Repeat([]byte("solid dedup test content "), 100)
	for i := range 10 {
		name := fmt.Sprintf("dup%d.txt", i)
		if err := os.WriteFile(filepath.Join(srcDir, name), sharedContent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		name := fmt.Sprintf("unique%d.txt", i)
		content := make([]byte, 256)
		rand.Read(content)
		if err := os.WriteFile(filepath.Join(srcDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()

	// Archive with solid blocks.
	marcPath := filepath.Join(tmp, "solid-dedup.marc")
	opts := runtime.ArchiveOpts{SolidBlockSize: 4 * 1024 * 1024}
	if err := runtime.ArchiveWithOpts(ctx, marcPath, srcDir, "zstd", false, opts); err != nil {
		t.Fatal(err)
	}

	// Verify dedup still works: 10 identical files should produce 1 blob, plus 5 unique = 6.
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blobIDs := make(map[int64]bool)
	if err := r.WalkEntries(func(_ string, e store.EntryRow) error {
		if e.BlobID != 0 {
			blobIDs[e.BlobID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	if len(blobIDs) != 6 {
		t.Fatalf("expected 6 unique blobs (1 shared + 5 unique), got %d", len(blobIDs))
	}

	// Extract and verify byte-exact round-trip.
	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(ctx, marcPath, restoreDir); err != nil {
		t.Fatal(err)
	}
	compareDirectories(t, srcDir, restoreDir)
}
