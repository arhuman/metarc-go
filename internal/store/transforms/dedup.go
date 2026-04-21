// Package transforms contains concrete Transform implementations.
package transforms

import (
	"context"
	"io"

	"github.com/arhuman/metarc-go/pkg/marc"
)

const dedupID marc.TransformID = "dedup/v1"

// Dedup is a whole-file deduplication transform. It delegates hash-based dedup
// to the BlobSink, which deduplicates internally on content hash.
type Dedup struct{}

// NewDedup returns a new Dedup transform.
func NewDedup() *Dedup { return &Dedup{} }

// ID returns the stable transform identifier.
func (d *Dedup) ID() marc.TransformID { return dedupID }

// Applicable returns true for any regular file with size > 0.
// Must not read file content.
func (d *Dedup) Applicable(_ context.Context, _ marc.Entry, facts marc.Facts) bool {
	return facts.Size > 0
}

// CostEstimate returns optimistic gain (full file size) and hashing cost.
func (d *Dedup) CostEstimate(e marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size, facts.Size / 1024
}

// Apply tries to dedup via the pre-computed SHA (zero I/O). If no dedup hit,
// it returns handled=false to let the next transform in the chain try.
func (d *Dedup) Apply(_ context.Context, _ marc.Entry, facts marc.Facts, _ io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	zeroSHA := [32]byte{}
	if facts.SHA != zeroSHA {
		if id, ok := sink.Reuse(facts.SHA); ok {
			return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
		}
	}
	// Not a duplicate -- don't handle, let the next transform try.
	return marc.Result{}, false, nil
}

// Reverse opens the single blob referenced by r and copies it to dst.
func (d *Dedup) Reverse(ctx context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}
	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(dst, rc)
	return err
}
