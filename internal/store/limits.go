package store

import (
	"fmt"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
)

// maxDecompressedSize is the size invariant of the .marc format: no stored blob
// payload and no solid block may exceed it. A blob payload here means the
// post-transform, pre-compression bytes, which is what blobs.ulen records and
// blobs.sha hashes.
//
// The write path enforces it (checkBlobPayloadSize at the blob sink, and the
// solid block size validated in OpenWriter), so an archive that violates it is
// invalid by construction. The read path relies on that: the cap applies to
// every zstd decode, the catalog chunk, a standalone blob and a solid block
// alike, it is the ceiling on any bound derived from a catalog length, and it
// is the largest buffer the read path will allocate from one.
//
// The value is marc.MaxBlobPayload, where the format states the invariant. It
// is zstd.MaxWindowSize, the widest match window cmd/metarc accepts
// (resolveZstdWindow), the widest solidWindowFor can pick, and the widest
// zstd.WithWindowSize accepts at all, so the cap cannot reject a frame a
// legitimate encoder produced.
const maxDecompressedSize = marc.MaxBlobPayload

// Compile-time assertion that maxDecompressedSize covers zstd.MaxWindowSize.
// Both write-side window bounds are expressed against the library constant, so
// lowering this one below it would let an encoder emit a frame whose declared
// window the read path then refuses.
const _ uint = maxDecompressedSize - zstd.MaxWindowSize

// checkBlobPayloadSize rejects a blob payload the format cannot store. n is the
// post-transform, pre-compression length and who names the entry it came from.
// Callers add their own site prefix to the returned error.
func checkBlobPayloadSize(who string, n int64) error {
	if n > maxDecompressedSize {
		return fmt.Errorf("payload of %s is %d bytes, over the %d byte limit", who, n, maxDecompressedSize)
	}
	return nil
}
