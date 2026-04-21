package transforms

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
	size int64
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

// fakeEntry builds a minimal marc.Entry.
func fakeEntry(size int64) marc.Entry {
	return marc.Entry{
		RelPath: "file.bin",
		Info:    fakeFileInfo{size: size},
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

func TestDedup_ID(t *testing.T) {
	d := NewDedup()
	if d.ID() != "dedup/v1" {
		t.Errorf("ID() = %q, want %q", d.ID(), "dedup/v1")
	}
}

func TestDedup_Applicable(t *testing.T) {
	d := NewDedup()
	ctx := context.Background()

	tests := []struct {
		name string
		size int64
		want bool
	}{
		{"non-empty file", 1024, true},
		{"one byte", 1, true},
		{"empty file", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := fakeEntry(tt.size)
			got := d.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(size=%d) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

func TestDedup_CostEstimate(t *testing.T) {
	d := NewDedup()
	e := fakeEntry(4096)
	gain, cpu := d.CostEstimate(e, marc.Facts{Size: 4096})
	if gain != 4096 {
		t.Errorf("gain = %d, want 4096", gain)
	}
	if cpu != 4 {
		t.Errorf("cpu = %d, want 4 (4096/1024)", cpu)
	}
}

func TestDedup_Apply(t *testing.T) {
	d := NewDedup()
	ctx := context.Background()
	sink := newFakeSink()

	content := []byte("dedup apply test content")
	e := fakeEntry(int64(len(content)))

	result, err := d.Apply(ctx, e, bytes.NewReader(content), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 BlobID, got %d", len(result.BlobIDs))
	}
	if result.BlobIDs[0] == 0 {
		t.Fatal("BlobID is zero")
	}
}

func TestDedup_Reverse(t *testing.T) {
	d := NewDedup()
	ctx := context.Background()
	sink := newFakeSink()

	content := []byte("dedup reverse test content")
	e := fakeEntry(int64(len(content)))

	// Apply first to get the blob ID.
	result, err := d.Apply(ctx, e, bytes.NewReader(content), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var out bytes.Buffer
	if err := d.Reverse(ctx, result, blobs, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if !bytes.Equal(out.Bytes(), content) {
		t.Errorf("Reverse content mismatch: got %q, want %q", out.Bytes(), content)
	}
}

func TestDedup_Reverse_emptyResult(t *testing.T) {
	d := NewDedup()
	ctx := context.Background()
	blobs := &fakeBlobs{blobs: make(map[marc.BlobID][]byte)}
	var out bytes.Buffer

	// Empty BlobIDs should not error.
	if err := d.Reverse(ctx, marc.Result{}, blobs, &out); err != nil {
		t.Errorf("Reverse with empty BlobIDs: %v", err)
	}
}
