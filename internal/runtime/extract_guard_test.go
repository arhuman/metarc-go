package runtime

// TODO: uncomment and validate — added by an agent on 2026-08-17 alongside the
// traversal-guard fix. The table encodes which entry names extract accepts and
// which it refuses; the repository owner should confirm the contract.

import (
	"path/filepath"
	"testing"
)

// TestSafeJoin checks the extraction traversal guard against both the default
// relative destination and an absolute one: a legitimate entry must resolve,
// and any name that would land outside the destination must be refused.
func TestSafeJoin(t *testing.T) {
	absDest, err := filepath.Abs("out")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		relPath string
		want    string // expected result, empty means "must be rejected"
	}{
		{"plain file", "a.txt", filepath.Join(absDest, "a.txt")},
		{"nested file", "sub/inner.txt", filepath.Join(absDest, "sub", "inner.txt")},
		{"harmless dots in name", "a..b.txt", filepath.Join(absDest, "a..b.txt")},
		{"interior traversal stays inside", "sub/../a.txt", filepath.Join(absDest, "a.txt")},
		{"leading traversal", "../evil.txt", ""},
		{"deep traversal", "sub/../../evil.txt", ""},
		{"bare traversal", "..", ""},
		{"absolute name", "/etc/passwd", ""},
		{"destination itself", ".", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeJoin(absDest, tc.relPath)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("safeJoin(%q) = %q, want rejection", tc.relPath, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeJoin(%q): unexpected error: %v", tc.relPath, err)
			}
			if got != tc.want {
				t.Fatalf("safeJoin(%q) = %q, want %q", tc.relPath, got, tc.want)
			}
		})
	}
}
