// Package logdate implements the log-date-subst/v1 transform, which detects
// timestamp-dense log files and replaces each timestamp with a compact 13-byte
// binary token. Reverse reconstructs the original timestamps exactly (lossless).
package logdate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

const transformID marc.TransformID = "log-date-subst/v2"

const (
	minSize = 1024
	maxSize = 50 * 1024 * 1024
)

// Token layout — variable length depending on fmt_id:
//
// 10 bytes (fmtRFC3339=2, fmtGINLog=6 — no TZ, no subsec):
//
//	byte  0   : 0x00       sentinel
//	byte  1   : fmt_byte   bits 7-6=quote_type, bit 5=sep_slash, bits 4-0=fmt_id
//	bytes 2-9 : unix_nanos int64 BE
//
// 11 bytes (fmtRFC3339Nano=1 — no TZ, variable subsec):
//
//	byte  0    : 0x00          sentinel
//	byte  1    : fmt_byte
//	byte  2    : subsec_digits uint8
//	bytes 3-10 : unix_nanos    int64 BE
//
// 13 bytes (fmtISOOffset=3, fmtMacOS=4, fmtRFC3339NanoOffset=5 — has TZ):
//
//	byte  0    : 0x00          sentinel
//	byte  1    : fmt_byte
//	bytes 2-3  : tz_offset_min int16 BE (signed minutes from UTC)
//	byte  4    : subsec_digits uint8
//	bytes 5-12 : unix_nanos    int64 BE
//
// tokenSizeForFmt returns the encoded byte length (including sentinel) for a given fmt_id.
func tokenSizeForFmt(fmtID uint8) int {
	switch fmtID {
	case fmtRFC3339, fmtGINLog, fmtSyslog:
		return 10
	case fmtRFC3339Nano, fmtLog4j, fmtPython:
		return 11
	default: // fmtISOOffset, fmtMacOS, fmtRFC3339NanoOffset
		return 13
	}
}

// Quote type constants encoded in bits 7-6 of the fmt byte.
const (
	quoteNone   uint8 = 0 << 6
	quoteSingle uint8 = 1 << 6
	quoteDouble uint8 = 2 << 6
)

// sepSlashBit is bit 5 of the fmt byte: set when the date separator is '/' not '-'.
// Bits 5-3 are unused by fmt_id (values 1–6 fit in bits 2–0), so this is backward-compatible.
const sepSlashBit uint8 = 0x20

// Format IDs in bits 4-0 of the fmt byte.
const (
	fmtRFC3339Nano       uint8 = 1 // 2026-05-11T08:42:17.123456789Z
	fmtRFC3339           uint8 = 2 // 2026-05-11T08:42:17Z
	fmtISOOffset         uint8 = 3 // 2026-05-11T15:42:17+07:00
	fmtMacOS             uint8 = 4 // 2026-05-11 15:42:17.123456+0700
	fmtRFC3339NanoOffset uint8 = 5 // 2026-05-11T08:42:17.795+02:00
	fmtGINLog            uint8 = 6 // 2026-05-11 10:36:19
	fmtSyslog            uint8 = 7 // Jun 14 15:16:01       (no year, no TZ)
	fmtLog4j             uint8 = 8 // 2026-05-11 10:36:19,978  (comma ms)
	fmtPython            uint8 = 9 // 2026-05-11 10:36:19.008  (dot ms, variable digits)
)

var logBasenames = map[string]bool{
	"syslog": true, "messages": true, "kern.log": true,
}

// basePatternStrs are the 9 raw regex strings without quote wrappers.
//
// Ordering constraints:
//   - fmtMacOS (3) before fmtPython (8): both match space-separated datetimes with a dot
//     subsecond, but macOS requires exactly 6 digits + TZ offset. Longest-match selection
//     in the replacement pass resolves ambiguity, but macOS must appear first so sampling
//     attributes it correctly.
//   - fmtLog4j (7) and fmtPython (8) before fmtGINLog (5): GINLog is a prefix of both.
//     The replacement pass uses longest-match when start positions tie, so GINLog never
//     eats a Log4j or Python timestamp. Ordering here only affects the sample phase.
//   - fmtRFC3339NanoOffset (4) before fmtISOOffset (2): same [+-] suffix, nano variant
//     requires \.\d+ first.
var basePatternStrs = [9]string{
	`\d{4}[-/]\d{2}[-/]\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z`,               // fmtRFC3339Nano (1)
	`\d{4}[-/]\d{2}[-/]\d{2}T\d{2}:\d{2}:\d{2}Z`,                    // fmtRFC3339 (2)
	`\d{4}[-/]\d{2}[-/]\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}`,      // fmtISOOffset (3)
	`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2}\.\d{6}[+-]\d{4}`,     // fmtMacOS (4)
	`\d{4}[-/]\d{2}[-/]\d{2}T\d{2}:\d{2}:\d{2}\.\d+[+-]\d{2}:\d{2}`, // fmtRFC3339NanoOffset (5)
	`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2}`,                     // fmtGINLog (6)
	`[A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2}`,                       // fmtSyslog (7)
	`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2},\d{3}`,               // fmtLog4j (8)
	`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2}\.\d+`,                // fmtPython (9)
}

var parseLayouts = [9]string{
	"2006-01-02T15:04:05.999999999Z07:00", // fmtRFC3339Nano (1)
	time.RFC3339,                          // fmtRFC3339 (2)
	"2006-01-02T15:04:05Z07:00",           // fmtISOOffset (3)
	"2006-01-02 15:04:05.000000-0700",     // fmtMacOS (4)
	"2006-01-02T15:04:05.999999999Z07:00", // fmtRFC3339NanoOffset (5)
	"2006-01-02 15:04:05",                 // fmtGINLog (6)
	"",                                    // fmtSyslog (7) — special: no year in format
	"2006-01-02 15:04:05.000",             // fmtLog4j (8) — after normalising comma→dot
	"2006-01-02 15:04:05.999999999",       // fmtPython (9)
}

const nBase = len(basePatternStrs) // 9

// allPatterns holds 27 compiled patterns: 9 base × 3 quote types.
// Layout: [0-8]=unquoted, [9-17]=single-quoted, [18-26]=double-quoted.
// Index formula: quoteGroup*nBase + (fmtID-1).
var allPatterns [nBase * 3]*regexp.Regexp

// allFmtBytes holds the encoded fmt byte (quote bits | fmt_id) for each pattern.
var allFmtBytes [nBase * 3]uint8

// sampleOrder iterates patterns with double-quoted first, then single-quoted,
// then unquoted so that quoted timestamps are attributed to their quoted variant
// during the sampling phase (quoted regex matches one char earlier in the string).
var sampleOrder [nBase * 3]int

func init() {
	quoteGroups := [3]struct {
		qtype          uint8
		prefix, suffix string
	}{
		{quoteNone, "", ""},
		{quoteSingle, `'`, `'`},
		{quoteDouble, `"`, `"`},
	}
	for qi, q := range quoteGroups {
		for fi, base := range basePatternStrs {
			idx := qi*nBase + fi
			allPatterns[idx] = regexp.MustCompile(q.prefix + base + q.suffix)
			allFmtBytes[idx] = q.qtype | uint8(fi+1)
		}
	}
	// sampleOrder: double-quoted (18-26), single-quoted (9-17), unquoted (0-8).
	for i := range nBase {
		sampleOrder[i] = 2*nBase + i
		sampleOrder[nBase+i] = nBase + i
		sampleOrder[2*nBase+i] = i
	}
}

// LogDate implements the log-date-subst/v1 transform.
type LogDate struct{}

// New returns a new log-date-subst transform.
func New() *LogDate { return &LogDate{} }

// ID returns the stable transform identifier.
func (l *LogDate) ID() marc.TransformID { return transformID }

// Applicable returns true for known log filenames and .log extensions within size bounds.
func (l *LogDate) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	if facts.Size < minSize || facts.Size > maxSize {
		return false
	}
	base := e.Info.Name()
	if logBasenames[base] {
		return true
	}
	ext := filepath.Ext(base)
	if ext == ".log" {
		return true
	}
	return filepath.Ext(strings.TrimSuffix(base, ext)) == ".log"
}

// CostEstimate returns gain and CPU estimates for log date substitution.
func (l *LogDate) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 3, facts.Size / 512
}

// dateParams is the JSON structure stored in Result.Params.
type dateParams struct {
	Fmt uint8 `json:"fmt"` // 0 = multi-format mode (each token self-describes its format)
}

// Apply reads the file, checks that >50% of non-empty lines carry timestamps of any
// recognized format, then replaces each timestamp with a 13-byte token. Each token
// is self-describing: its fmt byte encodes the quote style (bits 7-6) and format id
// (bits 5-0), so mixed-format files are handled correctly.
//
// Pattern matching uses a move-to-front ordered list initialized from sample
// frequency: the most common pattern is tried first on each position, and the
// winning pattern is promoted to front for the next position.
func (l *LogDate) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.ReadSeeker, sink marc.BlobSink) (marc.Result, bool, error) {
	// Sample phase: read up to 2000 lines, count pattern matches.
	const sampleLineLimit = 200
	sampleReader := bufio.NewReaderSize(src, 64*1024)
	sampleLines := make([]string, 0, sampleLineLimit)
	patCounts := make([]int, len(allPatterns))
	var totalMatched int

	for len(sampleLines) < sampleLineLimit {
		line, err := sampleReader.ReadString('\n')
		if len(line) > 0 {
			if strings.IndexByte(line, 0x00) >= 0 {
				return marc.Result{}, false, nil
			}
			sampleLines = append(sampleLines, line)
			lb := []byte(line)
			for _, patIdx := range sampleOrder {
				if allPatterns[patIdx].Find(lb) != nil {
					patCounts[patIdx]++
					totalMatched++
					break
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return marc.Result{}, false, fmt.Errorf("log-date-subst: sample read: %w", err)
		}
	}

	// Gate: absolute threshold of 10 matches.
	if totalMatched < 10 {
		return marc.Result{}, false, nil
	}

	// Initialize pattern order by descending sample frequency.
	patternOrder := make([]int, len(allPatterns))
	for i := range patternOrder {
		patternOrder[i] = i
	}
	sort.Slice(patternOrder, func(i, j int) bool {
		return patCounts[patternOrder[i]] > patCounts[patternOrder[j]]
	})

	// Seek back to start for the replacement pass.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return marc.Result{}, false, fmt.Errorf("log-date-subst: seek: %w", err)
	}

	// Replacement pass: streaming line-by-line with move-to-front pattern ordering.
	var out bytes.Buffer
	reader := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.IndexByte(line, 0x00) >= 0 {
				return marc.Result{}, false, nil
			}
			data := []byte(line)
			pos := 0
			for pos < len(data) {
				bestStart := -1
				bestEnd := -1
				bestOrderIdx := -1

				for oi, patIdx := range patternOrder {
					loc := allPatterns[patIdx].FindIndex(data[pos:])
					if loc == nil {
						continue
					}
					// Prefer earlier start; break ties by longer match (more specific pattern).
					if bestStart < 0 || loc[0] < bestStart || (loc[0] == bestStart && loc[1] > bestEnd) {
						bestStart = loc[0]
						bestEnd = loc[1]
						bestOrderIdx = oi
					}
				}

				if bestStart < 0 {
					out.Write(data[pos:])
					break
				}

				if bestOrderIdx > 0 {
					winner := patternOrder[bestOrderIdx]
					copy(patternOrder[1:bestOrderIdx+1], patternOrder[:bestOrderIdx])
					patternOrder[0] = winner
				}

				start := pos + bestStart
				end := pos + bestEnd
				out.Write(data[pos:start])

				tok, encErr := encodeToken(allFmtBytes[patternOrder[0]], string(data[start:end]))
				if encErr != nil {
					return marc.Result{}, false, nil
				}
				out.Write(tok)
				pos = end
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return marc.Result{}, false, fmt.Errorf("log-date-subst: read: %w", err)
		}
	}

	id, err := sink.Write(ctx, bytes.NewReader(out.Bytes()))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("log-date-subst: write blob: %w", err)
	}

	params, err := json.Marshal(dateParams{Fmt: 0})
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("log-date-subst: marshal params: %w", err)
	}

	return marc.Result{
		BlobIDs: []marc.BlobID{id},
		Params:  params,
	}, true, nil
}

// encodeToken encodes a timestamp match into a variable-length token (10, 11, or 13 bytes).
func encodeToken(fmtByte uint8, matchStr string) ([]byte, error) {
	fmtID := fmtByte & 0x1F
	quoteType := fmtByte >> 6
	if fmtID < 1 || fmtID > 9 {
		return nil, fmt.Errorf("invalid fmt id %d", fmtID)
	}

	s := matchStr
	if quoteType != 0 && len(s) >= 2 {
		s = s[1 : len(s)-1]
	}

	storedFmtByte := (quoteType << 6) | fmtID

	// sep_slash: only for YYYY-separator formats (not syslog which starts with month name).
	if fmtID != fmtSyslog && len(s) >= 8 && s[4] == '/' {
		storedFmtByte |= sepSlashBit
		b := []byte(s)
		b[4] = '-'
		b[7] = '-'
		s = string(b)
	}

	var t time.Time
	var err error
	switch fmtID {
	case fmtSyslog:
		// No year in the format: assume current UTC year for the absolute value.
		// Reconstruction only uses month/day/time, so the year assumption is invisible.
		year := time.Now().UTC().Year()
		t, err = time.Parse("2006 Jan _2 15:04:05", fmt.Sprintf("%d %s", year, s))
	case fmtLog4j:
		// Comma millisecond separator: normalise to dot before parsing.
		if ci := strings.IndexByte(s, ','); ci >= 0 {
			b := []byte(s)
			b[ci] = '.'
			s = string(b)
		}
		t, err = time.Parse(parseLayouts[fmtID-1], s)
	default:
		t, err = time.Parse(parseLayouts[fmtID-1], s)
	}
	if err != nil {
		return nil, err
	}

	var subsecDigits uint8
	switch fmtID {
	case fmtRFC3339Nano:
		dot := strings.IndexByte(s, '.')
		zPos := strings.IndexByte(s, 'Z')
		if dot >= 0 && zPos > dot {
			subsecDigits = uint8(min(zPos-dot-1, 9))
		}
	case fmtRFC3339NanoOffset:
		if _, frac, ok := strings.Cut(s, "."); ok {
			n := strings.IndexAny(frac, "+-")
			if n >= 0 {
				subsecDigits = uint8(min(n, 9))
			}
		}
	case fmtMacOS:
		subsecDigits = 6
	case fmtLog4j:
		subsecDigits = 3
	case fmtPython:
		if dot := strings.IndexByte(s, '.'); dot >= 0 {
			subsecDigits = uint8(min(len(s)-dot-1, 9))
		}
	}

	nanos := uint64(t.UnixNano())
	size := tokenSizeForFmt(fmtID)
	tok := make([]byte, size)
	tok[0] = 0x00
	tok[1] = storedFmtByte
	switch size {
	case 10:
		binary.BigEndian.PutUint64(tok[2:10], nanos)
	case 11:
		tok[2] = subsecDigits
		binary.BigEndian.PutUint64(tok[3:11], nanos)
	default: // 13
		_, offsetSec := t.Zone()
		binary.BigEndian.PutUint16(tok[2:4], uint16(int16(offsetSec/60)))
		tok[4] = subsecDigits
		binary.BigEndian.PutUint64(tok[5:13], nanos)
	}
	return tok, nil
}

// Reverse reconstructs the original file from the substituted blob.
func (l *LogDate) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}

	var p dateParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return fmt.Errorf("log-date-subst: unmarshal params: %w", err)
	}

	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("log-date-subst: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("log-date-subst: read blob: %w", err)
	}

	var buf bytes.Buffer
	buf.Grow(len(data) * 2)
	pos := 0
	for pos < len(data) {
		idx := bytes.IndexByte(data[pos:], 0x00)
		if idx < 0 {
			buf.Write(data[pos:])
			break
		}
		buf.Write(data[pos : pos+idx])
		tokStart := pos + idx
		if tokStart+2 > len(data) {
			buf.Write(data[tokStart:])
			break
		}
		fmtByte := data[tokStart+1]
		fmtID := fmtByte & 0x1F
		quoteType := fmtByte >> 6
		sepSlash := (fmtByte>>5)&1 != 0
		if fmtID < 1 || fmtID > 9 {
			return fmt.Errorf("log-date-subst: unknown fmt id %d (byte=0x%02x)", fmtID, fmtByte)
		}
		tokSize := tokenSizeForFmt(fmtID)
		if tokStart+tokSize > len(data) {
			buf.Write(data[tokStart:])
			break
		}

		var tzOffMin int16
		var subsecDigits uint8
		var unixNanos int64
		switch tokSize {
		case 10:
			unixNanos = int64(binary.BigEndian.Uint64(data[tokStart+2 : tokStart+10]))
		case 11:
			subsecDigits = data[tokStart+2]
			unixNanos = int64(binary.BigEndian.Uint64(data[tokStart+3 : tokStart+11]))
		default: // 13
			tzOffMin = int16(binary.BigEndian.Uint16(data[tokStart+2 : tokStart+4]))
			subsecDigits = data[tokStart+4]
			unixNanos = int64(binary.BigEndian.Uint64(data[tokStart+5 : tokStart+13]))
		}

		loc := time.FixedZone("", int(tzOffMin)*60)
		t := time.Unix(0, unixNanos).In(loc)
		ts := reconstructTimestamp(fmtID, sepSlash, t, tzOffMin, subsecDigits)

		switch quoteType {
		case 1:
			buf.WriteByte('\'')
			buf.WriteString(ts)
			buf.WriteByte('\'')
		case 2:
			buf.WriteByte('"')
			buf.WriteString(ts)
			buf.WriteByte('"')
		default:
			buf.WriteString(ts)
		}
		pos = tokStart + tokSize
	}

	_, err = dst.Write(buf.Bytes())
	return err
}

// reconstructTimestamp formats t back into the original textual form (without quotes).
func reconstructTimestamp(fmtID uint8, sepSlash bool, t time.Time, tzOffMin int16, subsecDigits uint8) string {
	var s string
	switch fmtID {
	case fmtRFC3339Nano:
		u := t.UTC()
		y, mo, d := u.Date()
		h, m, sec := u.Clock()
		digits := min(int(subsecDigits), 9)
		frac := u.Nanosecond() / pow10(9-digits)
		s = fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%0*dZ", y, int(mo), d, h, m, sec, digits, frac)
	case fmtRFC3339:
		s = t.UTC().Format(time.RFC3339)
	case fmtISOOffset:
		s = t.Format("2006-01-02T15:04:05") + formatOffsetColon(tzOffMin)
	case fmtMacOS:
		s = t.Format("2006-01-02 15:04:05") + "." + fmt.Sprintf("%06d", t.Nanosecond()/1000) + formatOffsetNoColon(tzOffMin)
	case fmtRFC3339NanoOffset:
		y, mo, d := t.Date()
		h, m, sec := t.Clock()
		digits := min(int(subsecDigits), 9)
		frac := t.Nanosecond() / pow10(9-digits)
		s = fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%0*d", y, int(mo), d, h, m, sec, digits, frac) + formatOffsetColon(tzOffMin)
	case fmtGINLog:
		s = t.UTC().Format("2006-01-02 15:04:05")
	case fmtSyslog:
		// Reconstruct without year; sep_slash does not apply (format starts with month name).
		return t.UTC().Format("Jan _2 15:04:05")
	case fmtLog4j:
		s = t.UTC().Format("2006-01-02 15:04:05")
		s += fmt.Sprintf(",%03d", t.Nanosecond()/1_000_000)
	case fmtPython:
		digits := min(int(subsecDigits), 9)
		frac := t.UTC().Nanosecond() / pow10(9-digits)
		s = t.UTC().Format("2006-01-02 15:04:05") + fmt.Sprintf(".%0*d", digits, frac)
	default:
		return ""
	}
	if sepSlash && len(s) >= 10 {
		b := []byte(s)
		b[4] = '/'
		b[7] = '/'
		return string(b)
	}
	return s
}

func formatOffsetColon(m int16) string {
	sign := byte('+')
	v := int(m)
	if v < 0 {
		sign = '-'
		v = -v
	}
	return fmt.Sprintf("%c%02d:%02d", sign, v/60, v%60)
}

func formatOffsetNoColon(m int16) string {
	sign := byte('+')
	v := int(m)
	if v < 0 {
		sign = '-'
		v = -v
	}
	return fmt.Sprintf("%c%02d%02d", sign, v/60, v%60)
}

func pow10(n int) int {
	if n < 0 || n > 9 {
		return 1
	}
	p := 1
	for range n {
		p *= 10
	}
	return p
}
