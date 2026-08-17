package license

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzCanonicalRoundTrip checks that any input recognized by Apply (handled=true)
// round-trips byte-exactly through Reverse. normalize() converts CRLF to LF
// before matching, so the expected output is the CRLF-normalized input, matching
// TestRoundTrip_preservesExactWhitespace's convention.
func FuzzCanonicalRoundTrip(f *testing.F) {
	c := NewCanonical()

	mitWithCopyright := makeLicenseWithCopyright(mitText, "Copyright (c) 2024 Google LLC")
	f.Add(mitWithCopyright)
	f.Add(mitWithCopyright + "\n\n")
	f.Add("\n\n" + mitWithCopyright + "\n")
	f.Add(strings.Replace(mitWithCopyright, "Software", "SOFTWARE PACKAGE", 1))
	f.Add("This is not a real license.")
	f.Add("")

	f.Fuzz(func(t *testing.T, s string) {
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("LICENSE", int64(len(s)))
		facts := marc.Facts{Size: int64(len(s))}
		result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(s), sink)
		if err != nil || !handled {
			return
		}

		var buf bytes.Buffer
		if err := c.Reverse(ctx, result, nil, &buf); err != nil {
			t.Fatalf("Reverse: %v", err)
		}

		want := strings.ReplaceAll(s, "\r\n", "\n")
		if buf.String() != want {
			t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.String(), want)
		}
	})
}

// FuzzCanonicalReverse_corrupt feeds arbitrary bytes as both Result.Params and
// the backing blob content, simulating a corrupted or maliciously crafted
// archive. Reverse must not panic; any returned error is acceptable.
func FuzzCanonicalReverse_corrupt(f *testing.F) {
	c := NewCanonical()

	f.Add([]byte(`{"spdx":"MIT"}`), []byte(""))
	f.Add([]byte(`{"spdx":"unknown"}`), []byte(""))
	f.Add([]byte(`not json`), []byte("garbage"))
	f.Add([]byte(``), []byte(``))
	f.Add([]byte(`{"spdx":"MIT","ops":[{"k":"=","n":1}]}`), []byte(""))
	// Regression: negative or oversized equal counts once panicked in expandOps.
	f.Add([]byte(`{"spdx":"MIT","ops":[{"k":"=","n":-1}]}`), []byte(""))
	f.Add([]byte(`{"spdx":"MIT","ops":[{"k":"=","n":9223372036854775807}]}`), []byte(""))

	f.Fuzz(func(t *testing.T, paramsBytes, blobBytes []byte) {
		blobs := &fakeBlobs{blobs: map[marc.BlobID][]byte{1: blobBytes}}
		ctx := context.Background()
		var buf bytes.Buffer

		// Legacy path: template text comes from the blob.
		result := marc.Result{BlobIDs: []marc.BlobID{1}, Params: paramsBytes}
		_ = c.Reverse(ctx, result, blobs, &buf) // error is fine, panic is not

		// Embedded-template path: no blob, template resolved via spdxIndex.
		buf.Reset()
		_ = c.Reverse(ctx, marc.Result{Params: paramsBytes}, blobs, &buf)
	})
}
