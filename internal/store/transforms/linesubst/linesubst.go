// Package linesubst implements the shared line-level substitution engine
// used by the per-language transforms (go-line-subst, py-line-subst,
// js-line-subst). Each per-language wrapper supplies an ID, a static
// dictionary of frequent lines, and an Applicable predicate; this package
// owns the encode/decode logic, the bufio processing loop, and the marc
// Transform method set.
//
// Encoding: each dictionary-matched line is replaced with a 2-byte token
// (\x00 + 1-byte index). The encoded byte skips 0x00 (marker) and 0x0a
// (newline) so it never conflicts with the line delimiter used by
// bufio.Reader.ReadString.
package linesubst

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// LineSubst is a reusable Transform implementation parameterised by a
// dictionary and an Applicable predicate.
type LineSubst struct {
	id           marc.TransformID
	dict         []string
	dictLookup   map[string]byte
	applicableFn func(marc.Entry, marc.Facts) bool
}

// New returns a LineSubst configured with the given ID, dictionary, and
// applicable predicate. The dictionary is captured by reference (callers
// must not mutate it after construction). Maximum dictionary size is 254
// entries (one byte minus the two reserved values 0x00 and 0x0a).
func New(id marc.TransformID, dict []string, applicableFn func(marc.Entry, marc.Facts) bool) *LineSubst {
	lookup := make(map[string]byte, len(dict))
	for i, s := range dict {
		lookup[s] = EncodeIndex(i)
	}
	return &LineSubst{
		id:           id,
		dict:         dict,
		dictLookup:   lookup,
		applicableFn: applicableFn,
	}
}

// EncodeIndex maps a dictionary index to an encoded byte, skipping 0x00
// (marker) and 0x0a (newline).
func EncodeIndex(i int) byte {
	b := i + 1 // skip 0x00
	if b >= 0x0a {
		b++ // skip 0x0a
	}
	return byte(b)
}

// DecodeIndex maps an encoded byte back to a dictionary index.
func DecodeIndex(b byte) int {
	i := int(b)
	if i > 0x0a {
		i-- // undo 0x0a skip
	}
	i-- // undo 0x00 skip
	return i
}

// ID returns the stable transform identifier.
func (ls *LineSubst) ID() marc.TransformID { return ls.id }

// Applicable delegates to the wrapper's predicate.
func (ls *LineSubst) Applicable(_ context.Context, e marc.Entry, f marc.Facts) bool {
	return ls.applicableFn(e, f)
}

// CostEstimate returns estimated gain (10% of file size) and CPU cost
// (linear in file size). Mirrors the per-language defaults.
func (ls *LineSubst) CostEstimate(_ marc.Entry, f marc.Facts) (gainBytes, cpuUnits int64) {
	return f.Size / 10, f.Size / 1024
}

// Apply reads src line-by-line, replacing dictionary-matched lines with
// 2-byte tokens (\x00 + index). The result is written as a single blob.
// Returns handled=false if the content contains NUL bytes.
func (ls *LineSubst) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	reader := bufio.NewReaderSize(src, 64*1024)
	var buf bytes.Buffer

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.ContainsRune(line, 0x00) {
				return marc.Result{}, false, nil
			}

			hasNewline := strings.HasSuffix(line, "\n")
			content := line
			if hasNewline {
				content = line[:len(line)-1]
			}

			stripped := strings.TrimLeft(content, "\t ")
			prefix := content[:len(content)-len(stripped)]

			if idx, ok := ls.dictLookup[stripped]; ok {
				buf.WriteString(prefix)
				buf.WriteByte(0x00)
				buf.WriteByte(idx)
			} else {
				buf.WriteString(content)
			}
			if hasNewline {
				buf.WriteByte('\n')
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return marc.Result{}, false, fmt.Errorf("%s: read: %w", ls.id, err)
		}
	}

	id, err := sink.Write(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("%s: write blob: %w", ls.id, err)
	}

	return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
}

// Reverse reconstructs the original file from the substituted blob.
func (ls *LineSubst) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}

	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("%s: open blob: %w", ls.id, err)
	}
	defer func() { _ = rc.Close() }()

	reader := bufio.NewReaderSize(rc, 64*1024)
	w := bufio.NewWriter(dst)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			hasNewline := strings.HasSuffix(line, "\n")
			content := line
			if hasNewline {
				content = line[:len(line)-1]
			}

			if idx := strings.IndexByte(content, 0x00); idx >= 0 {
				prefix := content[:idx]
				if idx+1 < len(content) {
					dictIdx := DecodeIndex(content[idx+1])
					if dictIdx >= 0 && dictIdx < len(ls.dict) {
						if _, err := w.WriteString(prefix); err != nil {
							return err
						}
						if _, err := w.WriteString(ls.dict[dictIdx]); err != nil {
							return err
						}
					} else {
						if _, err := w.WriteString(content); err != nil {
							return err
						}
					}
				} else {
					if _, err := w.WriteString(content); err != nil {
						return err
					}
				}
			} else {
				if _, err := w.WriteString(content); err != nil {
					return err
				}
			}
			if hasNewline {
				if err := w.WriteByte('\n'); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: read blob: %w", ls.id, err)
		}
	}

	return w.Flush()
}
