package goline

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

func makeEntry(name string, size int64) marc.Entry {
	return marc.Entry{
		RelPath: name,
		Info:    fakeFileInfo{name: name, size: size},
	}
}

// fakeSink is a simple in-memory BlobSink.
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

// fakeBlobs implements marc.BlobReader backed by fakeSink.
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
	g := NewGoLineSubst()
	if g.ID() != "go-line-subst/v1" {
		t.Errorf("ID() = %q, want %q", g.ID(), "go-line-subst/v1")
	}
}

func TestApplicable(t *testing.T) {
	g := NewGoLineSubst()
	ctx := context.Background()

	tests := []struct {
		name    string
		relPath string
		size    int64
		want    bool
	}{
		{"go file with content", "main.go", 1024, true},
		{"go file in subdir", "pkg/utils/helper.go", 512, true},
		{"empty go file", "empty.go", 0, false},
		{"non-go file", "readme.md", 1024, false},
		{"json file", "config.json", 1024, false},
		{"go extension in dir name", "main.go/readme.txt", 1024, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := makeEntry(tt.relPath, tt.size)
			got := g.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q, size=%d) = %v, want %v", tt.relPath, tt.size, got, tt.want)
			}
		})
	}
}

func TestCostEstimate(t *testing.T) {
	g := NewGoLineSubst()
	e := makeEntry("main.go", 10240)
	gain, cpu := g.CostEstimate(e, marc.Facts{Size: 10240})
	if gain != 1024 {
		t.Errorf("gain = %d, want 1024 (10240/10)", gain)
	}
	if cpu != 10 {
		t.Errorf("cpu = %d, want 10 (10240/1024)", cpu)
	}
}

func TestRoundTrip(t *testing.T) {
	// Content with dictionary lines at various indentation levels.
	input := "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {\n\tx, err := doSomething()\n\tif err != nil {\n\t\treturn\n\t}\n\tfmt.Println(x)\n}\n"

	g := NewGoLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("main.go", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := g.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 BlobID, got %d", len(result.BlobIDs))
	}

	// Verify the blob contains substitution tokens.
	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Contains(blob, []byte{0x00}) {
		t.Error("expected blob to contain \\x00 substitution tokens")
	}

	// Reverse and verify byte-identical output.
	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := g.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_noMatches(t *testing.T) {
	// Content with no dictionary lines.
	input := "package foo\n\nvar x = 42\nvar y = \"hello\"\n"

	g := NewGoLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("foo.go", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := g.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	// Blob should be identical to input (no substitutions).
	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Equal(blob, []byte(input)) {
		t.Error("expected blob to equal input when no substitutions occur")
	}

	// Round-trip should still work.
	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := g.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_allMatches(t *testing.T) {
	// Content where every line is a dictionary entry.
	input := "import (\nreturn nil\nreturn err\ncontinue\ndefault:\n"

	g := NewGoLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("all.go", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := g.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	// Every line should be substituted.
	blob := sink.blobs[result.BlobIDs[0]]
	if len(blob) >= len(input) {
		t.Errorf("expected blob (%d bytes) to be smaller than input (%d bytes)", len(blob), len(input))
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := g.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestNULByte(t *testing.T) {
	// Content with a NUL byte should return handled=false.
	input := "package main\n\nvar x = \"hello\x00world\"\n"

	g := NewGoLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("nul.go", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	_, handled, err := g.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Error("expected handled=false for content with NUL byte")
	}
}

func TestNoTrailingNewline(t *testing.T) {
	// Content without a final newline.
	input := "package main\n\nfunc main() {\n\treturn\n}"

	g := NewGoLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("notrail.go", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := g.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := g.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestDictionaryParsing(t *testing.T) {
	if len(dict) != 105 {
		t.Errorf("dictionary has %d entries, want 105", len(dict))
	}

	// Spot-check key entries.
	// Verify key entries exist in the lookup.
	for _, want := range []string{"if err != nil {", "return nil", "import ("} {
		if _, ok := dictLookup[want]; !ok {
			t.Errorf("dictLookup missing expected entry %q", want)
		}
	}

	// Verify lookup map is consistent with the encode/decode cycle.
	if len(dictLookup) != len(dict) {
		t.Errorf("dictLookup has %d entries, dict has %d", len(dictLookup), len(dict))
	}
	for i, s := range dict {
		encoded := encodeIndex(i)
		if idx, ok := dictLookup[s]; !ok || idx != encoded {
			t.Errorf("dictLookup[%q] = (%d, %v), want (%d, true)", s, idx, ok, encoded)
		}
		// Verify round-trip of encode/decode.
		if decoded := decodeIndex(encoded); decoded != i {
			t.Errorf("decodeIndex(encodeIndex(%d)) = %d", i, decoded)
		}
	}

	// Verify no encoded byte is 0x00 or 0x0a (would conflict with marker/newline).
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
