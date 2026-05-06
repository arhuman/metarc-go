package linesubst_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/internal/store/transforms/linesubst"
	"github.com/arhuman/metarc-go/pkg/marc"
)

// --- test helpers ---

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
	err   error // if set, Write returns this error
}

func newFakeSink() *fakeSink {
	return &fakeSink{blobs: make(map[marc.BlobID][]byte), next: 1}
}

func (s *fakeSink) Write(_ context.Context, r io.Reader) (marc.BlobID, error) {
	if s.err != nil {
		return 0, s.err
	}
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
	err   error // if set, Open returns this error
}

func (b *fakeBlobs) Open(id marc.BlobID) (io.ReadCloser, error) {
	if b.err != nil {
		return nil, b.err
	}
	data, ok := b.blobs[id]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// testDict is a small dictionary used by all engine tests.
var testDict = []string{
	"if err != nil {",
	"return nil",
	"return err",
	"import (",
	"func main() {",
}

func newTestTransform() *linesubst.LineSubst {
	return linesubst.New("test/v1", testDict, func(e marc.Entry, f marc.Facts) bool {
		return strings.HasSuffix(e.RelPath, ".go") && f.Size > 0
	})
}

func applyAndReverse(t *testing.T, ls *linesubst.LineSubst, input string) string {
	t.Helper()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("x.go", int64(len(input)))
	result, handled, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("Apply: expected handled=true")
	}
	var out bytes.Buffer
	if err := ls.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	return out.String()
}

// --- EncodeIndex / DecodeIndex ---

func TestEncodeIndex_SkipsReservedBytes(t *testing.T) {
	for i := range 254 {
		b := linesubst.EncodeIndex(i)
		if b == 0x00 {
			t.Errorf("EncodeIndex(%d) = 0x00: conflicts with marker byte", i)
		}
		if b == 0x0a {
			t.Errorf("EncodeIndex(%d) = 0x0a: conflicts with newline", i)
		}
	}
}

func TestEncodeDecodeIndex_RoundTrip(t *testing.T) {
	for i := range 254 {
		enc := linesubst.EncodeIndex(i)
		got := linesubst.DecodeIndex(enc)
		if got != i {
			t.Errorf("DecodeIndex(EncodeIndex(%d)) = %d", i, got)
		}
	}
}

func TestEncodeIndex_KnownValues(t *testing.T) {
	cases := []struct {
		i    int
		want byte
	}{
		{0, 0x01},  // 0+1=1, < 0x0a → 1
		{8, 0x09},  // 8+1=9, < 0x0a → 9
		{9, 0x0b},  // 9+1=10 = 0x0a → skip → 11
		{10, 0x0c}, // 10+1=11 → skip 0x0a → 12
	}
	for _, c := range cases {
		if got := linesubst.EncodeIndex(c.i); got != c.want {
			t.Errorf("EncodeIndex(%d) = 0x%02x, want 0x%02x", c.i, got, c.want)
		}
	}
}

func TestEncodeIndex_AllUnique(t *testing.T) {
	seen := make(map[byte]int)
	for i := range 254 {
		b := linesubst.EncodeIndex(i)
		if prev, dup := seen[b]; dup {
			t.Errorf("EncodeIndex(%d) = 0x%02x, collides with index %d", i, b, prev)
		}
		seen[b] = i
	}
}

// --- New / ID ---

func TestNew_ID(t *testing.T) {
	ls := linesubst.New("my-transform/v1", testDict, func(marc.Entry, marc.Facts) bool { return true })
	if ls.ID() != "my-transform/v1" {
		t.Errorf("ID() = %q, want %q", ls.ID(), "my-transform/v1")
	}
}

func TestNew_EmptyDictionary(t *testing.T) {
	ls := linesubst.New("empty/v1", nil, func(marc.Entry, marc.Facts) bool { return true })
	if ls.ID() != "empty/v1" {
		t.Error("ID() wrong on empty-dict transform")
	}
	// Apply with empty dict: nothing is substituted, round-trip still works.
	input := "hello world\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip with empty dict: got %q, want %q", got, input)
	}
}

// --- Applicable ---

func TestApplicable_DelegatesToPredicate(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()

	cases := []struct {
		path string
		size int64
		want bool
	}{
		{"main.go", 1024, true},
		{"main.txt", 1024, false},
		{"main.go", 0, false},
	}
	for _, c := range cases {
		e := makeEntry(c.path, c.size)
		got := ls.Applicable(ctx, e, marc.Facts{Size: c.size})
		if got != c.want {
			t.Errorf("Applicable(%q, %d) = %v, want %v", c.path, c.size, got, c.want)
		}
	}
}

// --- CostEstimate ---

func TestCostEstimate(t *testing.T) {
	ls := newTestTransform()
	e := makeEntry("x.go", 10240)
	gain, cpu := ls.CostEstimate(e, marc.Facts{Size: 10240})
	if gain != 1024 {
		t.Errorf("gain = %d, want 1024", gain)
	}
	if cpu != 10 {
		t.Errorf("cpu = %d, want 10", cpu)
	}
}

func TestCostEstimate_ZeroSize(t *testing.T) {
	ls := newTestTransform()
	e := makeEntry("x.go", 0)
	gain, cpu := ls.CostEstimate(e, marc.Facts{Size: 0})
	if gain != 0 || cpu != 0 {
		t.Errorf("zero-size CostEstimate = (%d, %d), want (0, 0)", gain, cpu)
	}
}

// --- Apply ---

func TestApply_EmptyInput(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("x.go", 0)
	result, handled, err := ls.Apply(ctx, e, marc.Facts{}, strings.NewReader(""), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true on empty input")
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected 1 BlobID, got %d", len(result.BlobIDs))
	}
	if len(sink.blobs[result.BlobIDs[0]]) != 0 {
		t.Error("expected empty blob for empty input")
	}
}

func TestApply_NULByte_ReturnsNotHandled(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("x.go", 10)
	input := "hello\x00world\n"
	_, handled, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for content with NUL byte")
	}
}

func TestApply_NULByte_InFirstLine(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("x.go", 10)
	// NUL in very first line, no other content.
	input := "\x00\n"
	_, handled, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for NUL-only line")
	}
}

func TestApply_NoMatches_BlobEqualsInput(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	input := "var x = 42\nvar y = \"hello\"\n"
	e := makeEntry("x.go", int64(len(input)))
	result, _, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Equal(blob, []byte(input)) {
		t.Errorf("blob %q != input %q (expected no substitutions)", blob, input)
	}
}

func TestApply_ProducesToken_ForDictMatch(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	input := "return nil\n"
	e := makeEntry("x.go", int64(len(input)))
	result, _, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	blob := sink.blobs[result.BlobIDs[0]]
	if !bytes.Contains(blob, []byte{0x00}) {
		t.Error("expected blob to contain 0x00 token for dict-matched line")
	}
	// Token should be smaller than original.
	if len(blob) >= len(input) {
		t.Errorf("blob (%d B) not smaller than input (%d B)", len(blob), len(input))
	}
}

func TestApply_SinkError(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	sink.err = errors.New("disk full")
	e := makeEntry("x.go", 10)
	_, _, err := ls.Apply(ctx, e, marc.Facts{Size: 10}, strings.NewReader("hello\n"), sink)
	if err == nil {
		t.Fatal("expected error from sink, got nil")
	}
}

// --- Reverse ---

func TestReverse_EmptyBlobIDs_ReturnsNil(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	var out bytes.Buffer
	err := ls.Reverse(ctx, marc.Result{BlobIDs: nil}, &fakeBlobs{blobs: nil}, &out)
	if err != nil {
		t.Errorf("Reverse with empty BlobIDs: %v", err)
	}
	if out.Len() != 0 {
		t.Error("expected empty output for empty BlobIDs")
	}
}

func TestReverse_BlobOpenError(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	blobs := &fakeBlobs{err: errors.New("not found")}
	var out bytes.Buffer
	result := marc.Result{BlobIDs: []marc.BlobID{1}}
	err := ls.Reverse(ctx, result, blobs, &out)
	if err == nil {
		t.Fatal("expected error from blob open, got nil")
	}
}

func TestReverse_TruncatedToken_WritesContent(t *testing.T) {
	// A token marker with no following byte should be written as-is (fallback).
	ls := linesubst.New("test/v1", testDict, func(marc.Entry, marc.Facts) bool { return true })
	ctx := context.Background()

	// Manually craft a blob with a truncated token: \x00 at end of content, no index byte.
	truncated := "prefix\x00\n" // \x00 is last byte of content before newline
	sink := newFakeSink()
	_, _ = sink.Write(ctx, strings.NewReader(truncated))
	result := marc.Result{BlobIDs: []marc.BlobID{1}}

	var out bytes.Buffer
	err := ls.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	// Should not panic; content written as-is (graceful degradation).
}

// --- Round-trip tests ---

func TestRoundTrip_Mixed(t *testing.T) {
	ls := newTestTransform()
	input := "var x = 1\nif err != nil {\nreturn nil\nfmt.Println(x)\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_AllDictMatches(t *testing.T) {
	ls := newTestTransform()
	input := "if err != nil {\nreturn nil\nreturn err\nimport (\nfunc main() {\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_NoDictMatches(t *testing.T) {
	ls := newTestTransform()
	input := "// header\nvar answer = 42\nfmt.Println(answer)\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_NoTrailingNewline(t *testing.T) {
	ls := newTestTransform()
	input := "if err != nil {\nreturn nil"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_IndentedDictMatch_Tabs(t *testing.T) {
	ls := newTestTransform()
	// "return nil" in dict; tabbed variant should also round-trip.
	input := "\treturn nil\n\t\treturn err\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch (tabs):\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_IndentedDictMatch_Spaces(t *testing.T) {
	ls := newTestTransform()
	input := "    return nil\n        return err\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch (spaces):\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_EmptyLines(t *testing.T) {
	ls := newTestTransform()
	input := "\n\nreturn nil\n\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch (empty lines):\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_WhitespaceOnlyLine(t *testing.T) {
	ls := newTestTransform()
	input := "return nil\n   \n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch (whitespace line):\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_SingleLineNoNewline(t *testing.T) {
	ls := newTestTransform()
	input := "return nil"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch (single line no newline):\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_SingleLineWithNewline(t *testing.T) {
	ls := newTestTransform()
	input := "return nil\n"
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", got, input)
	}
}

func TestRoundTrip_LargeRepetitiveContent(t *testing.T) {
	ls := newTestTransform()
	var sb strings.Builder
	for i := range 1000 {
		sb.WriteString("if err != nil {\n")
		sb.WriteString("return nil\n")
		sb.WriteString(strings.Repeat("\t", i%5))
		sb.WriteString("return err\n")
	}
	input := sb.String()
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch on large input (len=%d)", len(input))
	}
}

func TestRoundTrip_AllIndentLevels(t *testing.T) {
	ls := newTestTransform()
	// Each dict entry at depths 0-4 (tabs).
	var sb strings.Builder
	for depth := range 5 {
		prefix := strings.Repeat("\t", depth)
		for _, line := range testDict {
			sb.WriteString(prefix)
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	input := sb.String()
	got := applyAndReverse(t, ls, input)
	if got != input {
		t.Errorf("round-trip mismatch for all indent levels")
	}
}

func TestRoundTrip_CompressesWhenMatchesExist(t *testing.T) {
	ls := newTestTransform()
	ctx := context.Background()
	sink := newFakeSink()
	input := strings.Repeat("if err != nil {\n", 100)
	e := makeEntry("x.go", int64(len(input)))
	result, handled, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(input))}, strings.NewReader(input), sink)
	if err != nil || !handled {
		t.Fatalf("Apply failed or not handled: %v %v", err, handled)
	}
	blob := sink.blobs[result.BlobIDs[0]]
	if len(blob) >= len(input) {
		t.Errorf("expected blob (%d B) < input (%d B)", len(blob), len(input))
	}
}

// --- Fuzz ---

func FuzzRoundTrip(f *testing.F) {
	ls := newTestTransform()

	f.Add("if err != nil {\nreturn nil\n")
	f.Add("var x = 1\n\treturn err\n")
	f.Add("")
	f.Add("no match\n")
	f.Add("\treturn nil")

	f.Fuzz(func(t *testing.T, s string) {
		if strings.ContainsRune(s, 0x00) {
			return
		}
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("x.go", int64(len(s)))
		result, handled, err := ls.Apply(ctx, e, marc.Facts{Size: int64(len(s))}, strings.NewReader(s), sink)
		if err != nil || !handled {
			return
		}
		var out bytes.Buffer
		if err := ls.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if out.String() != s {
			t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", out.String(), s)
		}
	})
}
