// Package marc transform types define the versioned, reversible content
// transformation contract used by the planner and store.
package marc

import (
	"context"
	"io"
)

// TransformID is a stable string identifier. New breaking version = new ID.
// Example: "dedup/v1" -> "dedup/v2" for a breaking change.
type TransformID string

// Facts holds catalog facts about an entry available to the planner without I/O.
type Facts struct {
	Size int64
	SHA  [32]byte // pre-computed BLAKE3-256 hash (zero if not available)
}

// Result is returned by Transform.Apply.
type Result struct {
	BlobIDs []BlobID // ordered blob references
	Params  []byte   // small (<=1KB) inline per-entry params stored in entries.params
}

// BlobID is an opaque reference to a blob in the store.
type BlobID int64

// BlobSink is the write side of the blob store, passed to Transform.Apply.
type BlobSink interface {
	// Write streams r into the store and returns the BlobID.
	// Deduplicates internally on content hash.
	Write(ctx context.Context, r io.Reader) (BlobID, error)
	// Reuse claims an existing blob by its BLAKE3-256 hash.
	Reuse(sha [32]byte) (BlobID, bool)
}

// BlobReader is the read side of the blob store, passed to Transform.Reverse.
type BlobReader interface {
	Open(id BlobID) (io.ReadCloser, error)
}

// Transform is a versioned, reversible content transformation.
type Transform interface {
	ID() TransformID

	// Applicable is a cheap predicate -- must not read file content.
	Applicable(ctx context.Context, e Entry, facts Facts) bool

	// CostEstimate returns (estimated_gain_bytes, estimated_cpu_units).
	CostEstimate(e Entry, facts Facts) (gainBytes, cpuUnits int64)

	// Apply reads src, writes blobs through sink, returns a Result.
	// The bool return means "handled": true = halt chain, false = pass to next transform.
	Apply(ctx context.Context, e Entry, facts Facts, src io.Reader, sink BlobSink) (Result, bool, error)

	// Reverse reconstructs original bytes from Result + blob access.
	Reverse(ctx context.Context, r Result, blobs BlobReader, dst io.Writer) error
}
