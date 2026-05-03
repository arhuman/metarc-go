package jsline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

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

func makeEntry(name string, size int64) marc.Entry {
	return marc.Entry{
		RelPath: name,
		Info:    fakeFileInfo{name: name, size: size},
	}
}

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

func TestID(t *testing.T) {
	j := NewJsLineSubst()
	if j.ID() != "js-line-subst/v1" {
		t.Errorf("ID() = %q, want %q", j.ID(), "js-line-subst/v1")
	}
}

func TestApplicable(t *testing.T) {
	j := NewJsLineSubst()
	ctx := context.Background()

	tests := []struct {
		name    string
		relPath string
		size    int64
		want    bool
	}{
		{"js file", "main.js", 1024, true},
		{"jsx file", "App.jsx", 1024, true},
		{"ts file", "main.ts", 1024, true},
		{"tsx file", "App.tsx", 1024, true},
		{"mjs file", "main.mjs", 1024, true},
		{"cjs file", "main.cjs", 1024, true},
		{"empty js file", "empty.js", 0, false},
		{"non-js file", "readme.md", 1024, false},
		{"go file", "main.go", 1024, false},
		{"py file", "main.py", 1024, false},
		{"json file", "config.json", 1024, false},
		{"js extension in dir name", "main.js/readme.txt", 1024, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := makeEntry(tt.relPath, tt.size)
			got := j.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q, size=%d) = %v, want %v", tt.relPath, tt.size, got, tt.want)
			}
		})
	}
}

func TestCostEstimate(t *testing.T) {
	j := NewJsLineSubst()
	e := makeEntry("main.ts", 10240)
	gain, cpu := j.CostEstimate(e, marc.Facts{Size: 10240})
	if gain != 1024 {
		t.Errorf("gain = %d, want 1024 (10240/10)", gain)
	}
	if cpu != 10 {
		t.Errorf("cpu = %d, want 10 (10240/1024)", cpu)
	}
}

func TestRoundTrip(t *testing.T) {
	// Mix of dictionary lines (imports, return statements, jsx) and unique
	// content. Indentation varies (2-space, tab, none).
	input := "'use strict';\nconst path = require('path');\n\nfunction main() {\n\tif (err) {\n\t\treturn null;\n\t}\n\treturn true;\n}\n\nmodule.exports = {\n\tmain,\n};\n"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("main.js", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 BlobID, got %d", len(result.BlobIDs))
	}

	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Contains(blob, []byte{0x00}) {
		t.Error("expected blob to contain \\x00 substitution tokens")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := j.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_typescript(t *testing.T) {
	input := "import React, { useState } from 'react';\n\nexport interface Props {\n  name: string;\n}\n\nexport function App(props: Props) {\n  return (\n    <div>{props.name}</div>\n  );\n}\n"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("App.tsx", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := j.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_noMatches(t *testing.T) {
	input := "var x = 42;\nvar y = \"hello\";\nconsole.log(x + y);\n"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("foo.js", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Equal(blob, []byte(input)) {
		t.Error("expected blob to equal input when no substitutions occur")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := j.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_allMatches(t *testing.T) {
	input := "'use strict';\nreturn null;\nreturn true;\nbreak;\ncontinue;\n"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("all.js", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blob := sink.blobs[result.BlobIDs[0]]
	if len(blob) >= len(input) {
		t.Errorf("expected blob (%d bytes) to be smaller than input (%d bytes)", len(blob), len(input))
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := j.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestNULByte(t *testing.T) {
	input := "var x = \"hello\x00world\";\n"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("nul.js", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	_, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Error("expected handled=false for content with NUL byte")
	}
}

func TestNoTrailingNewline(t *testing.T) {
	input := "'use strict';\nreturn null;"

	j := NewJsLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("notrail.js", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := j.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestDictionaryParsing(t *testing.T) {
	if len(dict) == 0 {
		t.Fatal("dictionary is empty")
	}

	for _, want := range []string{`'use strict';`, `return null;`, `return true;`, `module.exports = {`} {
		if _, ok := dictLookup[want]; !ok {
			t.Errorf("dictLookup missing expected entry %q", want)
		}
	}

	if len(dictLookup) != len(dict) {
		t.Errorf("dictLookup has %d entries, dict has %d (suggests a duplicate in dict)", len(dictLookup), len(dict))
	}

	for i, s := range dict {
		encoded := encodeIndex(i)
		if idx, ok := dictLookup[s]; !ok || idx != encoded {
			t.Errorf("dictLookup[%q] = (%d, %v), want (%d, true)", s, idx, ok, encoded)
		}
		if decoded := decodeIndex(encoded); decoded != i {
			t.Errorf("decodeIndex(encodeIndex(%d)) = %d", i, decoded)
		}
	}

	for i := range dict {
		b := encodeIndex(i)
		if b == 0x00 {
			t.Errorf("encodeIndex(%d) = 0x00, conflicts with marker byte", i)
		}
		if b == 0x0a {
			t.Errorf("encodeIndex(%d) = 0x0a, conflicts with newline", i)
		}
	}
}

// FuzzRoundTrip checks that random JS/TS-ish content survives a round-trip.
func FuzzRoundTrip(f *testing.F) {
	f.Add("'use strict';\nreturn null;\n")
	f.Add("import React from 'react';\n")
	f.Add("")
	f.Add("// comment\n")
	f.Add("export function foo() {\n  return true;\n}\n")
	f.Add("const path = require('path');\n")

	j := NewJsLineSubst()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, input string) {
		for i := range input {
			if input[i] == 0x00 {
				return
			}
		}

		sink := newFakeSink()
		e := makeEntry("fuzz.ts", int64(len(input)))
		facts := marc.Facts{Size: int64(len(input))}
		result, handled, err := j.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !handled {
			t.Fatalf("expected handled=true for NUL-free input")
		}

		blobs := &fakeBlobs{blobs: sink.blobs}
		var out bytes.Buffer
		if err := j.Reverse(ctx, result, blobs, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if out.String() != input {
			t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
		}
	})
}
