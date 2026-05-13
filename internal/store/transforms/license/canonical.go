// Package license implements the license-canonical/v1 transform, which replaces
// recognized open-source license files with a reference to an embedded canonical
// copy plus a compact Myers diff, enabling lossless reconstruction.
package license

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arhuman/metarc-go/internal/diff/linediff"
	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/zeebo/blake3"
)

const canonicalID marc.TransformID = "license-canonical/v1"

// filenameRe matches license-like filenames (case-insensitive).
var filenameRe = regexp.MustCompile(`(?i)^(LICEN[CS]E|COPYING|NOTICE)(\..+)?$`)

// copyrightRe matches copyright declaration lines (not in-body mentions).
var copyrightRe = regexp.MustCompile(`(?i)^\s*copyright`)

// maxParamsBytes is the safety limit for serialized params.
const maxParamsBytes = 900

// licenseEntry pairs an SPDX identifier with canonical text.
type licenseEntry struct {
	SPDX string
	Text string
}

// fingerprints maps BLAKE3-256 of normalized canonical text to its licenseEntry.
// Used for the fast path (exact match).
var fingerprints map[[32]byte]licenseEntry

// bodyFingerprints maps BLAKE3-256 of the normalized body (copyright lines
// stripped) to its licenseEntry. Used when the file differs only in the
// copyright line.
var bodyFingerprints map[[32]byte]licenseEntry

// spdxIndex maps SPDX ID to its licenseEntry for O(1) lookup during Reverse.
var spdxIndex map[string]licenseEntry

// canonicalTexts is the ordered list of supported licenses.
// Internal SPDX IDs that are not real SPDX identifiers (Apache-2.0-curly,
// BSD-3-Clause-Go-LLC, BSD-3-Clause-Go-Inc) are used only for reconstruction
// via spdxIndex and are never exposed to users.
var canonicalTexts = []licenseEntry{
	{SPDX: "MIT", Text: mitText},
	{SPDX: "Apache-2.0", Text: apache2Text},
	{SPDX: "Apache-2.0-curly", Text: apache2CurlyText},
	{SPDX: "BSD-2-Clause", Text: bsd2Text},
	{SPDX: "BSD-3-Clause", Text: bsd3Text},
	{SPDX: "BSD-3-Clause-Go-LLC", Text: bsd3GoLLCText},
	{SPDX: "BSD-3-Clause-Go-Inc", Text: bsd3GoIncText},
	{SPDX: "ISC", Text: iscText},
}

func init() {
	fingerprints = make(map[[32]byte]licenseEntry, len(canonicalTexts))
	bodyFingerprints = make(map[[32]byte]licenseEntry, len(canonicalTexts))
	spdxIndex = make(map[string]licenseEntry, len(canonicalTexts))
	for _, l := range canonicalTexts {
		norm := normalize(l.Text)
		h := blake3.Sum256([]byte(norm))
		fingerprints[h] = l

		body := stripCopyrightLines(strings.Split(norm, "\n"))
		bh := blake3.Sum256([]byte(strings.Join(body, "\n")))
		bodyFingerprints[bh] = l

		spdxIndex[l.SPDX] = l
	}
}

// normalize trims whitespace and replaces \r\n with \n.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// stripCopyrightLines removes lines that start with "Copyright" (case-insensitive).
func stripCopyrightLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if !copyrightRe.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

// FingerprintCount returns the number of loaded exact fingerprints (for testing).
func FingerprintCount() int { return len(fingerprints) }

// BodyFingerprintCount returns the number of loaded body fingerprints (for testing).
func BodyFingerprintCount() int { return len(bodyFingerprints) }

// Canonical implements the license-canonical/v1 transform.
type Canonical struct{}

// NewCanonical returns a new license-canonical transform.
func NewCanonical() *Canonical { return &Canonical{} }

// ID returns the stable transform identifier.
func (c *Canonical) ID() marc.TransformID { return canonicalID }

// Applicable checks whether the entry's filename matches a license pattern.
func (c *Canonical) Applicable(_ context.Context, e marc.Entry, _ marc.Facts) bool {
	base := filepath.Base(e.RelPath)
	return filenameRe.MatchString(base)
}

// CostEstimate returns the gain and CPU cost.
// Gain is the full file size: unlike blob transforms, license-canonical writes
// zero bytes to the blob region — the canonical text is embedded in the binary
// and only a compact params record (≤900 B) goes into the compressed catalog,
// whose marginal cost is negligible (~10–30 B after catalog-level zstd).
// CPU cost is low: one BLAKE3 body-hash lookup + a short Myers diff.
func (c *Canonical) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size, 512
}

// params is the JSON structure stored in Result.Params.
//
// Leading/Trailing capture the exact whitespace bytes that normalize()
// trims off the head and tail of the source content, so reverse can
// reconstruct byte-identical output even when the trim collapsed multiple
// newlines (e.g. a file ending with "\n\n").
//
// TrailingNL is the legacy field (true = exactly one trailing newline). It
// is read-only — kept for back-compat with archives produced by metarc
// versions before this fix; never written by current code.
type params struct {
	SPDX       string   `json:"spdx"`
	Ops        []diffOp `json:"ops,omitempty"`
	Leading    string   `json:"lead,omitempty"`
	Trailing   string   `json:"tail,omitempty"`
	TrailingNL bool     `json:"nl,omitempty"`
}

// diffOp is a compact representation of a single diff operation.
type diffOp struct {
	Kind  string `json:"k"`           // "=", "+", "-"
	Count int    `json:"n,omitempty"` // number of consecutive equal lines
	Line  string `json:"l,omitempty"` // line text for insert/delete
}

// trimWhitespace returns (leading, body, trailing) where leading + body +
// trailing == s and body has no leading/trailing whitespace. Mirrors what
// normalize() removes, so the trimmed bytes can be preserved in params.
func trimWhitespace(s string) (leading, body, trailing string) {
	body = strings.TrimSpace(s)
	if body == "" {
		return s, "", ""
	}
	leading, trailing, _ = strings.Cut(s, body)
	return leading, body, trailing
}

// Apply reads the full file content, normalizes it, and attempts to match it
// against known license templates. On exact match, no diff is stored. On
// body-hash match (differing only in copyright line), a compact Myers diff
// is stored in Params for lossless reconstruction.
func (c *Canonical) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.ReadSeeker, sink marc.BlobSink) (marc.Result, bool, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("license-canonical: read: %w", err)
	}

	// Capture exact leading/trailing whitespace before normalize() trims it.
	// normalize() does CRLF→LF then TrimSpace; capture matches that order.
	withCRLF := strings.ReplaceAll(string(data), "\r\n", "\n")
	leading, norm, trailing := trimWhitespace(withCRLF)
	h := blake3.Sum256([]byte(norm))

	// Fast path: exact match against canonical template (including placeholders).
	if entry, ok := fingerprints[h]; ok {
		return c.writeResult(entry, leading, trailing, nil)
	}

	// Body-hash path: strip copyright lines, hash the body, look up.
	lines := strings.Split(norm, "\n")
	body := stripCopyrightLines(lines)
	bh := blake3.Sum256([]byte(strings.Join(body, "\n")))

	entry, ok := bodyFingerprints[bh]
	if !ok {
		return marc.Result{}, false, nil
	}

	// Compute Myers diff: template → actual file.
	templateNorm := normalize(entry.Text)
	templateLines := strings.Split(templateNorm, "\n")
	ops := linediff.Diff(templateLines, lines)

	// Compact the diff for storage.
	compact := compactOps(ops)

	// Check params size safety.
	p := params{SPDX: entry.SPDX, Ops: compact, Leading: leading, Trailing: trailing}
	paramsJSON, err := json.Marshal(p)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("license-canonical: marshal params: %w", err)
	}
	if len(paramsJSON) > maxParamsBytes {
		return marc.Result{}, false, nil // diff too large, not a real match
	}

	return c.writeResult(entry, leading, trailing, paramsJSON)
}

// writeResult returns the transform result. The canonical text is embedded in
// the binary (spdxIndex), so no blob is written — only SPDX + diff in Params.
func (c *Canonical) writeResult(entry licenseEntry, leading, trailing string, paramsJSON []byte) (marc.Result, bool, error) {
	if paramsJSON == nil {
		var marshalErr error
		paramsJSON, marshalErr = json.Marshal(params{SPDX: entry.SPDX, Leading: leading, Trailing: trailing})
		if marshalErr != nil {
			return marc.Result{}, false, fmt.Errorf("license-canonical: marshal params: %w", marshalErr)
		}
	}
	return marc.Result{Params: paramsJSON}, true, nil
}

// Reverse reconstructs the original file from the SPDX ID and diff ops stored
// in Params. The canonical text is looked up from the embedded spdxIndex.
//
// Legacy path: archives produced before this fix stored the canonical text as
// a blob (BlobIDs non-empty). Those archives are still handled by reading the
// blob directly, so old .marc files remain extractable.
func (c *Canonical) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	var p params
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return fmt.Errorf("license-canonical: unmarshal params: %w", err)
	}

	var templateText string
	if len(r.BlobIDs) > 0 {
		// Legacy: canonical text was written as a blob.
		rc, err := blobs.Open(r.BlobIDs[0])
		if err != nil {
			return fmt.Errorf("license-canonical: open blob: %w", err)
		}
		defer func() { _ = rc.Close() }()
		data, err := io.ReadAll(rc)
		if err != nil {
			return fmt.Errorf("license-canonical: read blob: %w", err)
		}
		templateText = string(data)
	} else {
		entry, ok := spdxIndex[p.SPDX]
		if !ok {
			return fmt.Errorf("license-canonical: unknown SPDX ID %q", p.SPDX)
		}
		templateText = normalize(entry.Text)
	}

	// Reconstruct original content.
	var out string
	if len(p.Ops) == 0 {
		out = templateText
	} else {
		templateLines := strings.Split(templateText, "\n")
		ops := expandOps(p.Ops)
		original, applyErr := linediff.Apply(templateLines, ops)
		if applyErr != nil {
			return fmt.Errorf("license-canonical: apply diff: %w", applyErr)
		}
		out = strings.Join(original, "\n")
	}

	// Restore exact whitespace. New archives store Leading/Trailing; old
	// archives only have the legacy TrailingNL bool which means "exactly
	// one trailing newline" — preserve that exact semantic.
	if p.Leading != "" || p.Trailing != "" {
		out = p.Leading + out + p.Trailing
	} else if p.TrailingNL {
		out += "\n"
	}
	_, err := io.WriteString(dst, out)
	return err
}

// compactOps converts linediff.Op slice to the compact JSON-friendly form.
func compactOps(ops []linediff.Op) []diffOp {
	var result []diffOp
	for _, op := range ops {
		switch op.Kind {
		case linediff.Equal:
			result = append(result, diffOp{Kind: "=", Count: len(op.Lines)})
		case linediff.Insert:
			for _, line := range op.Lines {
				result = append(result, diffOp{Kind: "+", Line: line})
			}
		case linediff.Delete:
			for _, line := range op.Lines {
				result = append(result, diffOp{Kind: "-", Line: line})
			}
		}
	}
	return result
}

// expandOps converts the compact form back to linediff.Op slice.
func expandOps(compact []diffOp) []linediff.Op {
	var ops []linediff.Op
	for _, d := range compact {
		switch d.Kind {
		case "=":
			// Equal ops carry a count but no line text; Apply reads from base.
			ops = append(ops, linediff.Op{Kind: linediff.Equal, Lines: make([]string, d.Count)})
		case "+":
			ops = append(ops, linediff.Op{Kind: linediff.Insert, Lines: []string{d.Line}})
		case "-":
			ops = append(ops, linediff.Op{Kind: linediff.Delete, Lines: []string{d.Line}})
		}
	}
	return ops
}
