// Package license implements the license-canonical/v1 transform, which replaces
// recognized open-source license files with a reference to an embedded canonical
// copy, reducing redundancy when many repos share the same license.
package license

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/zeebo/blake3"
)

const canonicalID marc.TransformID = "license-canonical/v1"

// filenameRe matches license-like filenames (case-insensitive).
var filenameRe = regexp.MustCompile(`(?i)^(LICEN[CS]E|COPYING|NOTICE)(\..+)?$`)

// licenseEntry pairs an SPDX identifier with canonical text.
type licenseEntry struct {
	SPDX string
	Text string
}

// fingerprints maps BLAKE3-256 of normalized canonical text to its licenseEntry.
var fingerprints map[[32]byte]licenseEntry

// canonicalTexts is the ordered list of supported licenses.
var canonicalTexts = []licenseEntry{
	{SPDX: "MIT", Text: mitText},
	{SPDX: "Apache-2.0", Text: apache2Text},
	{SPDX: "BSD-2-Clause", Text: bsd2Text},
	{SPDX: "BSD-3-Clause", Text: bsd3Text},
	{SPDX: "ISC", Text: iscText},
}

func init() {
	fingerprints = make(map[[32]byte]licenseEntry, len(canonicalTexts))
	for _, l := range canonicalTexts {
		norm := normalize(l.Text)
		h := blake3.Sum256([]byte(norm))
		fingerprints[h] = l
	}
}

// normalize trims whitespace and replaces \r\n with \n.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// FingerprintCount returns the number of loaded fingerprints (for testing).
func FingerprintCount() int { return len(fingerprints) }

// Canonical implements the license-canonical/v1 transform.
type Canonical struct{}

// NewCanonical returns a new license-canonical transform.
func NewCanonical() *Canonical { return &Canonical{} }

// ID returns the stable transform identifier.
func (c *Canonical) ID() marc.TransformID { return canonicalID }

// Applicable checks whether the entry's filename matches a license pattern.
// No I/O is performed.
func (c *Canonical) Applicable(_ context.Context, e marc.Entry, _ marc.Facts) bool {
	base := filepath.Base(e.RelPath)
	return filenameRe.MatchString(base)
}

// CostEstimate returns optimistic gain (full file replaced by reference) and
// a fixed CPU cost for the BLAKE3 hash computation.
func (c *Canonical) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size, 512
}

// params is the JSON structure stored in Result.Params.
type params struct {
	SPDX string `json:"spdx"`
}

// Apply reads the full file content, normalizes it, and looks up its BLAKE3
// fingerprint against known license texts. If found, it writes the canonical
// text to the sink and returns a result with the SPDX identifier. If not found,
// it returns handled=false.
func (c *Canonical) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("license-canonical: read: %w", err)
	}

	norm := normalize(string(data))
	h := blake3.Sum256([]byte(norm))

	entry, ok := fingerprints[h]
	if !ok {
		return marc.Result{}, false, nil
	}

	// Write canonical (normalized) bytes to the sink.
	canonicalBytes := []byte(normalize(entry.Text))
	blobID, err := sink.Write(ctx, bytes.NewReader(canonicalBytes))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("license-canonical: write blob: %w", err)
	}

	paramsJSON, err := json.Marshal(params{SPDX: entry.SPDX})
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("license-canonical: marshal params: %w", err)
	}

	return marc.Result{
		BlobIDs: []marc.BlobID{blobID},
		Params:  paramsJSON,
	}, true, nil
}

// Reverse opens the blob referenced by r and copies it to dst.
func (c *Canonical) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}
	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("license-canonical: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(dst, rc)
	return err
}
