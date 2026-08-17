package goline

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzRoundTrip checks that Apply followed by Reverse reproduces the input
// byte-exactly for any content without NUL bytes, using the real dictionary.
func FuzzRoundTrip(f *testing.F) {
	g := NewGoLineSubst()

	f.Add("package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {\n\tif err != nil {\n\t\treturn nil\n\t}\n}\n")
	f.Add("\tif err != nil {\n\t\treturn err\n\t}\n")
	f.Add("    if err != nil {\n        return nil\n    }\n")
	f.Add("")
	f.Add("var x = 1\nfmt.Println(x)")
	f.Add("if err != nil {\nreturn nil")

	f.Fuzz(func(t *testing.T, s string) {
		if strings.ContainsRune(s, 0x00) {
			return
		}
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("x.go", int64(len(s)))
		result, handled, err := g.Apply(ctx, e, marc.Facts{Size: int64(len(s))}, strings.NewReader(s), sink)
		if err != nil || !handled {
			return
		}
		var out bytes.Buffer
		if err := g.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if out.String() != s {
			t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", out.String(), s)
		}
	})
}
