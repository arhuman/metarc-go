package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runCLI drives the full cobra command tree in-process, exactly as main() does,
// and returns the combined stdout+stderr the command produced.
//
// Cobra writes command output through cmd.Print*, so redirecting Out and Err
// captures everything the subcommands emit. Errors are returned rather than
// asserted so callers can cover both the success and the failure paths.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	// Usage text on error would swamp the assertions below.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	return buf.String(), err
}

// mustRunCLI is runCLI for the cases where the command is expected to succeed.
func mustRunCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("metarc %s: unexpected error: %v\noutput:\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// corpusFile is one file written into a test source tree.
type corpusFile struct {
	path string
	body string
}

// writeCorpus materialises a small but structurally varied source tree: two
// identical files (to exercise dedup), a Go file (go-line-subst/v1), a JSON
// file, a nested directory, and an empty file.
func writeCorpus(t *testing.T) (string, []corpusFile) {
	t.Helper()

	const dup = "shared payload line\nrepeated across two files\n"
	files := []corpusFile{
		{"a.txt", dup},
		{"nested/b.txt", dup}, // byte-identical to a.txt: must dedup to one blob
		{"main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"},
		{"conf.json", "{\"alpha\": 1, \"beta\": [2, 3], \"gamma\": \"delta\"}\n"},
		{"nested/deep/notes.md", strings.Repeat("# heading\n\nsome prose here.\n\n", 40)},
		{"empty.txt", ""},
	}

	src := t.TempDir()
	for _, f := range files {
		full := filepath.Join(src, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f.path, err)
		}
		if err := os.WriteFile(full, []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.path, err)
		}
	}
	return src, files
}

// assertCorpusExtracted checks every corpus file came back byte-identical
// under root.
func assertCorpusExtracted(t *testing.T, root string, files []corpusFile) {
	t.Helper()
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.path)))
		if err != nil {
			t.Errorf("extracted %s: %v", f.path, err)
			continue
		}
		if string(got) != f.body {
			t.Errorf("extracted %s: content mismatch\n got: %q\nwant: %q", f.path, got, f.body)
		}
	}
}

// TestCLIRoundTrip is the end-to-end CLI contract: archive a tree, inspect it,
// verify it, extract it, and confirm the extracted bytes match the source.
// This is the acceptance test for the whole command surface.
func TestCLIRoundTrip(t *testing.T) {
	src, files := writeCorpus(t)
	marcPath := filepath.Join(t.TempDir(), "out.marc")

	archiveOut := mustRunCLI(t, "archive", marcPath, src, "--explain")
	if !strings.Contains(archiveOut, "Plan Summary") {
		t.Errorf("--explain should print a plan summary, got:\n%s", archiveOut)
	}
	if _, err := os.Stat(marcPath); err != nil {
		t.Fatalf("archive did not produce %s: %v", marcPath, err)
	}

	inspectOut := mustRunCLI(t, "inspect", marcPath)
	for _, want := range []string{"Entries:", "Blobs:", "Original size:", "Archive layout"} {
		if !strings.Contains(inspectOut, want) {
			t.Errorf("inspect output missing %q, got:\n%s", want, inspectOut)
		}
	}

	verifyOut := mustRunCLI(t, "verify", marcPath)
	if !strings.Contains(verifyOut, "Footer checksum:      ok") {
		t.Errorf("verify should report a good footer, got:\n%s", verifyOut)
	}
	if !strings.Contains(verifyOut, "\nok\n") {
		t.Errorf("verify should end with ok, got:\n%s", verifyOut)
	}

	dest := t.TempDir()
	mustRunCLI(t, "extract", marcPath, "-C", dest)
	assertCorpusExtracted(t, dest, files)
}

// TestCLIArchiveMultiSource covers the multi-source layout, where each source
// becomes a top-level directory named after its basename.
func TestCLIArchiveMultiSource(t *testing.T) {
	parent := t.TempDir()
	frontend := filepath.Join(parent, "frontend")
	backend := filepath.Join(parent, "backend")
	for _, d := range []string{frontend, backend} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte(filepath.Base(d)+" content\n"), 0o644); err != nil {
			t.Fatalf("write in %s: %v", d, err)
		}
	}

	marcPath := filepath.Join(t.TempDir(), "multi.marc")
	mustRunCLI(t, "archive", marcPath, frontend, backend)

	dest := t.TempDir()
	mustRunCLI(t, "extract", marcPath, "-C", dest)

	for _, name := range []string{"frontend", "backend"} {
		got, err := os.ReadFile(filepath.Join(dest, name, "file.txt"))
		if err != nil {
			t.Errorf("extracted %s/file.txt: %v", name, err)
			continue
		}
		if string(got) != name+" content\n" {
			t.Errorf("%s/file.txt: got %q", name, got)
		}
	}
}

// TestCLIArchiveOptions exercises the archive flag surface that changes the
// on-disk layout, checking each variant still round-trips.
func TestCLIArchiveOptions(t *testing.T) {
	cases := []struct {
		name  string
		flags []string
	}{
		{"no solid blocks", []string{"--no-solid"}},
		{"uncompressed blobs", []string{"--final-compressor", "none"}},
		{"explicit zstd levels", []string{"--zstd-level", "3", "--zstd-level-catalog", "11"}},
		{"small solid blocks", []string{"--solid-block-size", "1MB"}},
		{"single worker", []string{"--workers", "1"}},
		{"explicit zstd window", []string{"--zstd-window", "1MB"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, files := writeCorpus(t)
			marcPath := filepath.Join(t.TempDir(), "out.marc")

			args := append([]string{"archive", marcPath, src}, tc.flags...)
			mustRunCLI(t, args...)

			mustRunCLI(t, "verify", marcPath)

			dest := t.TempDir()
			mustRunCLI(t, "extract", marcPath, "-C", dest)
			assertCorpusExtracted(t, dest, files)
		})
	}
}

// TestCLIArchiveInvalidFlags covers the flag-validation error paths, which
// must fail before any archive file is created.
func TestCLIArchiveInvalidFlags(t *testing.T) {
	cases := []struct {
		name    string
		flags   []string
		wantMsg string
	}{
		{"zstd level too high", []string{"--zstd-level", "12"}, "zstd level"},
		{"per-chunk level out of range", []string{"--zstd-level-blob", "99"}, "zstd-level-blob"},
		{"window not a power of two", []string{"--zstd-window", "3MB"}, "power of two"},
		{"window below minimum", []string{"--zstd-window", "512"}, "zstd-window"},
		{"unparseable solid block size", []string{"--solid-block-size", "abc"}, "solid-block-size"},
		{"non-positive solid block size", []string{"--solid-block-size", "0"}, "solid-block-size"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := writeCorpus(t)
			marcPath := filepath.Join(t.TempDir(), "should-not-exist.marc")

			args := append([]string{"archive", marcPath, src}, tc.flags...)
			out, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("expected an error, got success:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
			if _, statErr := os.Stat(marcPath); statErr == nil {
				t.Errorf("archive file was created despite a flag error")
			}
		})
	}
}

// TestCLIDisableTransform checks --disable-transform is honoured and that the
// archive still round-trips without the transform.
//
// plan.Disabled is package-level state mutated by this flag, so this test must
// not run in parallel with anything that reads the registry.
func TestCLIDisableTransform(t *testing.T) {
	src, files := writeCorpus(t)
	marcPath := filepath.Join(t.TempDir(), "nodedup.marc")

	mustRunCLI(t, "archive", marcPath, src,
		"--disable-transform", "go-line-subst/v1",
		"--keep-plan-log")

	out := mustRunCLI(t, "inspect", marcPath, "--plan-log")
	if strings.Contains(out, "go-line-subst/v1") {
		t.Errorf("disabled transform still appears in the plan log:\n%s", out)
	}

	dest := t.TempDir()
	mustRunCLI(t, "extract", marcPath, "-C", dest)
	assertCorpusExtracted(t, dest, files)
}

// TestCLIInspectVariants covers the inspect flag surface: --plan-log against
// an archive built without one, and --raw.
func TestCLIInspectVariants(t *testing.T) {
	src, _ := writeCorpus(t)
	marcPath := filepath.Join(t.TempDir(), "out.marc")
	mustRunCLI(t, "archive", marcPath, src)

	t.Run("plan-log absent", func(t *testing.T) {
		out := mustRunCLI(t, "inspect", marcPath, "--plan-log")
		if !strings.Contains(out, "plan_log is empty") {
			t.Errorf("expected the empty plan_log notice, got:\n%s", out)
		}
	})

	t.Run("plan-log present", func(t *testing.T) {
		withLog := filepath.Join(t.TempDir(), "withlog.marc")
		mustRunCLI(t, "archive", withLog, src, "--keep-plan-log")
		out := mustRunCLI(t, "inspect", withLog, "--plan-log")
		if !strings.Contains(out, "Plan log") || !strings.Contains(out, "Transform") {
			t.Errorf("expected a populated plan log, got:\n%s", out)
		}
	})

	t.Run("raw prints sqlite hint", func(t *testing.T) {
		out := mustRunCLI(t, "inspect", marcPath, "--raw")
		if !strings.Contains(out, "sqlite3") {
			t.Errorf("--raw should print a sqlite3 hint, got:\n%s", out)
		}
	})
}

// TestCLIMissingArchive checks the read-side commands fail cleanly when handed
// a path that does not exist, rather than panicking.
func TestCLIMissingArchive(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.marc")

	for _, args := range [][]string{
		{"inspect", missing},
		{"inspect", missing, "--plan-log"},
		{"verify", missing},
		{"extract", missing, "-C", t.TempDir()},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			if out, err := runCLI(t, args...); err == nil {
				t.Errorf("expected an error for a missing archive, got:\n%s", out)
			}
		})
	}
}

// TestCLICorruptArchive checks verify reports a footer mismatch (and exits
// non-zero) when archive bytes are altered after the fact.
func TestCLICorruptArchive(t *testing.T) {
	src, _ := writeCorpus(t)
	marcPath := filepath.Join(t.TempDir(), "corrupt.marc")
	mustRunCLI(t, "archive", marcPath, src)

	raw, err := os.ReadFile(marcPath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	// Flip a bit inside the blob region, past the 8-byte magic, leaving the
	// footer and catalog structurally intact so verify reaches the hash check.
	if len(raw) < 64 {
		t.Fatalf("archive unexpectedly small: %d bytes", len(raw))
	}
	raw[32] ^= 0xFF
	if err := os.WriteFile(marcPath, raw, 0o644); err != nil {
		t.Fatalf("rewrite archive: %v", err)
	}

	out, err := runCLI(t, "verify", marcPath)
	if err == nil {
		t.Fatalf("verify should fail on a corrupted archive, got:\n%s", out)
	}
	if !strings.Contains(out, "MISMATCH") && !strings.Contains(out, "failed") {
		t.Errorf("verify output should name the failure, got:\n%s", out)
	}
}

// TestCLIVersionAndTransforms covers the root command: --version, and the
// verbose form that also lists the transform registry.
func TestCLIVersionAndTransforms(t *testing.T) {
	out := mustRunCLI(t, "--version")
	if !strings.Contains(out, "metarc version") {
		t.Errorf("--version should print the version line, got:\n%s", out)
	}

	verbose := mustRunCLI(t, "--version", "-v")
	if !strings.Contains(verbose, "Transforms:") {
		t.Errorf("--version -v should list transforms, got:\n%s", verbose)
	}
	if !strings.Contains(verbose, "dedup/v1") {
		t.Errorf("transform listing should include dedup/v1, got:\n%s", verbose)
	}
	if !strings.Contains(verbose, "near-dup-delta/v1") {
		t.Errorf("transform listing should include the stub entry, got:\n%s", verbose)
	}
}

// TestCLIHelpAndUsageErrors covers the no-args help path and cobra's argument
// arity checks on each subcommand.
func TestCLIHelpAndUsageErrors(t *testing.T) {
	t.Run("bare invocation prints help", func(t *testing.T) {
		out := mustRunCLI(t)
		if !strings.Contains(out, "Available Commands") {
			t.Errorf("bare metarc should print help, got:\n%s", out)
		}
	})

	t.Run("debug flag is accepted", func(t *testing.T) {
		// --debug only reconfigures slog; assert it parses and does not fail.
		mustRunCLI(t, "--debug", "--version")
	})

	for _, args := range [][]string{
		{"archive"},
		{"archive", "only-one-arg.marc"},
		{"extract"},
		{"inspect"},
		{"verify"},
		{"bench"},
	} {
		t.Run("arity "+strings.Join(args, " "), func(t *testing.T) {
			if out, err := runCLI(t, args...); err == nil {
				t.Errorf("expected an arity error, got:\n%s", out)
			}
		})
	}
}

// TestCLIBench covers the bench subcommand in both output modes. It archives a
// real tree, but the corpus is a few KB and the whole test runs in well under a
// second, so it stays in the -short set that `make cover` measures.
func TestCLIBench(t *testing.T) {
	src, _ := writeCorpus(t)

	t.Run("human output", func(t *testing.T) {
		out := mustRunCLI(t, "bench", src)
		for _, want := range []string{"Source:", "Archive:", "Duration:", "Throughput:", "Ratio:"} {
			if !strings.Contains(out, want) {
				t.Errorf("bench output missing %q, got:\n%s", want, out)
			}
		}
		// bench always keeps the plan log, so the breakdown must be populated.
		if !strings.Contains(out, "Transform breakdown:") {
			t.Errorf("bench should print the transform breakdown, got:\n%s", out)
		}
		if !strings.Contains(out, "dedup/v1") {
			t.Errorf("breakdown should list dedup/v1, got:\n%s", out)
		}
	})

	t.Run("json output to explicit path", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "bench.marc")
		out := mustRunCLI(t, "bench", src, "--json", "--output", outPath, "--workers", "1")
		if !strings.Contains(out, "\"source_bytes\"") || !strings.Contains(out, "\"ratio_pct\"") {
			t.Errorf("bench --json should emit the report fields, got:\n%s", out)
		}
		if _, err := os.Stat(outPath); err != nil {
			t.Errorf("--output should keep the archive at %s: %v", outPath, err)
		}
	})

	// runBench's WalkDir callback swallows per-entry errors, so a missing
	// source is reported as an empty corpus rather than failing. Pinning that
	// here so a future change to the error handling is a visible decision.
	t.Run("missing source dir reports an empty corpus", func(t *testing.T) {
		out := mustRunCLI(t, "bench", filepath.Join(t.TempDir(), "absent"))
		if !strings.Contains(out, "0 files") {
			t.Errorf("expected an empty-corpus report, got:\n%s", out)
		}
	})
}

// TestFormatBytes and TestHumanBytes pin the two byte formatters, which are
// otherwise only reached through printed output.
func TestFormatBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseByteSize pins the human-readable size parser, including the
// rejection paths the archive flags rely on.
func TestParseByteSize(t *testing.T) {
	t.Parallel()

	ok := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"4MB", 4 * 1024 * 1024},
		{"16mb", 16 * 1024 * 1024},
		{" 2KB ", 2 * 1024},
		{"1GB", 1024 * 1024 * 1024},
	}
	for _, tc := range ok {
		got, err := parseByteSize(tc.in)
		if err != nil {
			t.Errorf("parseByteSize(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	for _, in := range []string{"", "   ", "abc", "0", "-5", "12XB", "1.5MB"} {
		if got, err := parseByteSize(in); err == nil {
			t.Errorf("parseByteSize(%q) = %d, want an error", in, got)
		}
	}
}

// TestGetRSSPeakKB checks the platform RSS probe returns a sane value. On
// Linux and macOS it should be positive; elsewhere the stub may return 0.
func TestGetRSSPeakKB(t *testing.T) {
	t.Parallel()
	got := getRSSPeakKB()
	if got < 0 {
		t.Errorf("getRSSPeakKB() = %d, want >= 0", got)
	}
	if got == 0 && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
		t.Errorf("getRSSPeakKB() = 0 on %s, want a positive peak", runtime.GOOS)
	}
}
