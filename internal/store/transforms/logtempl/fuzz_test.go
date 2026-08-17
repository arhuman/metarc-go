package logtempl

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzTemplate_NoPanic checks that Apply/Reverse never panic on arbitrary
// input. The transform is lossy by design (see findCommonPrefix), so this
// does not assert byte equality, only that a handled result reverses cleanly.
func FuzzTemplate_NoPanic(f *testing.F) {
	tmpl := NewTemplate()

	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("2024-01-15 10:30:%02d INFO server request handled path=/api/v1/users id=%d", i%60, i))
	}
	f.Add(strings.Join(lines, "\n") + "\n")
	f.Add("")
	f.Add("no\ncommon\nprefix\nat\nall\nbetween\nany\nof\nthese\nlines\n")
	f.Add(strings.Repeat("x\n", 20))

	f.Fuzz(func(t *testing.T, s string) {
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("app.log", int64(len(s)))
		facts := marc.Facts{Size: int64(len(s))}
		result, handled, err := tmpl.Apply(ctx, e, facts, strings.NewReader(s), sink)
		if err != nil || !handled {
			return
		}

		var out bytes.Buffer
		if err := tmpl.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
	})
}
