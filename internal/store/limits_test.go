package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestBlobDecoderMemory pins the read-side bound derived from a catalog ulen.
// Both ends matter: the floor keeps a small blob above the smallest legal zstd
// window, and the ceiling keeps a forged ulen from restoring the library's
// 64 GiB default.
func TestBlobDecoderMemory(t *testing.T) {
	tests := []struct {
		name string
		ulen int64
		want uint64
	}{
		{name: "zero clamps up to the minimum window", ulen: 0, want: zstd.MinWindowSize},
		{name: "negative clamps up to the minimum window", ulen: -1, want: zstd.MinWindowSize},
		{name: "just below the minimum window", ulen: zstd.MinWindowSize - 1, want: zstd.MinWindowSize},
		{name: "exactly the minimum window", ulen: zstd.MinWindowSize, want: zstd.MinWindowSize},
		{name: "ordinary blob passes through", ulen: 64 << 10, want: 64 << 10},
		{name: "exactly at the payload cap", ulen: maxDecompressedSize, want: maxDecompressedSize},
		{name: "one byte over the cap clamps down", ulen: maxDecompressedSize + 1, want: maxDecompressedSize},
		{name: "forged ulen clamps down", ulen: 1 << 62, want: maxDecompressedSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blobDecoderMemory(tt.ulen); got != tt.want {
				t.Errorf("blobDecoderMemory(%d) = %d, want %d", tt.ulen, got, tt.want)
			}
		})
	}
}

// TestCheckBlobPayloadSize exercises the write-side cap at its seam: the helper
// takes a length, so the boundary can be pinned without allocating anything
// near it.
func TestCheckBlobPayloadSize(t *testing.T) {
	tests := []struct {
		name    string
		n       int64
		wantErr bool
	}{
		{name: "empty payload", n: 0},
		{name: "ordinary payload", n: 4096},
		{name: "exactly at the limit", n: maxDecompressedSize},
		{name: "one byte over the limit", n: maxDecompressedSize + 1, wantErr: true},
		{name: "far over the limit", n: 1 << 40, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBlobPayloadSize("sub/big.bin", tt.n)
			if tt.wantErr == (err == nil) {
				t.Fatalf("checkBlobPayloadSize(%d) = %v, wantErr %v", tt.n, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), "sub/big.bin") {
				t.Errorf("error %q does not name the entry", err)
			}
			if !strings.Contains(err.Error(), "536870912") {
				t.Errorf("error %q does not state the limit", err)
			}
		})
	}
}

// TestOpenWriter_solidBlockSizeOverLimit pins the early rejection of a solid
// block size the read path would refuse to decode.
func TestOpenWriter_solidBlockSizeOverLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "over.marc")

	w, err := OpenWriter(path, WithSolidBlockSize(maxDecompressedSize+1))
	if err == nil {
		_ = w.Close()
		t.Fatal("OpenWriter accepted a solid block size over the limit")
	}
	if !strings.Contains(err.Error(), "solid block size") {
		t.Fatalf("error %q does not name the offending option", err)
	}
	if !strings.Contains(err.Error(), "536870912") {
		t.Fatalf("error %q does not state the limit", err)
	}
}

// TestOpenWriter_solidBlockSizeAtLimit checks the rejection is not off by one:
// the cap itself is a legal configuration. Nothing of that size is written, so
// the accumulator never allocates its block.
func TestOpenWriter_solidBlockSizeAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlimit.marc")

	w, err := OpenWriter(path, WithSolidBlockSize(maxDecompressedSize))
	if err != nil {
		t.Fatalf("OpenWriter rejected the limit itself: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
