// Package analyze provides BLAKE3 hashing and quick-signature computation
// for content deduplication.
package analyze

import (
	"io"

	"github.com/zeebo/blake3"
)

// FullHash computes the BLAKE3-256 hash of all bytes from r.
func FullHash(r io.Reader) ([32]byte, error) {
	h := blake3.New()
	if _, err := io.Copy(h, r); err != nil {
		return [32]byte{}, err
	}
	return *(*[32]byte)(h.Sum(nil)), nil
}

const quickSigChunk = 4096

// QuickSig computes a 16-byte signature from head 4K + tail 4K of r.
// For files < 8K, hashes the full content.
// Uses BLAKE3 internally. The reader must support io.ReadSeeker for files >= 8K.
func QuickSig(r io.Reader) ([16]byte, error) {
	// Read up to 8K+1 to distinguish "exactly 8K" from "> 8K".
	buf := make([]byte, quickSigChunk*2+1)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return [16]byte{}, err
	}

	if n <= quickSigChunk*2 {
		// Small file (<= 8K): hash whatever we got.
		h := blake3.New()
		_, _ = h.Write(buf[:n])
		var sig [16]byte
		copy(sig[:], h.Sum(nil)[:16])
		return sig, nil
	}

	// Large file (> 8K): use head + tail.
	// We already have the head in buf[:quickSigChunk].
	// Try to seek for the tail.
	rs, ok := r.(io.ReadSeeker)
	if !ok {
		// Fallback for non-seekable readers: hash the head only.
		h := blake3.New()
		_, _ = h.Write(buf[:quickSigChunk])
		var sig [16]byte
		copy(sig[:], h.Sum(nil)[:16])
		return sig, nil
	}

	// Seek to end minus quickSigChunk to read the tail.
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return [16]byte{}, err
	}

	tailStart := end - quickSigChunk
	if tailStart < 0 {
		tailStart = 0
	}
	if _, err := rs.Seek(tailStart, io.SeekStart); err != nil {
		return [16]byte{}, err
	}

	tail := make([]byte, quickSigChunk)
	tn, err := io.ReadFull(rs, tail)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return [16]byte{}, err
	}

	h := blake3.New()
	_, _ = h.Write(buf[:quickSigChunk]) // head
	_, _ = h.Write(tail[:tn])           // tail
	var sig [16]byte
	copy(sig[:], h.Sum(nil)[:16])
	return sig, nil
}
