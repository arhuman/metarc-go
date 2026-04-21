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

func TestApply_MIT(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", int64(len(mitText)))
	result, err := c.Apply(ctx, e, strings.NewReader(mitText), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
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
}

func TestApply_unknown(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", 100)
	_, err := c.Apply(ctx, e, strings.NewReader("This is not a real license."), sink)
	if !errors.Is(err, marc.ErrNotApplicable) {
		t.Fatalf("expected ErrNotApplicable, got %v", err)
	}
}

func TestRoundTrip_license(t *testing.T) {
	c := NewCanonical()
	ctx := context.Background()
	sink := newFakeSink()

	e := makeEntry("LICENSE", int64(len(mitText)))
	result, err := c.Apply(ctx, e, strings.NewReader(mitText), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
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
