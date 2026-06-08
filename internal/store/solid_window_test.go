package store

import (
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestSolidWindowFor(t *testing.T) {
	tests := []struct {
		name  string
		block int64
		want  int
	}{
		{"default 16 MiB block is already a power of two", 16 << 20, 16 << 20},
		{"legacy 4 MiB block", 4 << 20, 4 << 20},
		{"non power of two rounds up to cover the block", 20 * 1000 * 1000, 32 << 20},
		{"one byte over rounds up", (16 << 20) + 1, 32 << 20},
		{"tiny block clamps to the library minimum", 512, zstd.MinWindowSize},
		{"oversized block clamps to the library maximum", 1 << 31, zstd.MaxWindowSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := solidWindowFor(tt.block)
			if got != tt.want {
				t.Fatalf("solidWindowFor(%d) = %d, want %d", tt.block, got, tt.want)
			}
			if got&(got-1) != 0 {
				t.Fatalf("solidWindowFor(%d) = %d is not a power of two", tt.block, got)
			}
			if int64(got) < tt.block && got != zstd.MaxWindowSize {
				t.Fatalf("solidWindowFor(%d) = %d does not cover the block", tt.block, got)
			}
			// The value must be accepted by the encoder option it feeds.
			if _, err := zstd.NewWriter(nil, zstd.WithWindowSize(got)); err != nil {
				t.Fatalf("zstd rejected window %d: %v", got, err)
			}
		})
	}
}
