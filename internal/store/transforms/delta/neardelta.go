// Package delta implements the near-dup-delta/v1 transform stub.
// This transform is gated behind a benchmark showing >10% gain over dedup alone.
// Currently returns ErrNotApplicable for all inputs.
package delta

import (
	"context"
	"io"

	"github.com/arhuman/metarc/pkg/marc"
)

const nearDupID marc.TransformID = "near-dup-delta/v1"

// NearDup is a stub for the near-dup-delta/v1 transform.
// It always returns ErrNotApplicable until the benchmark gate passes.
type NearDup struct{}

// NewNearDup returns a new near-dup-delta transform stub.
func NewNearDup() *NearDup { return &NearDup{} }

// ID returns the stable transform identifier.
func (n *NearDup) ID() marc.TransformID { return nearDupID }

// Applicable always returns false (stub).
func (n *NearDup) Applicable(_ context.Context, _ marc.Entry, _ marc.Facts) bool {
	return false
}

// CostEstimate returns zero (stub).
func (n *NearDup) CostEstimate(_ marc.Entry, _ marc.Facts) (gainBytes, cpuUnits int64) {
	return 0, 0
}

// Apply always returns ErrNotApplicable (stub).
func (n *NearDup) Apply(_ context.Context, _ marc.Entry, _ io.Reader, _ marc.BlobSink) (marc.Result, error) {
	return marc.Result{}, marc.ErrNotApplicable
}

// Reverse is a no-op (stub).
func (n *NearDup) Reverse(_ context.Context, _ marc.Result, _ marc.BlobReader, _ io.Writer) error {
	return nil
}
