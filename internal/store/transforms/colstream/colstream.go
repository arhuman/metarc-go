// Package colstream implements the col-stream/v1 transform: a lossless
// column-major re-encoding of delimiter-separated row data (.csv and .tsv).
//
// The axis it attacks is explicit non-byte structure plus arithmetic relations
// between values, not local textual redundancy. A row-major CSV interleaves
// unrelated value domains on every line, so a timestamp sits next to a log
// level which sits next to free-form text. Transposing puts each column's
// values next to each other, which gives the final zstd pass long runs of
// same-domain data. A column whose every value is an integer is additionally
// stored as delta plus zigzag plus varint: LZ77 cannot subtract, so a counter
// incrementing by one costs a single byte here where the absolute decimal text
// cost six.
//
// Selection is by measurement, never by extension. The extension is only a
// cheap gate; Apply compresses a sample both ways and proceeds only if the
// column-major form wins by a margin. It then runs its own Reverse over the
// whole encoded file and compares byte-for-byte, falling back to the raw path
// on any mismatch. Both guards exist because this class of transform fails by
// silently inflating a file that looked like a match (see ADR-020).
package colstream

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
)

const colStreamID marc.TransformID = "col-stream/v1"

// Applicability bounds. Below minSize the column headers and the per-column
// framing cost more than transposition can recover; above maxSize the
// whole-file verify pass (a full decode plus a comparison) stops being a
// reasonable price to pay at archive time.
const (
	minSize = 4 * 1024
	maxSize = 256 * 1024 * 1024
)

// sampleSize bounds the cheap pre-filter that runs before the decisive
// whole-file measurement. It only rejects, never accepts.
const sampleSize = 256 * 1024

// winMargin is the fraction by which the column-major form must beat the raw
// form to be selected. A win smaller than this does not pay for the transform
// record, the params, and the CPU, and the downside of a wrong "yes" (an
// inflated file) is worse than a missed marginal gain.
const winMargin = 0.02

// sampleRejectMargin is how badly the sample must lose for the pre-filter to
// skip the whole-file measurement. It is deliberately slack because the sample
// is biased pessimistic: a truncated table has shorter numeric runs and less
// long-range column redundancy than the file it came from, so files that win
// overall routinely lose on their first 256 KB (Hadoop measures +4.9% sampled
// against -10.1% whole). Only a file losing by more than this margin on the
// sample is hopeless enough to reject without measuring it properly.
const sampleRejectMargin = 0.20

// blobMagic identifies a col-stream blob and pins its layout version. Reverse
// reads it before anything else, so a blob from a future encoder is rejected
// rather than misparsed.
var blobMagic = [4]byte{'M', 'C', 'S', 1}

// Column kinds, stored as one byte per column in the blob header.
const (
	kindText    byte = 0 // values joined by \n, in row order
	kindNumeric byte = 1 // delta + zigzag + varint of each value
)

// ColStream implements the col-stream/v1 transform.
type ColStream struct{}

// New returns a new col-stream transform.
func New() *ColStream { return &ColStream{} }

// ID returns the stable transform identifier.
func (c *ColStream) ID() marc.TransformID { return colStreamID }

// Applicable gates on extension and size only. It is deliberately permissive:
// the real decision is the measured probe inside Apply, and a cheap predicate
// that admitted too little would hide files the probe would have accepted.
func (c *ColStream) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	if facts.Size < minSize || facts.Size > maxSize {
		return false
	}
	switch strings.ToLower(filepath.Ext(e.RelPath)) {
	case ".csv", ".tsv":
		return true
	default:
		return false
	}
}

// CostEstimate returns the gain and CPU estimates for column transposition.
//
// The gain is net, counting bytes added as well as bytes removed. That is the
// lesson of log-date-subst/v2, which the planner selected because its estimate
// counted only the text it deleted and ignored the near-incompressible binary
// it wrote in its place (ADR-020). Here the added bytes are the per-column
// framing and the quoting bitmap, roughly one bit per field; the removed bytes
// are the delimiters and the redundancy the final compressor now gets to see
// column-wise. A tenth of the file is a deliberately conservative net figure
// against the -13% measured on the loghub CSV class.
//
// CPU is charged for the passes over the content: the CSV parse, the encode,
// the two measurement compressions, and the verify decode.
func (c *ColStream) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 10, facts.Size / 64
}

// blobParams is the JSON structure stored in Result.Params. The blob is
// self-describing, so params carries only what identifies the encoding, well
// under the 1 KB budget.
type blobParams struct {
	V int `json:"v"`
}

// Apply parses the file as delimiter-separated rows, re-encodes it
// column-major, and writes a single blob. It returns handled=false, never an
// error, whenever the content is unsuitable: unparseable, NUL-bearing, not a
// measured win, or failing the round-trip verify. A content mismatch is not an
// archiving failure, it is this transform declining the file.
func (c *ColStream) Apply(ctx context.Context, e marc.Entry, _ marc.Facts, src io.ReadSeeker, sink marc.BlobSink) (marc.Result, bool, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("col-stream: read: %w", err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return marc.Result{}, false, nil
	}

	comma := delimiterFor(e.RelPath)

	encoded, ok := encode(data, comma)
	if !ok {
		return marc.Result{}, false, nil
	}

	if !worthIt(data, encoded, comma) {
		return marc.Result{}, false, nil
	}

	// Verify or fall back: the lossless contract is enforced here at archive
	// time, not only in tests. Any divergence sends the file down the raw path.
	var check bytes.Buffer
	if decodeErr := decode(encoded, &check); decodeErr != nil {
		return marc.Result{}, false, nil
	}
	if !bytes.Equal(check.Bytes(), data) {
		return marc.Result{}, false, nil
	}

	id, err := sink.Write(ctx, bytes.NewReader(encoded))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("col-stream: write blob: %w", err)
	}

	params, err := json.Marshal(blobParams{V: 1})
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("col-stream: marshal params: %w", err)
	}

	return marc.Result{BlobIDs: []marc.BlobID{id}, Params: params}, true, nil
}

// Reverse reconstructs the original file from the blob alone. Params is not
// consulted beyond its presence: the blob carries its own version, column
// count, per-column kinds and lengths.
func (c *ColStream) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return errors.New("col-stream: result has no blob")
	}
	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("col-stream: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("col-stream: read blob: %w", err)
	}
	return decode(data, dst)
}

// delimiterFor returns the field delimiter implied by the file extension.
func delimiterFor(relPath string) rune {
	if strings.EqualFold(filepath.Ext(relPath), ".tsv") {
		return '\t'
	}
	return ','
}

// worthIt decides by measurement whether the column-major form is worth
// keeping: it zstd-compresses both forms and requires the column-major one to
// win by winMargin. Extension never decides; this does.
//
// The decisive comparison is on the whole file. A sampled comparison was the
// first design, and measuring it against the loghub CSV class showed it
// systematically under-selects: the sample's verdict disagrees with the
// file's on roughly a third of the corpus, always in the direction of
// refusing a file that would have won. The whole-file compression is also
// nearly free here, since Apply has already encoded the whole file and is
// about to decode it again for the verify pass.
//
// The sample survives as a pre-filter for the hopeless cases only, so a large
// file that transposition clearly inflates is dropped without paying for two
// full compressions.
func worthIt(data, encoded []byte, comma rune) bool {
	if len(data) > sampleSize && !samplePlausible(data, comma) {
		return false
	}

	rawSize, err := zstdSize(data)
	if err != nil {
		return false
	}
	encSize, err := zstdSize(encoded)
	if err != nil {
		return false
	}
	return float64(encSize) < float64(rawSize)*(1-winMargin)
}

// samplePlausible reports whether the first rows of the file leave any chance
// that the whole file wins. It returns false only for a sample that loses by
// more than sampleRejectMargin, which the measured bias shows is well outside
// the range where a sampled loss still hides a whole-file win.
//
// The sample is re-encoded from scratch rather than sliced out of the full
// encoding: a column-major encoding cannot be truncated at a byte offset and
// still represent the same rows.
func samplePlausible(data []byte, comma rune) bool {
	sample := data
	// Cut on a row boundary so the sample parses as whole records.
	if nl := bytes.LastIndexByte(sample[:sampleSize], '\n'); nl > 0 {
		sample = sample[:nl+1]
	} else {
		return true // no row boundary to cut on; let the full measurement decide
	}

	sampleEnc, ok := encode(sample, comma)
	if !ok {
		return true // the prefix alone is not a table; judge the whole file
	}
	rawSize, err := zstdSize(sample)
	if err != nil {
		return true
	}
	encSize, err := zstdSize(sampleEnc)
	if err != nil {
		return true
	}
	return float64(encSize) < float64(rawSize)*(1+sampleRejectMargin)
}

// zstdSize returns the compressed length of b. The level is the one the solid
// block path uses, so the probe measures what the archive will actually pay.
func zstdSize(b []byte) (int, error) {
	var counter countingWriter
	enc, err := zstd.NewWriter(&counter, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return 0, err
	}
	if _, err := enc.Write(b); err != nil {
		_ = enc.Close()
		return 0, err
	}
	if err := enc.Close(); err != nil {
		return 0, err
	}
	return counter.n, nil
}

// countingWriter discards its input and counts the bytes. The probe needs the
// compressed size, never the compressed bytes.
type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// record is one parsed row plus the byte-exact detail encoding/csv discards.
type record struct {
	fields []string
	quoted []bool // per field: was it quoted in the source
	crlf   bool   // the row's terminator was \r\n rather than \n
}

// encode re-encodes data column-major. It returns ok=false when the content is
// not a table this transform can represent: unparseable, fewer than minRows
// rows, ragged beyond the exception budget, or too few columns for
// transposition to mean anything.
func encode(data []byte, comma rune) ([]byte, bool) {
	recs, trailingNL, ok := parse(data, comma)
	if !ok {
		return nil, false
	}
	// Below these, the per-column framing dominates and transposition has no
	// column runs to exploit.
	const minRows, minCols = 8, 2
	if len(recs) < minRows || len(recs[0].fields) < minCols {
		return nil, false
	}

	nCols := len(recs[0].fields)
	var exceptions []int
	for i, r := range recs {
		if len(r.fields) != nCols {
			exceptions = append(exceptions, i)
		}
	}
	// A table that is mostly ragged is not a table; the exception stream would
	// hold the file verbatim plus the framing on top.
	if len(exceptions)*4 > len(recs) {
		return nil, false
	}

	regular := make([]*record, 0, len(recs)-len(exceptions))
	for i := range recs {
		if len(recs[i].fields) == nCols {
			regular = append(regular, &recs[i])
		}
	}

	var out bytes.Buffer
	out.Write(blobMagic[:])
	// The delimiter goes in the blob: Reverse is given the blob and params
	// only, never the entry path the extension came from.
	out.WriteByte(byte(comma))
	writeUvarint(&out, uint64(len(recs)))
	writeUvarint(&out, uint64(nCols))
	if trailingNL {
		out.WriteByte(1)
	} else {
		out.WriteByte(0)
	}

	// Column payloads, each preceded by its kind and byte length so Reverse
	// can walk them without re-deriving any of the encoder's decisions.
	cols := make([][]byte, nCols)
	kinds := make([]byte, nCols)
	for c := 0; c < nCols; c++ {
		cols[c], kinds[c] = encodeColumn(regular, c)
	}
	for c := 0; c < nCols; c++ {
		out.WriteByte(kinds[c])
		writeUvarint(&out, uint64(len(cols[c])))
	}
	for c := 0; c < nCols; c++ {
		out.Write(cols[c])
	}

	// Per-row flags: the CRLF bit for every row, then the quoting bit for
	// every field of the regular rows. These are what makes the round trip
	// byte-exact rather than merely semantically equal.
	writeBits(&out, func(yield func(bool)) {
		for i := range recs {
			yield(recs[i].crlf)
		}
	})
	writeBits(&out, func(yield func(bool)) {
		for _, r := range regular {
			for _, q := range r.quoted {
				yield(q)
			}
		}
	})

	// Exception rows: index plus the verbatim field list, so one malformed row
	// never costs the whole file.
	writeUvarint(&out, uint64(len(exceptions)))
	for _, idx := range exceptions {
		writeUvarint(&out, uint64(idx))
		r := recs[idx]
		writeUvarint(&out, uint64(len(r.fields)))
		for i, f := range r.fields {
			writeUvarint(&out, uint64(len(f)))
			out.WriteString(f)
			if r.quoted[i] {
				out.WriteByte(1)
			} else {
				out.WriteByte(0)
			}
		}
	}

	return out.Bytes(), true
}

// encodeColumn serializes column c of the regular rows, choosing the numeric
// encoding when every value in the column is an integer that survives a
// decimal round trip. The round-trip check matters: "007" and "+7" parse as
// integers but do not re-render as themselves, so they must stay text.
func encodeColumn(rows []*record, c int) (payload []byte, kind byte) {
	var buf bytes.Buffer

	numeric := len(rows) > 0
	for _, r := range rows {
		v := r.fields[c]
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != v {
			numeric = false
			break
		}
	}

	if numeric {
		var prev int64
		tmp := make([]byte, binary.MaxVarintLen64)
		for _, r := range rows {
			n, _ := strconv.ParseInt(r.fields[c], 10, 64)
			// Deltas are taken modulo 2^64 so a swing across the int64 range
			// wraps instead of overflowing; decode undoes it the same way.
			d := int64(uint64(n) - uint64(prev))
			k := binary.PutVarint(tmp, d)
			buf.Write(tmp[:k])
			prev = n
		}
		return buf.Bytes(), kindNumeric
	}

	for _, r := range rows {
		writeUvarint(&buf, uint64(len(r.fields[c])))
		buf.WriteString(r.fields[c])
	}
	return buf.Bytes(), kindText
}

// decode reconstructs the original bytes from a col-stream blob.
//
// Every length and count read here comes from archive data that may be
// corrupt, so each one is validated against the bytes actually remaining
// before it is used to allocate or to slice.
func decode(blob []byte, dst io.Writer) error {
	rd := &reader{b: blob}

	magic, err := rd.bytes(len(blobMagic))
	if err != nil {
		return fmt.Errorf("col-stream: read magic: %w", err)
	}
	if !bytes.Equal(magic, blobMagic[:]) {
		return fmt.Errorf("col-stream: bad magic %x", magic)
	}

	comma, err := rd.byteVal()
	if err != nil {
		return fmt.Errorf("col-stream: read delimiter: %w", err)
	}

	nRows, err := rd.count()
	if err != nil {
		return fmt.Errorf("col-stream: read row count: %w", err)
	}
	nCols, err := rd.count()
	if err != nil {
		return fmt.Errorf("col-stream: read column count: %w", err)
	}
	trailingNLByte, err := rd.byteVal()
	if err != nil {
		return fmt.Errorf("col-stream: read trailing flag: %w", err)
	}
	trailingNL := trailingNLByte == 1
	if nCols == 0 {
		return errors.New("col-stream: zero columns")
	}

	kinds := make([]byte, nCols)
	lengths := make([]int, nCols)
	for c := 0; c < nCols; c++ {
		if kinds[c], err = rd.byteVal(); err != nil {
			return fmt.Errorf("col-stream: read column kind: %w", err)
		}
		if lengths[c], err = rd.count(); err != nil {
			return fmt.Errorf("col-stream: read column length: %w", err)
		}
	}

	payloads := make([][]byte, nCols)
	for c := 0; c < nCols; c++ {
		if payloads[c], err = rd.bytes(lengths[c]); err != nil {
			return fmt.Errorf("col-stream: read column %d: %w", c, err)
		}
	}

	crlf, err := rd.bits(nRows)
	if err != nil {
		return fmt.Errorf("col-stream: read line-ending flags: %w", err)
	}

	// The exception indices are needed before the quoting bits can be split
	// per row, because only regular rows contributed quoting bits. They sit at
	// the end of the blob, so the quoting bits are claimed after a lookahead
	// that leaves the reader where it was.
	exceptions, quoted, err := readTail(rd, nRows, nCols)
	if err != nil {
		return err
	}

	cols, err := decodeColumns(payloads, kinds, nRows-len(exceptions))
	if err != nil {
		return err
	}

	return emit(dst, cols, crlf, quoted, exceptions, nRows, nCols, trailingNL, comma)
}

// readTail reads the quoting bitmap and the exception rows. It is one function
// because the two are interdependent: the bitmap's length depends on how many
// rows are exceptions, which is only known from the tail.
func readTail(rd *reader, nRows, nCols int) (map[int]*record, []bool, error) {
	save := rd.off

	// Skip the quoting bitmap with a placeholder length to reach the
	// exception count, then come back once the real length is known.
	// The bitmap length is a function of the field count, so the exception
	// count is the only unknown and one lookahead resolves it.
	// The bitmap is prefixed by its own bit count, so skipping is exact.
	if err := rd.skipBits(); err != nil {
		return nil, nil, fmt.Errorf("col-stream: skip quoting flags: %w", err)
	}
	nExc, err := rd.count()
	if err != nil {
		return nil, nil, fmt.Errorf("col-stream: read exception count: %w", err)
	}
	if nExc > nRows {
		return nil, nil, fmt.Errorf("col-stream: %d exceptions exceed %d rows", nExc, nRows)
	}
	exceptions := make(map[int]*record, nExc)
	for i := 0; i < nExc; i++ {
		idx, idxErr := rd.count()
		if idxErr != nil {
			return nil, nil, fmt.Errorf("col-stream: read exception index: %w", idxErr)
		}
		if idx >= nRows {
			return nil, nil, fmt.Errorf("col-stream: exception index %d out of range (%d rows)", idx, nRows)
		}
		nf, nfErr := rd.count()
		if nfErr != nil {
			return nil, nil, fmt.Errorf("col-stream: read exception field count: %w", nfErr)
		}
		rec := &record{fields: make([]string, nf), quoted: make([]bool, nf)}
		for f := 0; f < nf; f++ {
			ln, lnErr := rd.count()
			if lnErr != nil {
				return nil, nil, fmt.Errorf("col-stream: read exception field length: %w", lnErr)
			}
			raw, rawErr := rd.bytes(ln)
			if rawErr != nil {
				return nil, nil, fmt.Errorf("col-stream: read exception field: %w", rawErr)
			}
			rec.fields[f] = string(raw)
			q, qErr := rd.byteVal()
			if qErr != nil {
				return nil, nil, fmt.Errorf("col-stream: read exception quote flag: %w", qErr)
			}
			rec.quoted[f] = q == 1
		}
		exceptions[idx] = rec
	}

	end := rd.off
	rd.off = save
	quoted, err := rd.bits((nRows - nExc) * nCols)
	if err != nil {
		return nil, nil, fmt.Errorf("col-stream: read quoting flags: %w", err)
	}
	rd.off = end
	return exceptions, quoted, nil
}

// decodeColumns expands each column payload back into nRegular values.
func decodeColumns(payloads [][]byte, kinds []byte, nRegular int) ([][]string, error) {
	if nRegular < 0 {
		return nil, errors.New("col-stream: negative regular row count")
	}
	cols := make([][]string, len(payloads))
	for c, payload := range payloads {
		vals := make([]string, nRegular)
		pr := &reader{b: payload}
		switch kinds[c] {
		case kindNumeric:
			var prev int64
			for i := 0; i < nRegular; i++ {
				d, err := pr.varint()
				if err != nil {
					return nil, fmt.Errorf("col-stream: column %d value %d: %w", c, i, err)
				}
				prev = int64(uint64(prev) + uint64(d))
				vals[i] = strconv.FormatInt(prev, 10)
			}
		case kindText:
			for i := 0; i < nRegular; i++ {
				ln, err := pr.count()
				if err != nil {
					return nil, fmt.Errorf("col-stream: column %d length %d: %w", c, i, err)
				}
				raw, err := pr.bytes(ln)
				if err != nil {
					return nil, fmt.Errorf("col-stream: column %d value %d: %w", c, i, err)
				}
				vals[i] = string(raw)
			}
		default:
			return nil, fmt.Errorf("col-stream: unknown column kind %d", kinds[c])
		}
		if pr.off != len(payload) {
			return nil, fmt.Errorf("col-stream: column %d has %d trailing bytes", c, len(payload)-pr.off)
		}
		cols[c] = vals
	}
	return cols, nil
}

// emit renders the rows back to their original byte form, interleaving the
// regular rows read column-wise with the verbatim exception rows.
func emit(dst io.Writer, cols [][]string, crlf, quoted []bool, exceptions map[int]*record, nRows, nCols int, trailingNL bool, comma byte) error {
	var out bytes.Buffer
	regular := 0
	qi := 0
	for row := 0; row < nRows; row++ {
		if exc, ok := exceptions[row]; ok {
			out.WriteString(render(exc.fields, exc.quoted, rune(comma)))
		} else {
			fields := make([]string, nCols)
			q := make([]bool, nCols)
			for c := 0; c < nCols; c++ {
				if regular >= len(cols[c]) {
					return errors.New("col-stream: column shorter than row count")
				}
				fields[c] = cols[c][regular]
				if qi >= len(quoted) {
					return errors.New("col-stream: quoting bitmap too short")
				}
				q[c] = quoted[qi]
				qi++
			}
			regular++
			out.WriteString(render(fields, q, rune(comma)))
		}
		// The last row carries a terminator only if the source file ended
		// with one; every other row always does.
		if row == nRows-1 && !trailingNL {
			break
		}
		if crlf[row] {
			out.WriteString("\r\n")
		} else {
			out.WriteByte('\n')
		}
	}
	_, err := dst.Write(out.Bytes())
	return err
}

// parse reads data with encoding/csv and recovers, per row, the two things the
// parser normalizes away: which fields were quoted and whether the terminator
// was CRLF. It returns ok=false when the content is not parseable at all.
func parse(data []byte, comma rune) (recs []record, trailingNL bool, ok bool) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	r.LazyQuotes = false
	r.FieldsPerRecord = -1 // ragged rows are handled as exceptions, not errors
	r.ReuseRecord = false
	parsed, err := r.ReadAll()
	if err != nil || len(parsed) == 0 {
		return nil, false, false
	}

	lines, trailingNL := splitRows(data)
	// encoding/csv folds an embedded newline into one record, so a mismatch
	// means the file uses multi-line fields. Representing those would need a
	// row-to-line map; until then such files go raw.
	if len(lines) != len(parsed) {
		return nil, false, false
	}

	recs = make([]record, len(parsed))
	for i, fields := range parsed {
		line := lines[i]
		recs[i] = record{
			fields: fields,
			quoted: quotingOf(line.text, fields, comma),
			crlf:   line.crlf,
		}
		if recs[i].quoted == nil {
			return nil, false, false
		}
	}
	return recs, trailingNL, true
}

// rowLine is one physical source line and how it was terminated.
type rowLine struct {
	text string
	crlf bool
}

// splitRows splits data on newlines, recording per line whether the
// terminator was CRLF, and reports whether the file ended with a newline.
func splitRows(data []byte) (lines []rowLine, trailingNL bool) {
	s := string(data)
	trailingNL = strings.HasSuffix(s, "\n")
	if trailingNL {
		// Drop only the final \n. A preceding \r stays on the last line and
		// becomes its CRLF flag below, exactly like every other terminator.
		s = s[:len(s)-1]
	}
	parts := strings.Split(s, "\n")
	for i, raw := range parts {
		// A trailing \r is a terminator only when a \n followed it. On a final
		// line with no trailing newline the \r is content, and treating it as
		// a terminator would silently drop it (no terminator is re-emitted
		// after the last row in that case).
		terminated := i < len(parts)-1 || trailingNL
		if terminated && strings.HasSuffix(raw, "\r") {
			lines = append(lines, rowLine{text: raw[:len(raw)-1], crlf: true})
			continue
		}
		lines = append(lines, rowLine{text: raw, crlf: false})
	}
	return lines, trailingNL
}

// quotingOf recovers, for each field of a parsed row, whether it was quoted in
// the source line. It re-renders the row both ways and keeps the rendering
// that reproduces the source exactly; nil means no combination does, which
// sends the file down the raw path.
//
// Quoting is decided per field by re-rendering greedily: a field is rendered
// quoted when the source has a quote at that position, unquoted otherwise, and
// the whole line is compared at the end. That comparison is the only guarantee
// that matters, so the greedy walk cannot silently mis-attribute a quote.
func quotingOf(line string, fields []string, comma rune) []bool {
	quoted := make([]bool, len(fields))
	pos := 0
	for i := range fields {
		if i > 0 {
			if pos >= len(line) || rune(line[pos]) != comma {
				return nil
			}
			pos++
		}
		if pos < len(line) && line[pos] == '"' {
			quoted[i] = true
			pos += 2 + len(strings.ReplaceAll(fields[i], `"`, `""`))
		} else {
			pos += len(fields[i])
		}
		if pos > len(line) {
			return nil
		}
	}
	if pos != len(line) {
		return nil
	}
	if render(fields, quoted, comma) != line {
		return nil
	}
	return quoted
}

// render is the inverse of quotingOf for one row, used to confirm the recovered
// quoting reproduces the source line.
func render(fields []string, quoted []bool, comma rune) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteRune(comma)
		}
		if quoted[i] {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(f, `"`, `""`))
			b.WriteByte('"')
		} else {
			b.WriteString(f)
		}
	}
	return b.String()
}

// writeUvarint appends n in varint form.
func writeUvarint(buf *bytes.Buffer, n uint64) {
	tmp := make([]byte, binary.MaxVarintLen64)
	k := binary.PutUvarint(tmp, n)
	buf.Write(tmp[:k])
}

// writeBits appends a bit count followed by the packed bits, LSB first.
func writeBits(buf *bytes.Buffer, seq func(yield func(bool))) {
	var bits []bool
	seq(func(b bool) { bits = append(bits, b) })
	writeUvarint(buf, uint64(len(bits)))
	packed := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b {
			packed[i/8] |= 1 << (i % 8)
		}
	}
	buf.Write(packed)
}

// reader is a bounds-checked cursor over a blob. Every accessor validates
// against the remaining bytes, because the blob comes from an archive that may
// be corrupt or hostile.
type reader struct {
	b   []byte
	off int
}

func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || n > len(r.b)-r.off {
		return nil, fmt.Errorf("want %d bytes, %d remain", n, len(r.b)-r.off)
	}
	out := r.b[r.off : r.off+n]
	r.off += n
	return out, nil
}

func (r *reader) byteVal() (byte, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *reader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		return 0, errors.New("malformed uvarint")
	}
	r.off += n
	return v, nil
}

// count reads a uvarint bounded by the bytes remaining. Counts in this format
// always index into the blob, so one that exceeds its length is corrupt and
// must be rejected before it reaches make.
func (r *reader) count() (int, error) {
	v, err := r.uvarint()
	if err != nil {
		return 0, err
	}
	if v > uint64(len(r.b)) {
		return 0, fmt.Errorf("count %d exceeds blob size %d", v, len(r.b))
	}
	return int(v), nil
}

func (r *reader) varint() (int64, error) {
	v, n := binary.Varint(r.b[r.off:])
	if n <= 0 {
		return 0, errors.New("malformed varint")
	}
	r.off += n
	return v, nil
}

// bits reads a length-prefixed bit vector and checks it holds exactly want bits.
func (r *reader) bits(want int) ([]bool, error) {
	n, err := r.count()
	if err != nil {
		return nil, err
	}
	if n != want {
		return nil, fmt.Errorf("bit vector holds %d bits, want %d", n, want)
	}
	packed, err := r.bytes((n + 7) / 8)
	if err != nil {
		return nil, err
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = packed[i/8]&(1<<(i%8)) != 0
	}
	return out, nil
}

// skipBits advances past a length-prefixed bit vector without decoding it.
func (r *reader) skipBits() error {
	n, err := r.count()
	if err != nil {
		return err
	}
	_, err = r.bytes((n + 7) / 8)
	return err
}
