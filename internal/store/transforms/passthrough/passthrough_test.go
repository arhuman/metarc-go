package passthrough

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// --- minimal fakes for unit tests ---

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
		Info:    fakeFileInfo{name: filepath.Base(relPath), size: size},
	}
}

// fakeSink is a BlobSink without WriteRaw — exercises the fallback path.
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

// rawSink is a BlobSink that DOES implement WriteRaw — exercises the
// happy path of the type assertion.
type rawSink struct {
	*fakeSink
	rawCalls int
}

func newRawSink() *rawSink {
	return &rawSink{fakeSink: newFakeSink()}
}

func (s *rawSink) WriteRaw(_ context.Context, r io.Reader) (marc.BlobID, error) {
	s.rawCalls++
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	id := s.next
	s.blobs[id] = data
	s.next++
	return id, nil
}

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

// --- ID / Applicable ---

func TestID(t *testing.T) {
	if New().ID() != "passthrough/v1" {
		t.Fatalf("unexpected ID: %q", New().ID())
	}
}

func TestApplicable_match(t *testing.T) {
	p := New()
	ctx := context.Background()
	facts := marc.Facts{Size: 1024}

	cases := []string{
		"image.png",
		"photo.JPG", // case-insensitive
		"clip.MP4",  // case-insensitive
		"font.woff2",
		"bundle.zip",
		"archive.tar.gz", // matches on final extension only (.gz)
		"music.flac",
		"icon.ICO",
		"doc.pdf",
		"path/to/asset.webp",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if !p.Applicable(ctx, makeEntry(name, 1024), facts) {
				t.Errorf("expected Applicable=true for %q", name)
			}
		})
	}
}

func TestApplicable_nomatch(t *testing.T) {
	p := New()
	ctx := context.Background()
	facts := marc.Facts{Size: 1024}

	cases := []string{
		"main.go",
		"README.md",
		"Makefile",
		"data.json",
		"style.css",
		"archive.tar", // .tar alone is uncompressed
		"plain.txt",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if p.Applicable(ctx, makeEntry(name, 1024), facts) {
				t.Errorf("expected Applicable=false for %q", name)
			}
		})
	}
}

func TestApplicable_emptyFile(t *testing.T) {
	p := New()
	ctx := context.Background()
	if p.Applicable(ctx, makeEntry("image.png", 0), marc.Facts{Size: 0}) {
		t.Fatal("expected Applicable=false for empty file")
	}
}

// --- CostEstimate ---

func TestCostEstimate(t *testing.T) {
	p := New()
	gain, cpu := p.CostEstimate(makeEntry("image.png", 100_000), marc.Facts{Size: 100_000})
	if gain <= cpu {
		t.Fatalf("planner requires gain (%d) > cpu (%d)", gain, cpu)
	}
}

// --- Apply: WriteRaw path ---

func TestApply_usesWriteRawWhenAvailable(t *testing.T) {
	p := New()
	ctx := context.Background()
	sink := newRawSink()

	body := []byte("any bytes — content doesn't matter for this test")
	res, handled, err := p.Apply(ctx, makeEntry("blob.zip", int64(len(body))), marc.Facts{Size: int64(len(body))}, bytes.NewReader(body), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if sink.rawCalls != 1 {
		t.Fatalf("expected exactly 1 WriteRaw call, got %d", sink.rawCalls)
	}
	if got, want := len(res.BlobIDs), 1; got != want {
		t.Fatalf("BlobIDs len=%d, want %d", got, want)
	}
}

// --- Apply: fallback path when sink doesn't implement WriteRaw ---

func TestApply_fallsBackToWrite(t *testing.T) {
	p := New()
	ctx := context.Background()
	sink := newFakeSink() // no WriteRaw

	body := []byte("bytes for the fallback path")
	res, handled, err := p.Apply(ctx, makeEntry("blob.zip", int64(len(body))), marc.Facts{Size: int64(len(body))}, bytes.NewReader(body), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true even on fallback")
	}
	if got, want := len(res.BlobIDs), 1; got != want {
		t.Fatalf("BlobIDs len=%d, want %d", got, want)
	}
	if !bytes.Equal(sink.blobs[res.BlobIDs[0]], body) {
		t.Fatal("fallback Write should have stored exact bytes")
	}
}

// --- Reverse round-trip via the fakes ---

func TestReverse_roundTrip(t *testing.T) {
	p := New()
	ctx := context.Background()
	sink := newRawSink()

	body := []byte("\x89PNG\r\n\x1a\n... pretend PNG bytes ...")
	res, _, err := p.Apply(ctx, makeEntry("img.png", int64(len(body))), marc.Facts{Size: int64(len(body))}, bytes.NewReader(body), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := p.Reverse(ctx, res, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.Bytes(), body)
	}
}

// End-to-end test (archive + extract through the real pipeline) lives in
// internal/runtime/passthrough_e2e_test.go to avoid an import cycle —
// runtime imports plan imports passthrough, so passthrough's tests can't
// import runtime.
