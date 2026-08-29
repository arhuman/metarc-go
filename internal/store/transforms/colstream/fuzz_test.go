package colstream

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzColStreamRoundTrip asserts the core lossless contract: whatever encode
// accepts must decode back to the exact input bytes. encode is fuzzed rather
// than Apply because Apply's measured predicate rejects nearly every random
// input before the encoder is ever reached, which would leave the round trip
// untested.
func FuzzColStreamRoundTrip(f *testing.F) {
	f.Add(structuredLogCSV(12), ",")
	f.Add(numericCSV(12), ",")
	f.Add(strings.ReplaceAll(numericCSV(12), "\n", "\r\n"), ",")
	f.Add(strings.TrimSuffix(numericCSV(12), "\n"), ",")
	f.Add("a,b\n1,\"x,y\"\n2,\"he said \"\"hi\"\"\"\n", ",")
	f.Add("a\tb\n1\t2\n", "\t")
	f.Add("", ",")
	f.Add("\n\n\n", ",")
	f.Add("a,b,c\n1,2\n3,4,5\n", ",")

	f.Fuzz(func(t *testing.T, s, delim string) {
		if delim != "," && delim != "\t" {
			t.Skip()
		}
		comma := rune(delim[0])

		enc, ok := encode([]byte(s), comma)
		if !ok {
			return
		}
		var out bytes.Buffer
		if err := decode(enc, &out); err != nil {
			t.Fatalf("decode of an accepted encoding failed: %v\ninput: %q", err, s)
		}
		if out.String() != s {
			t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), s)
		}
	})
}

// FuzzColStreamApply drives the full Apply then Reverse path. Apply must never
// return an error for a content mismatch and must never panic; when it reports
// handled, Reverse must reproduce the input exactly.
func FuzzColStreamApply(f *testing.F) {
	f.Add(structuredLogCSV(200))
	f.Add(numericCSV(200))
	f.Add("a,b\n" + strings.Repeat("1,x\n", 100))
	f.Add(strings.Repeat("\x00", 64))
	f.Add("")

	f.Fuzz(func(t *testing.T, s string) {
		c := New()
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("fuzz.csv", int64(len(s)))

		result, handled, err := c.Apply(ctx, e, marc.Facts{Size: int64(len(s))}, strings.NewReader(s), sink)
		if err != nil {
			t.Fatalf("Apply returned an error instead of declining: %v", err)
		}
		if !handled {
			return
		}

		var out bytes.Buffer
		if err := c.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
			t.Fatalf("Reverse of a handled result failed: %v", err)
		}
		if out.String() != s {
			t.Errorf("Apply reported handled but the round trip lost data:\ngot:  %q\nwant: %q", out.String(), s)
		}
	})
}

// FuzzColStreamDecode_corrupt feeds arbitrary bytes as a blob, simulating a
// corrupted or hostile archive. decode must not panic; any error is fine.
func FuzzColStreamDecode_corrupt(f *testing.F) {
	good, _ := encode([]byte(structuredLogCSV(12)), ',')
	f.Add(good)
	if len(good) > 20 {
		f.Add(good[:20])
	}
	f.Add([]byte{})
	f.Add(blobMagic[:])
	// Oversized counts once reachable through make must be rejected, not
	// allocated.
	f.Add(append(append([]byte{}, blobMagic[:]...), ',', 0xff, 0xff, 0xff, 0xff, 0x0f))

	f.Fuzz(func(t *testing.T, blob []byte) {
		var out bytes.Buffer
		_ = decode(blob, &out) // an error is fine, a panic is not
	})
}

// FuzzQuotingOf checks the quoting recovery never claims a quoting pattern
// that does not re-render to the source line.
func FuzzQuotingOf(f *testing.F) {
	f.Add(`a,b,c`)
	f.Add(`"a","b"`)
	f.Add(`a,"b,c",d`)
	f.Add(`"he said ""hi""",x`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, line string) {
		fields := splitForFuzz(line)
		q := quotingOf(line, fields, ',')
		if q == nil {
			return
		}
		if got := render(fields, q, ','); got != line {
			t.Errorf("quotingOf accepted a pattern that does not re-render:\ngot:  %q\nwant: %q", got, line)
		}
	})
}

// splitForFuzz produces a plausible field list for a raw line without going
// through encoding/csv, so quotingOf is exercised on inputs the parser would
// never hand it as well as on ones it would.
func splitForFuzz(line string) []string {
	parts := strings.Split(line, ",")
	for i, p := range parts {
		if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
			parts[i] = strings.ReplaceAll(p[1:len(p)-1], `""`, `"`)
		}
	}
	return parts
}
