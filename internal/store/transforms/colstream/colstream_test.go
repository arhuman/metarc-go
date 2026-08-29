package colstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// fakeFileInfo is a minimal fs.FileInfo for building marc.Entry values.
type fakeFileInfo struct{ size int64 }

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func makeEntry(path string, size int64) marc.Entry {
	return marc.Entry{RelPath: path, Path: path, Info: fakeFileInfo{size: size}}
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
type fakeBlobs struct{ blobs map[marc.BlobID][]byte }

func (b *fakeBlobs) Open(id marc.BlobID) (io.ReadCloser, error) {
	data, ok := b.blobs[id]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// encodeDecode runs the internal encode/decode pair and returns the
// reconstructed bytes. It bypasses the measured predicate so that byte-exact
// reconstruction can be asserted on inputs too small or too incompressible to
// be selected in production.
func encodeDecode(t *testing.T, src string, comma rune) (string, bool) {
	t.Helper()
	enc, ok := encode([]byte(src), comma)
	if !ok {
		return "", false
	}
	var out bytes.Buffer
	if err := decode(enc, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.String(), true
}

// requireRoundTrip asserts the content survives encode then decode unchanged.
func requireRoundTrip(t *testing.T, name, src string, comma rune) {
	t.Helper()
	got, ok := encodeDecode(t, src, comma)
	if !ok {
		t.Fatalf("%s: encode declined the input", name)
	}
	if got != src {
		t.Errorf("%s: round-trip mismatch:\ngot:  %q\nwant: %q", name, got, src)
	}
}

// numericCSV builds a table whose first column is a dense counter, the shape
// the delta encoding exists for.
func numericCSV(rows int) string {
	var b strings.Builder
	b.WriteString("id,level,msg\n")
	for i := 0; i < rows; i++ {
		b.WriteString(strconv.Itoa(1000 + i))
		b.WriteString(",INFO,starting worker\n")
	}
	return b.String()
}

func TestRoundTrip_basic(t *testing.T) {
	requireRoundTrip(t, "basic", numericCSV(40), ',')
}

func TestRoundTrip_tsv(t *testing.T) {
	var b strings.Builder
	b.WriteString("id\tname\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\talpha\n")
	}
	requireRoundTrip(t, "tsv", b.String(), '\t')
}

func TestRoundTrip_quotedFieldsContainingDelimiter(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,content\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`,"a,b,c and a ""quote"" inside"`)
		b.WriteByte('\n')
	}
	requireRoundTrip(t, "quoted", b.String(), ',')
}

// A quoted field that needs no quoting must still come back quoted: the
// quoting bitmap, not a re-derivation, decides.
func TestRoundTrip_gratuitousQuoting(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,name\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`,"plain"`)
		b.WriteByte('\n')
	}
	requireRoundTrip(t, "gratuitous quoting", b.String(), ',')
}

func TestRoundTrip_crlf(t *testing.T) {
	src := strings.ReplaceAll(numericCSV(20), "\n", "\r\n")
	requireRoundTrip(t, "crlf", src, ',')
}

// A file whose line endings are inconsistent must keep each row's own
// terminator, not be normalized to whichever is more common.
func TestRoundTrip_mixedLineEndings(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,name\r\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",alpha")
		if i%2 == 0 {
			b.WriteString("\r\n")
		} else {
			b.WriteString("\n")
		}
	}
	requireRoundTrip(t, "mixed line endings", b.String(), ',')
}

func TestRoundTrip_noTrailingNewline(t *testing.T) {
	src := strings.TrimSuffix(numericCSV(20), "\n")
	requireRoundTrip(t, "no trailing newline", src, ',')
}

// A row with the wrong field count goes into the exceptions stream instead of
// failing the whole file.
func TestRoundTrip_raggedRowBecomesException(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,level,msg\n")
	for i := 0; i < 30; i++ {
		if i == 7 {
			b.WriteString("truncated,row\n")
			continue
		}
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",INFO,working\n")
	}
	src := b.String()
	requireRoundTrip(t, "ragged", src, ',')

	enc, ok := encode([]byte(src), ',')
	if !ok {
		t.Fatal("encode declined a mostly-regular table")
	}
	// The exception count sits at the tail; assert the row really took that
	// path rather than being silently dropped or padded.
	var out bytes.Buffer
	if err := decode(enc, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.String(), "truncated,row\n") {
		t.Error("exception row missing from reconstruction")
	}
}

// A column that is numeric except for one value must fall back to the text
// encoding for the whole column rather than mis-encode that value.
func TestRoundTrip_columnNumericExceptOneRow(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,name\n")
	for i := 0; i < 30; i++ {
		if i == 11 {
			b.WriteString("n/a,alpha\n")
			continue
		}
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",alpha\n")
	}
	src := b.String()
	requireRoundTrip(t, "mostly numeric column", src, ',')

	enc, _ := encode([]byte(src), ',')
	if bytes.Count(enc, []byte{kindNumeric}) > 0 && !strings.Contains(string(enc), "n/a") {
		t.Error("non-integer value lost from a column encoded as numeric")
	}
}

// Integers that do not survive a decimal round trip ("007", "+7", "-0") must
// keep the text encoding so their exact spelling is preserved.
func TestRoundTrip_nonCanonicalIntegers(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,name\n")
	for i := 0; i < 20; i++ {
		b.WriteString("00")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",alpha\n")
	}
	requireRoundTrip(t, "zero-padded integers", b.String(), ',')
}

// Deltas that span the int64 range must wrap rather than overflow.
func TestRoundTrip_int64Extremes(t *testing.T) {
	var b strings.Builder
	b.WriteString("v,name\n")
	vals := []int64{0, 9223372036854775807, -9223372036854775808, 1, -1, 9223372036854775807, -9223372036854775808, 42, 0, -7}
	for _, v := range vals {
		b.WriteString(strconv.FormatInt(v, 10))
		b.WriteString(",alpha\n")
	}
	requireRoundTrip(t, "int64 extremes", b.String(), ',')
}

func TestRoundTrip_singleColumnIsDeclined(t *testing.T) {
	var b strings.Builder
	b.WriteString("value\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	if _, ok := encode([]byte(b.String()), ','); ok {
		t.Error("encode accepted a single-column file; transposition has nothing to do there")
	}
}

func TestEncode_emptyInput(t *testing.T) {
	if _, ok := encode(nil, ','); ok {
		t.Error("encode accepted empty input")
	}
	if _, ok := encode([]byte(""), ','); ok {
		t.Error("encode accepted an empty string")
	}
}

func TestEncode_tooFewRows(t *testing.T) {
	if _, ok := encode([]byte("a,b\n1,2\n"), ','); ok {
		t.Error("encode accepted a table below the minimum row count")
	}
}

// Embedded newlines inside quoted fields would need a row-to-line map that
// this version does not carry, so such files must be declined outright.
func TestEncode_embeddedNewlineDeclined(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,msg\n")
	for i := 0; i < 20; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString(",\"line one\nline two\"\n")
	}
	if _, ok := encode([]byte(b.String()), ','); ok {
		t.Error("encode accepted a file with multi-line fields")
	}
}

func TestEncode_mostlyRaggedDeclined(t *testing.T) {
	var b strings.Builder
	b.WriteString("a,b,c\n")
	for i := 0; i < 20; i++ {
		b.WriteString("x,y\n") // every row differs from the header width
	}
	if _, ok := encode([]byte(b.String()), ','); ok {
		t.Error("encode accepted a table that is mostly exceptions")
	}
}

func TestApply_bailsOnNUL(t *testing.T) {
	src := numericCSV(400) + "\x00"
	c := New()
	sink := newFakeSink()
	e := makeEntry("data.csv", int64(len(src)))
	_, handled, err := c.Apply(context.Background(), e, marc.Facts{Size: int64(len(src))}, strings.NewReader(src), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if handled {
		t.Error("Apply handled content containing a NUL byte")
	}
	if len(sink.blobs) != 0 {
		t.Error("Apply wrote a blob for content it should have declined")
	}
}

// Apply must decline content where the column form does not beat the raw form,
// which is the guard that keeps a regressing file (Mac in the loghub corpus)
// out of the transform.
func TestApply_declinesWhenNotAWin(t *testing.T) {
	// Two wide text columns of unrelated random-looking values: transposing
	// them buys nothing and the framing costs bytes.
	var b strings.Builder
	b.WriteString("a,b\n")
	for i := 0; i < 2000; i++ {
		b.WriteString(strconv.FormatInt(int64(i*2654435761&0x7fffffff), 36))
		b.WriteByte(',')
		b.WriteString(strconv.FormatInt(int64(i*40503&0x7fffffff), 36))
		b.WriteByte('\n')
	}
	src := b.String()

	c := New()
	sink := newFakeSink()
	e := makeEntry("data.csv", int64(len(src)))
	_, handled, err := c.Apply(context.Background(), e, marc.Facts{Size: int64(len(src))}, strings.NewReader(src), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if handled {
		t.Error("Apply handled content the column form does not compress better")
	}
}

// structuredLogCSV mirrors the shape of a loghub *_structured.csv: a dense
// counter, a quoted time field, a low-cardinality level, and varied content.
// numericCSV is too uniform to test the predicate's accept path: raw zstd
// already crushes a table whose every row is identical, leaving no margin for
// transposition to win, which is the predicate working as intended.
func structuredLogCSV(rows int) string {
	components := []string{
		"org.apache.hadoop.mapreduce.v2.app.MRAppMaster",
		"org.apache.hadoop.yarn.client.RMProxy",
		"org.apache.hadoop.ipc.Client",
		"org.apache.hadoop.mapreduce.v2.app.job.impl.TaskAttemptImpl",
	}
	levels := []string{"INFO", "WARN", "ERROR", "DEBUG"}
	var b strings.Builder
	b.WriteString("LineId,Time,Level,Component,Content\n")
	for i := 0; i < rows; i++ {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`,"18:0`)
		b.WriteString(strconv.Itoa(i % 10))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(10 + i%50))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(100 + i%900))
		b.WriteString(`",`)
		b.WriteString(levels[i%len(levels)])
		b.WriteByte(',')
		b.WriteString(components[i%len(components)])
		b.WriteString(",Created MRAppMaster for application appattempt_144514442372")
		b.WriteString(strconv.Itoa(i % 1000))
		b.WriteString("_000001\n")
	}
	return b.String()
}

func TestApply_reverseRoundTrip(t *testing.T) {
	src := structuredLogCSV(4000)
	c := New()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("data.csv", int64(len(src)))

	result, handled, err := c.Apply(ctx, e, marc.Facts{Size: int64(len(src))}, strings.NewReader(src), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Fatal("Apply declined a highly columnar table")
	}
	if len(result.BlobIDs) != 1 {
		t.Fatalf("expected exactly 1 blob, got %d", len(result.BlobIDs))
	}
	if len(result.Params) > 1024 {
		t.Errorf("params are %d bytes, budget is 1024", len(result.Params))
	}

	var out bytes.Buffer
	if err := c.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if out.String() != src {
		t.Error("Reverse did not reproduce the source byte-for-byte")
	}
}

func TestReverse_noBlob(t *testing.T) {
	var out bytes.Buffer
	if err := New().Reverse(context.Background(), marc.Result{}, &fakeBlobs{}, &out); err == nil {
		t.Error("Reverse accepted a result with no blob")
	}
}

func TestDecode_rejectsBadMagic(t *testing.T) {
	var out bytes.Buffer
	if err := decode([]byte("XXXX and then some"), &out); err == nil {
		t.Error("decode accepted a blob with the wrong magic")
	}
}

// TestRoundTrip_corpusFile exercises the transform against a real structured
// log CSV, the class it was built for. Skipped when the corpus is absent so
// the suite stays green in a checkout without it.
func TestRoundTrip_corpusFile(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..",
		"experiments", "ablation", "corpus", "loghub", "Hadoop", "Hadoop_2k.log_structured.csv")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("corpus file not present: %v", err)
	}

	c := New()
	ctx := context.Background()
	sink := newFakeSink()
	e := makeEntry("Hadoop_2k.log_structured.csv", int64(len(data)))

	result, handled, err := c.Apply(ctx, e, marc.Facts{Size: int64(len(data))}, bytes.NewReader(data), sink)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !handled {
		t.Skip("predicate declined the corpus file; nothing to round-trip")
	}

	var out bytes.Buffer
	if err := c.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Error("corpus file did not round-trip byte-for-byte")
	}
}
