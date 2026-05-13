package logdate

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

func makeSink() *fakeSink {
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
	ld := New()
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
			got := ld.Applicable(ctx, e, marc.Facts{Size: tt.size})
			if got != tt.want {
				t.Errorf("Applicable(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func runApply(t *testing.T, content string) (marc.Result, bool, *fakeSink) {
	t.Helper()
	ld := New()
	ctx := context.Background()
	sink := makeSink()
	e := makeEntry("app.log", int64(len(content)))
	facts := marc.Facts{Size: int64(len(content))}
	res, handled, err := ld.Apply(ctx, e, facts, strings.NewReader(content), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res, handled, sink
}

func roundTrip(t *testing.T, content string) string {
	t.Helper()
	res, handled, sink := runApply(t, content)
	if !handled {
		t.Fatalf("expected handled=true")
	}
	ld := New()
	blobs := &fakeBlobs{blobs: sink.blobs}
	var buf bytes.Buffer
	if err := ld.Reverse(context.Background(), res, blobs, &buf); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	return buf.String()
}

func TestApply_NULBailout(t *testing.T) {
	// Build content that has timestamps so it would otherwise match, then add NUL.
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("2026-05-11T08:42:%02dZ msg %d", i%60, i))
	}
	content := strings.Join(lines, "\n") + "\n\x00binary"
	_, handled, _ := runApply(t, content)
	if handled {
		t.Fatal("expected handled=false when NUL present")
	}
}

func TestApply_NoTimestamps(t *testing.T) {
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("plain log line number %d with no timestamp", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	_, handled, _ := runApply(t, content)
	if handled {
		t.Fatal("expected handled=false when no timestamps")
	}
}

func TestApply_BelowTimestampDensity(t *testing.T) {
	// Mostly non-timestamp lines: only 5 out of 60 have timestamps — below 50% gate.
	var lines []string
	for i := range 5 {
		lines = append(lines, fmt.Sprintf("2026-05-11T08:42:%02dZ ts line %d", i, i))
	}
	for i := range 55 {
		lines = append(lines, fmt.Sprintf("plain log line number %d with no timestamp", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	_, handled, _ := runApply(t, content)
	if handled {
		t.Fatal("expected handled=false when <50% of lines have timestamps")
	}
}

func TestRoundTrip_DoubleQuoted(t *testing.T) {
	// logfmt-style: time="..." where the timestamp is double-quoted.
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf(`time="2026-05-11T08:42:%02dZ" level=info msg="event %d"`, i%60, i))
	}
	content := strings.Join(lines, "\n") + "\n"
	got := roundTrip(t, content)
	if got != content {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func TestRoundTrip_SingleQuoted(t *testing.T) {
	// SQL-style timestamps surrounded by single quotes.
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("INSERT INTO logs VALUES ('2026-05-11T08:42:%02dZ', %d);", i%60, i))
	}
	content := strings.Join(lines, "\n") + "\n"
	got := roundTrip(t, content)
	if got != content {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		build func() string
	}{
		{
			name: "RFC3339Nano",
			build: func() string {
				cases := []string{
					"2026-05-11T08:42:17.123456789Z",
					"2026-05-11T08:42:17.000000000Z",
					"2026-05-11T08:42:17.1Z",
					"2026-05-11T08:42:17.999999Z",
				}
				var lines []string
				for i, ts := range cases {
					for j := range 15 {
						lines = append(lines, fmt.Sprintf("%s case=%d j=%d", ts, i, j))
					}
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "RFC3339",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2026-05-11T08:%02d:%02dZ event %d", i%60, (i*7)%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "ISOOffset",
			build: func() string {
				var lines []string
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026-05-11T15:%02d:%02d+07:00 east %d", i%60, (i*3)%60, i))
				}
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026-05-11T03:%02d:%02d-05:30 west %d", i%60, (i*5)%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "macOS",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2026-05-11 15:42:%02d.123456+0700 entry %d", i%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "RFC3339NanoOffset",
			build: func() string {
				cases := []string{
					"2026-05-12T04:22:21.795+02:00",
					"2026-05-12T04:22:21.123456789+02:00",
					"2026-05-12T04:22:21.000+00:00",
					"2026-05-12T04:22:21.999999-05:30",
				}
				var lines []string
				for i, ts := range cases {
					for j := range 15 {
						lines = append(lines, fmt.Sprintf("time=%s level=INFO msg=\"event\" i=%d j=%d", ts, i, j))
					}
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "GINLog",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2025/05/11 10:36:%02d [32m/api/repo.go:42", i%60))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "MixedFormats",
			build: func() string {
				var lines []string
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026-05-11T08:42:%02d.123Z fmt1 line %d", i%60, i))
				}
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026-05-11T08:42:%02d+07:00 fmt3 line %d", i%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "Syslog",
			build: func() string {
				months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("%s %2d 15:%02d:%02d sshd[1234]: login from 10.0.0.1", months[i%12], (i%28)+1, i%60, (i*7)%60))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "Log4j",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2026-05-11 10:%02d:%02d,%03d INFO main org.example.App: event %d", i%60, (i*7)%60, (i*17)%1000, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "Python",
			build: func() string {
				cases := []string{
					"2026-05-11 10:36:19.008",
					"2026-05-11 10:36:19.1",
					"2026-05-11 10:36:19.123456",
					"2026-05-11 10:36:19.000000000",
				}
				var lines []string
				for i, ts := range cases {
					for j := range 15 {
						lines = append(lines, fmt.Sprintf("%s INFO event i=%d j=%d", ts, i, j))
					}
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		// '/' date-separator variants
		{
			name: "RFC3339Nano/slash",
			build: func() string {
				cases := []string{
					"2026/05/11T08:42:17.123456789Z",
					"2026/05/11T08:42:17.000000000Z",
					"2026/05/11T08:42:17.1Z",
					"2026/05/11T08:42:17.999999Z",
				}
				var lines []string
				for i, ts := range cases {
					for j := range 15 {
						lines = append(lines, fmt.Sprintf("%s case=%d j=%d", ts, i, j))
					}
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "RFC3339/slash",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2026/05/11T08:%02d:%02dZ event %d", i%60, (i*7)%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "ISOOffset/slash",
			build: func() string {
				var lines []string
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026/05/11T15:%02d:%02d+07:00 east %d", i%60, (i*3)%60, i))
				}
				for i := range 30 {
					lines = append(lines, fmt.Sprintf("2026/05/11T03:%02d:%02d-05:30 west %d", i%60, (i*5)%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "macOS/slash",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2026/05/11 15:42:%02d.123456+0700 entry %d", i%60, i))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "RFC3339NanoOffset/slash",
			build: func() string {
				cases := []string{
					"2026/05/12T04:22:21.795+02:00",
					"2026/05/12T04:22:21.123456789+02:00",
					"2026/05/12T04:22:21.000+00:00",
					"2026/05/12T04:22:21.999999-05:30",
				}
				var lines []string
				for i, ts := range cases {
					for j := range 15 {
						lines = append(lines, fmt.Sprintf("time=%s level=INFO msg=\"event\" i=%d j=%d", ts, i, j))
					}
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
		{
			name: "GINLog/slash",
			build: func() string {
				var lines []string
				for i := range 60 {
					lines = append(lines, fmt.Sprintf("2025/05/11 10:36:%02d [32m/api/repo.go:42", i%60))
				}
				return strings.Join(lines, "\n") + "\n"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.build()
			got := roundTrip(t, content)
			if got != content {
				t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", got, content)
			}
		})
	}
}

func TestRoundTrip_MultiLineWithNonTimestampLines(t *testing.T) {
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("2026-05-11T08:42:%02dZ event %d", i%60, i))
	}
	// Add some non-timestamp lines (still below 50% threshold breaker).
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("--- separator line %d ---", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	got := roundTrip(t, content)
	if got != content {
		t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add("2026-05-11T08:42:17Z hello\n")
	f.Add("2026-05-11T08:42:17.123Z line\n2026-05-11T08:42:18.456Z line2\n")
	f.Fuzz(func(t *testing.T, s string) {
		if strings.IndexByte(s, 0x00) >= 0 {
			t.Skip()
		}
		// Pad with timestamps to ensure a dominant fmt may be detected; if not handled, just skip.
		ld := New()
		ctx := context.Background()
		sink := makeSink()
		e := makeEntry("fuzz.log", int64(len(s)))
		facts := marc.Facts{Size: int64(len(s))}
		res, handled, err := ld.Apply(ctx, e, facts, strings.NewReader(s), sink)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !handled {
			return
		}
		blobs := &fakeBlobs{blobs: sink.blobs}
		var buf bytes.Buffer
		if err := ld.Reverse(ctx, res, blobs, &buf); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if buf.String() != s {
			t.Fatalf("round-trip mismatch:\ngot:  %q\nwant: %q", buf.String(), s)
		}
	})
}
