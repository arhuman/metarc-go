package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCountFile covers the line-frequency accumulator: leading whitespace is
// stripped, lines of 5 characters or fewer are ignored, and counts accumulate
// across calls so a shared map totals a whole corpus.
func TestCountFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	// "short" (5 chars) is at the boundary and must be excluded; the indented
	// forms of "func main() {" must fold into the same key as the bare one.
	first := write("a.go", strings.Join([]string{
		"func main() {",
		"\tfunc main() {",
		"    func main() {",
		"short",
		"tiny",
		"",
		"unique line here",
	}, "\n"))

	second := write("b.go", "func main() {\nanother unique line\n")

	freq := make(map[string]int)
	countFile(first, freq)

	if got := freq["func main() {"]; got != 3 {
		t.Errorf("after one file: indented variants should fold, got %d, want 3", got)
	}
	for _, excluded := range []string{"short", "tiny", ""} {
		if _, ok := freq[excluded]; ok {
			t.Errorf("line %q is <= 5 chars and should be excluded", excluded)
		}
	}
	if got := freq["unique line here"]; got != 1 {
		t.Errorf("unique line: got %d, want 1", got)
	}

	countFile(second, freq)
	if got := freq["func main() {"]; got != 4 {
		t.Errorf("counts should accumulate across files, got %d, want 4", got)
	}
	if got := freq["another unique line"]; got != 1 {
		t.Errorf("second file's line: got %d, want 1", got)
	}
}

// TestCountFileUnreadable checks countFile treats an unopenable path as a
// no-op rather than panicking: main relies on this to skip bad entries.
func TestCountFileUnreadable(t *testing.T) {
	t.Parallel()

	freq := make(map[string]int)
	countFile(filepath.Join(t.TempDir(), "does-not-exist.go"), freq)
	if len(freq) != 0 {
		t.Errorf("missing file should leave the map untouched, got %v", freq)
	}
}

// TestCountFileLongLines checks a line beyond the scanner's 1 MiB buffer is
// dropped without corrupting the counts for the lines around it.
func TestCountFileLongLines(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "long.txt")
	body := "leading real line\n" + strings.Repeat("x", 2*1024*1024) + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	freq := make(map[string]int)
	countFile(p, freq)

	if got := freq["leading real line"]; got != 1 {
		t.Errorf("line before the oversized one: got %d, want 1", got)
	}
	if len(freq) != 1 {
		t.Errorf("oversized line should be dropped, got %d keys: %v", len(freq), freq)
	}
}
