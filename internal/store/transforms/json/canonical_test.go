package json

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

func TestApplicable(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()

	tests := []struct {
		name string
		path string
		size int64
		want bool
	}{
		{"json file", "data.json", 100, true},
		{"JSON uppercase", "CONFIG.JSON", 100, true},
		{"js file", "app.js", 100, false},
		{"yaml file", "config.yaml", 100, false},
		{"txt file", "notes.txt", 100, false},
		{"empty json", "empty.json", 0, false},
		{"huge json", "big.json", 11 * 1024 * 1024, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := makeEntry(tt.path, tt.size)
			got := c.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q, size=%d) = %v, want %v", tt.path, tt.size, got, tt.want)
			}
		})
	}
}

func TestApply_valid(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	pretty := `{
  "name":  "metarc",
  "version": "1.0.0",
  "description": "A metacompression tool"
}`
	e := makeEntry("package.json", int64(len(pretty)))
	result, err := c.Apply(ctx, e, strings.NewReader(pretty), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(result.BlobIDs))
	}

	canonical := sink.blobs[result.BlobIDs[0]]
	if len(canonical) >= len(pretty) {
		t.Errorf("canonical (%d bytes) should be smaller than pretty (%d bytes)", len(canonical), len(pretty))
	}

	// Verify it's valid JSON.
	var v any
	if err := json.Unmarshal(canonical, &v); err != nil {
		t.Fatalf("canonical output is not valid JSON: %v", err)
	}
}

func TestApply_sortkeys(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	input := `{"b":1,"a":2}`
	e := makeEntry("data.json", int64(len(input)))
	result, err := c.Apply(ctx, e, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	canonical := string(sink.blobs[result.BlobIDs[0]])
	expected := `{"a":2,"b":1}`
	if canonical != expected {
		t.Errorf("got %q, want %q", canonical, expected)
	}
}

func TestApply_invalid(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("bad.json", 8)
	_, err := c.Apply(ctx, e, strings.NewReader("not json"), sink)
	if !errors.Is(err, marc.ErrNotApplicable) {
		t.Fatalf("expected ErrNotApplicable, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	input := `{"z": 1, "a": [1, 2, 3], "m": {"nested": true}}`
	e := makeEntry("test.json", int64(len(input)))
	result, err := c.Apply(ctx, e, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := c.Reverse(ctx, result, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	// Canonical bytes should match what was stored.
	stored := sink.blobs[result.BlobIDs[0]]
	if !bytes.Equal(buf.Bytes(), stored) {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.String(), string(stored))
	}

	// Verify semantic equality with original.
	var orig, restored any
	if err := json.Unmarshal([]byte(input), &orig); err != nil {
		t.Fatalf("unmarshal original: %v", err)
	}
	if err := json.Unmarshal(buf.Bytes(), &restored); err != nil {
		t.Fatalf("unmarshal restored: %v", err)
	}
	origJSON, _ := json.Marshal(orig)
	restoredJSON, _ := json.Marshal(restored)
	if !bytes.Equal(origJSON, restoredJSON) {
		t.Error("semantic content differs after round-trip")
	}
}
