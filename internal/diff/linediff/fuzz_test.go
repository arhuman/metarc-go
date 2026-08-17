package linediff

import (
	"strings"
	"testing"
)

// FuzzDiffApply checks the core guarantee of the package: Apply(a, Diff(a, b)) == b
// for any pair of line slices, including degenerate splits of "" and inputs with
// no shared lines at all.
func FuzzDiffApply(f *testing.F) {
	f.Add("", "")
	f.Add("hello", "")
	f.Add("", "hello")
	f.Add("hello\nworld", "hello\nworld")
	f.Add("a\nb\nc", "x\ny\nz")
	f.Add("a\nb\nc\nd\ne", "a\nB\nc\nD\ne")

	f.Fuzz(func(t *testing.T, sa, sb string) {
		a := strings.Split(sa, "\n")
		b := strings.Split(sb, "\n")

		ops := Diff(a, b)
		got, err := Apply(a, ops)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		// Join normalizes the nil-vs-empty-slice distinction Apply can produce
		// for an empty result, since no line here can contain "\n" (they came
		// from splitting on it).
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", got, b)
		}
	})
}
