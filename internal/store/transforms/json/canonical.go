// Package json implements the json-canonical/v1 transform, which re-encodes
// JSON files in Go's canonical form (sorted keys, no whitespace) to improve
// downstream compression.
package json

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

const canonicalID marc.TransformID = "json-canonical/v1"

// maxSize is the upper bound for JSON files we attempt to canonicalize (10 MB).
const maxSize = 10 * 1024 * 1024

// Canonical implements the json-canonical/v1 transform.
type Canonical struct{}

// NewCanonical returns a new json-canonical transform.
func NewCanonical() *Canonical { return &Canonical{} }

// ID returns the stable transform identifier.
func (c *Canonical) ID() marc.TransformID { return canonicalID }

// Applicable checks whether the entry is a .json file within the size limit.
func (c *Canonical) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	if facts.Size <= 0 || facts.Size > maxSize {
		return false
	}
	return strings.EqualFold(filepath.Ext(e.RelPath), ".json")
}

// CostEstimate returns conservative gain and CPU estimates.
func (c *Canonical) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 4, facts.Size / 512
}

// Apply reads the JSON content, re-encodes it canonically, and writes to sink.
// Returns handled=false if the content is not valid JSON.
func (c *Canonical) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("json-canonical: read: %w", err)
	}

	// Parse as generic JSON value.
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return marc.Result{}, false, nil
	}

	// Re-encode canonically (sorted keys, no whitespace).
	canonical, err := json.Marshal(v)
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("json-canonical: marshal: %w", err)
	}

	id, err := sink.Write(ctx, bytes.NewReader(canonical))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("json-canonical: write blob: %w", err)
	}

	return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
}

// Reverse copies the canonical blob to dst. The canonical form IS the stored form.
func (c *Canonical) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}
	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("json-canonical: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(dst, rc)
	return err
}
