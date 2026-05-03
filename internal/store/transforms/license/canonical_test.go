package license

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// fakeFileInfo implements fs.FileInfo for testing.
type fakeFileInfo struct {
	name string
	size int64
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func makeEntry(relPath string, size int64) marc.Entry {
	return marc.Entry{
		RelPath: relPath,
		Info:    fakeFileInfo{name: relPath, size: size},
	}
}

// fakeSink records written blobs in memory.
type fakeSink struct {
	blobs map[marc.BlobID][]byte
	next  marc.BlobID
}

func newFakeSink() *fakeSink {
	return &fakeSink{blobs: make(map[marc.BlobID][]byte), next: 1}
}

func (s *fakeSink) Write(_ context.Context, r io.Reader) (marc.BlobID, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	id := s.next
	s.blobs[id] = data
	s.next++
	return id, nil
}

func (s *fakeSink) Reuse(_ [32]byte) (marc.BlobID, bool) { return 0, false }

// fakeBlobs implements marc.BlobReader backed by fakeSink data.
type fakeBlobs struct {
	blobs map[marc.BlobID][]byte
}

func (b *fakeBlobs) Open(id marc.BlobID) (io.ReadCloser, error) {
	data, ok := b.blobs[id]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestApplicable_match(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	facts := marc.Facts{Size: 1000}

	cases := []string{
		"LICENSE",
		"COPYING",
		"NOTICE",
		"license.txt",
		"License.md",
		"LICENCE",
		"licence",
		"COPYING.txt",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			e := makeEntry(name, 1000)
			if !c.Applicable(ctx, e, facts) {
				t.Errorf("expected Applicable=true for %q", name)
			}
		})
	}
}

func TestApplicable_nomatch(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	facts := marc.Facts{Size: 1000}

	cases := []string{
		"main.go",
		"README.md",
		"Makefile",
		"go.mod",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			e := makeEntry(name, 1000)
			if c.Applicable(ctx, e, facts) {
				t.Errorf("expected Applicable=false for %q", name)
			}
		})
	}
}

func TestFingerprints_loaded(t *testing.T) {
	count := FingerprintCount()
	if count < 3 {
		t.Fatalf("expected at least 3 fingerprints, got %d", count)
	}
}

func TestBodyFingerprints_loaded(t *testing.T) {
	count := BodyFingerprintCount()
	if count < 3 {
		t.Fatalf("expected at least 3 body fingerprints, got %d", count)
	}
}

func TestApply_MIT(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", int64(len(mitText)))
	facts := marc.Facts{Size: int64(len(mitText))}
	result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(mitText), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for known license")
	}

	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(result.BlobIDs))
	}

	var p params
	if err := json.Unmarshal(result.Params, &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p.SPDX != "MIT" {
		t.Fatalf("expected SPDX=MIT, got %q", p.SPDX)
	}
	if len(p.Ops) != 0 {
		t.Fatalf("expected empty Ops for exact match, got %d ops", len(p.Ops))
	}
}

func TestApply_unknown(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", 100)
	facts := marc.Facts{Size: 100}
	_, handled, err := c.Apply(ctx, e, facts, strings.NewReader("This is not a real license."), sink)
	if err != nil {
		t.Fatalf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Fatal("expected handled=false for unknown license")
	}
}

func TestRoundTrip_license(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", int64(len(mitText)))
	facts := marc.Facts{Size: int64(len(mitText))}
	result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(mitText), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := c.Reverse(ctx, result, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	canonical := normalize(mitText)
	if buf.String() != canonical {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.String()[:50], canonical[:50])
	}
}

// makeLicenseWithCopyright replaces the placeholder copyright line in a
// template with a real copyright line.
func makeLicenseWithCopyright(template, copyright string) string {
	lines := strings.Split(normalize(template), "\n")
	for i, l := range lines {
		if copyrightRe.MatchString(l) {
			lines[i] = copyright
			return strings.Join(lines, "\n")
		}
	}
	// If no copyright line found (shouldn't happen for our templates),
	// just prepend it.
	return copyright + "\n" + normalize(template)
}

func TestApply_MIT_realCopyright(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	input := makeLicenseWithCopyright(mitText, "Copyright (c) 2024 Google LLC")
	e := makeEntry("LICENSE", int64(len(input)))
	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for MIT with real copyright")
	}

	var p params
	if err := json.Unmarshal(result.Params, &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p.SPDX != "MIT" {
		t.Fatalf("expected SPDX=MIT, got %q", p.SPDX)
	}
	if len(p.Ops) == 0 {
		t.Fatal("expected non-empty Ops for real copyright")
	}
}

func TestRoundTrip_MIT_realCopyright(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	input := makeLicenseWithCopyright(mitText, "Copyright (c) 2024 Google LLC")
	e := makeEntry("LICENSE", int64(len(input)))
	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := c.Reverse(ctx, result, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if buf.String() != input {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", truncate(buf.String(), 80), truncate(input, 80))
	}
}

func TestRoundTrip_AllLicenses(t *testing.T) {
	copyrights := map[string]string{
		"MIT":          "Copyright (c) 2023 Acme Corp",
		"Apache-2.0":   "   Copyright 2023 Acme Corp",
		"BSD-2-Clause": "Copyright (c) 2022 Jane Doe",
		"BSD-3-Clause": "Copyright (c) 2021 John Smith",
		"ISC":          "Copyright (c) 2020 Open Source Foundation",
	}

	for _, tmpl := range canonicalTexts {
		t.Run(tmpl.SPDX, func(t *testing.T) {
			c := NewCanonical()
			ctx := context.Background()
			sink := newFakeSink()

			cr := copyrights[tmpl.SPDX]
			input := makeLicenseWithCopyright(tmpl.Text, cr)
			e := makeEntry("LICENSE", int64(len(input)))
			facts := marc.Facts{Size: int64(len(input))}

			result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(input), sink)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !handled {
				t.Fatalf("expected handled=true for %s", tmpl.SPDX)
			}

			var p params
			if err := json.Unmarshal(result.Params, &p); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			if p.SPDX != tmpl.SPDX {
				t.Fatalf("expected SPDX=%s, got %q", tmpl.SPDX, p.SPDX)
			}

			blobs := &fakeBlobs{blobs: sink.blobs}
			var buf bytes.Buffer
			if err := c.Reverse(ctx, result, blobs, &buf); err != nil {
				t.Fatalf("Reverse: %v", err)
			}

			if buf.String() != input {
				t.Fatalf("round-trip mismatch for %s:\ngot:  %q\nwant: %q",
					tmpl.SPDX, truncate(buf.String(), 80), truncate(input, 80))
			}
		})
	}
}

func TestApply_diffTooLarge(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	// Build a license that has the same body but an enormous copyright block.
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("This is not a real license text line number ")
		sb.WriteString(strings.Repeat("X", 20))
		sb.WriteString("\n")
	}
	input := sb.String()

	e := makeEntry("LICENSE", int64(len(input)))
	facts := marc.Facts{Size: int64(len(input))}
	_, handled, err := c.Apply(ctx, e, facts, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Fatal("expected handled=false for heavily modified license")
	}
}

func TestParamsSize(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	input := makeLicenseWithCopyright(mitText, "Copyright (c) 2024 Google LLC, All Rights Reserved")
	e := makeEntry("LICENSE", int64(len(input)))
	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	if len(result.Params) > maxParamsBytes {
		t.Fatalf("params size %d exceeds limit %d", len(result.Params), maxParamsBytes)
	}
	t.Logf("params size: %d bytes (JSON: %s)", len(result.Params), string(result.Params))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestRoundTrip_preservesExactWhitespace covers the regression where
// licenses with trailing-empty-line(s), CRLF endings, or leading whitespace
// silently lost bytes through the normalize→diff→reverse path. The bug was
// surfaced by vendor/github.com/x448/float16/LICENSE in kubernetes (1102
// bytes ending "SOFTWARE.\n\n", coming back as 1101 bytes "SOFTWARE.\n").
func TestRoundTrip_preservesExactWhitespace(t *testing.T) {
	mitWithRealCopyright := makeLicenseWithCopyright(mitText, "Copyright (c) 2024 Google LLC")

	cases := []struct {
		name string
		in   string
	}{
		{"trailing empty line", mitWithRealCopyright + "\n\n"},
		{"trailing 3 newlines", mitWithRealCopyright + "\n\n\n"},
		{"trailing whitespace", mitWithRealCopyright + "\n   \t\n"},
		{"leading whitespace", "\n\n" + mitWithRealCopyright + "\n"},
		{"CRLF line endings", strings.ReplaceAll(mitWithRealCopyright, "\n", "\r\n") + "\r\n"},
		{"no trailing newline", mitWithRealCopyright},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCanonical()
			ctx := context.Background()
			sink := newFakeSink()

			e := makeEntry("LICENSE", int64(len(tc.in)))
			facts := marc.Facts{Size: int64(len(tc.in))}
			result, handled, err := c.Apply(ctx, e, facts, strings.NewReader(tc.in), sink)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !handled {
				t.Fatalf("expected handled=true")
			}

			blobs := &fakeBlobs{blobs: sink.blobs}
			var buf bytes.Buffer
			if err := c.Reverse(ctx, result, blobs, &buf); err != nil {
				t.Fatalf("Reverse: %v", err)
			}

			// CRLF case: normalize() converts CRLF→LF before storing, so
			// reverse produces LF-only output. Compare against the
			// LF-normalized input rather than the literal input.
			want := strings.ReplaceAll(tc.in, "\r\n", "\n")
			got := buf.String()
			if got != want {
				t.Fatalf("round-trip mismatch (%d → %d bytes):\nwant: %q\n got: %q",
					len(tc.in), len(got), truncate(want, 80), truncate(got, 80))
			}
		})
	}
}

// TestReverse_legacyTrailingNL exercises the back-compat path: archives
// produced by metarc versions before the trailing-whitespace fix wrote
// {"nl": true} and expect exactly one trailing "\n" on reverse.
func TestReverse_legacyTrailingNL(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()

	// Hand-craft a legacy params record (no Leading/Trailing fields).
	legacy := params{SPDX: "MIT", TrailingNL: true}
	paramsJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Build a fake blob containing the canonical MIT text.
	blob := []byte(normalize(mitText))
	blobs := &fakeBlobs{blobs: map[marc.BlobID][]byte{1: blob}}

	r := marc.Result{BlobIDs: []marc.BlobID{1}, Params: paramsJSON}
	var buf bytes.Buffer
	if err := c.Reverse(ctx, r, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	want := normalize(mitText) + "\n"
	if buf.String() != want {
		t.Fatalf("legacy reverse: got %q, want %q",
			truncate(buf.String(), 80), truncate(want, 80))
	}
}
