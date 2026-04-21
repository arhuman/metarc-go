package logtempl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	tmpl := NewTemplate()
	ctx := context.Background()

	tests := []struct {
		name string
		path string
		size int64
		want bool
	}{
		{"access.log", "access.log", 2048, true},
		{"error.log", "logs/error.log", 2048, true},
		{"rotated log", "app.log.1", 2048, true},
		{"compressed log", "app.log.gz", 2048, true},
		{"syslog", "syslog", 2048, true},
		{"messages", "messages", 2048, true},
		{"kern.log", "kern.log", 2048, true},
		{"go file", "main.go", 2048, false},
		{"txt file", "notes.txt", 2048, false},
		{"too small", "tiny.log", 512, false},
		{"too large", "huge.log", 60 * 1024 * 1024, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := makeEntry(tt.path, tt.size)
			got := tmpl.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q, size=%d) = %v, want %v", tt.path, tt.size, got, tt.want)
			}
		})
	}
}

func TestApply_structured(t *testing.T) {
	tmpl := NewTemplate()
	ctx := context.Background()
	sink := newFakeSink()

	// Generate 100 lines with a common prefix.
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("2024-01-15 10:30:%02d INFO server request handled path=/api/v1/users id=%d", i%60, i))
	}
	content := strings.Join(lines, "\n") + "\n"

	e := makeEntry("access.log", int64(len(content)))
	result, err := tmpl.Apply(ctx, e, strings.NewReader(content), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(result.BlobIDs))
	}

	// Stored blob should be smaller than original.
	stored := sink.blobs[result.BlobIDs[0]]
	if len(stored) >= len(content) {
		t.Errorf("stored (%d bytes) should be smaller than original (%d bytes)", len(stored), len(content))
	}
}

func TestApply_unstructured(t *testing.T) {
	tmpl := NewTemplate()
	ctx := context.Background()
	sink := newFakeSink()

	// Lines with varied prefixes so no single prefix reaches 50%.
	prefixes := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("%s random content line %d xyz%d", prefixes[i%len(prefixes)], i, i*17))
	}
	content := strings.Join(lines, "\n") + "\n"

	e := makeEntry("random.log", int64(len(content)))
	_, err := tmpl.Apply(ctx, e, strings.NewReader(content), sink)
	if !errors.Is(err, marc.ErrNotApplicable) {
		t.Fatalf("expected ErrNotApplicable, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	tmpl := NewTemplate()
	ctx := context.Background()
	sink := newFakeSink()

	// Generate structured log content.
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("2024-01-15 10:30:00 INFO server processed request id=%d status=200", i))
	}
	content := strings.Join(lines, "\n") + "\n"

	e := makeEntry("server.log", int64(len(content)))
	result, err := tmpl.Apply(ctx, e, strings.NewReader(content), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := tmpl.Reverse(ctx, result, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if buf.String() != content {
		t.Fatalf("round-trip mismatch:\ngot (%d bytes):  %q\nwant (%d bytes): %q",
			buf.Len(), buf.String()[:min(100, buf.Len())],
			len(content), content[:min(100, len(content))])
	}
}
