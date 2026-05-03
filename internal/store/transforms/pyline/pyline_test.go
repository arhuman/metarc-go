package pyline

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
	p := NewPyLineSubst()
	if p.ID() != "py-line-subst/v1" {
		t.Errorf("ID() = %q, want %q", p.ID(), "py-line-subst/v1")
	}
}

func TestApplicable(t *testing.T) {
	p := NewPyLineSubst()
	ctx := context.Background()

	tests := []struct {
		name    string
		relPath string
		size    int64
		want    bool
	}{
		{"py file with content", "main.py", 1024, true},
		{"py file in subdir", "pkg/utils/helper.py", 512, true},
		{"empty py file", "empty.py", 0, false},
		{"non-py file", "readme.md", 1024, false},
		{"go file", "main.go", 1024, false},
		{"pyx file", "speedup.pyx", 1024, false},
		{"py extension in dir name", "main.py/readme.txt", 1024, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := makeEntry(tt.relPath, tt.size)
			got := p.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q, size=%d) = %v, want %v", tt.relPath, tt.size, got, tt.want)
			}
		})
	}
}

func TestCostEstimate(t *testing.T) {
	p := NewPyLineSubst()
	e := makeEntry("main.py", 10240)
	gain, cpu := p.CostEstimate(e, marc.Facts{Size: 10240})
	if gain != 1024 {
		t.Errorf("gain = %d, want 1024 (10240/10)", gain)
	}
	if cpu != 10 {
		t.Errorf("cpu = %d, want 10 (10240/1024)", cpu)
	}
}

func TestRoundTrip(t *testing.T) {
	// Content with dictionary lines at various indentation levels.
	input := "import os\nimport sys\n\nfrom typing import Optional\n\nclass Foo:\n    def __init__(self):\n        pass\n\n    def __repr__(self):\n        return f\"Foo({self._value})\"\n\n\nif __name__ == \"__main__\":\n    Foo()\n"

	p := NewPyLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("main.py", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
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
	if err := p.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_noMatches(t *testing.T) {
	input := "x = 42\ny = \"hello\"\nz = some_function()\n"

	p := NewPyLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("foo.py", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
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
	if err := p.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestRoundTrip_allMatches(t *testing.T) {
	// Content where every line is a dictionary entry.
	input := "import os\nimport sys\nimport json\npass\nreturn None\n"

	p := NewPyLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("all.py", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
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
	if err := p.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != input {
		t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
	}
}

func TestNULByte(t *testing.T) {
	input := "x = \"hello\x00world\"\n"

	p := NewPyLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("nul.py", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	_, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Error("expected handled=false for content with NUL byte")
	}
}

func TestNoTrailingNewline(t *testing.T) {
	input := "import os\nimport sys\npass"

	p := NewPyLineSubst()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("notrail.py", int64(len(input)))

	facts := marc.Facts{Size: int64(len(input))}
	result, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := p.Reverse(ctx, result, blobs, &out); err != nil {
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

	for _, want := range []string{`import os`, `pass`, `return None`, `if __name__ == "__main__":`} {
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

// FuzzRoundTrip checks that random Python-ish content survives a round-trip.
func FuzzRoundTrip(f *testing.F) {
	f.Add("import os\npass\n")
	f.Add("class Foo:\n    pass\n")
	f.Add("")
	f.Add("# comment\n")
	f.Add("def __init__(self):\n    self.x = 1\n")

	p := NewPyLineSubst()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, input string) {
		// NUL bytes trigger handled=false; skip those — fuzz is for the
		// happy path where Apply returns handled=true.
		for i := range input {
			if input[i] == 0x00 {
				return
			}
		}

		sink := newFakeSink()
		e := makeEntry("fuzz.py", int64(len(input)))
		facts := marc.Facts{Size: int64(len(input))}
		result, handled, err := p.Apply(ctx, e, facts, bytes.NewReader([]byte(input)), sink)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !handled {
			t.Fatalf("expected handled=true for NUL-free input")
		}

		blobs := &fakeBlobs{blobs: sink.blobs}
		var out bytes.Buffer
		if err := p.Reverse(ctx, result, blobs, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if out.String() != input {
			t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", out.String(), input)
		}
	})
}
