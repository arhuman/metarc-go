package main

import (
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestResolveZstdLevels covers the flag-precedence rules of the per-chunk
// zstd-level CLI knobs:
//   - all unset (-1) leaves zero values for every chunk (defer to runtime defaults);
//   - --zstd-level alone sets all four chunks;
//   - per-chunk overrides take precedence over --zstd-level;
//   - out-of-range values produce a clean error.
func TestResolveZstdLevels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                                       string
		global, blob, solid, catalog, dict         int
		wantBlob, wantSolid, wantCatalog, wantDict zstd.EncoderLevel
		wantErr                                    bool
	}{
		{
			name:   "all unset leaves zero values",
			global: -1, blob: -1, solid: -1, catalog: -1, dict: -1,
		},
		{
			name:   "global only sets every chunk",
			global: 11, blob: -1, solid: -1, catalog: -1, dict: -1,
			wantBlob:    zstd.EncoderLevelFromZstd(11),
			wantSolid:   zstd.EncoderLevelFromZstd(11),
			wantCatalog: zstd.EncoderLevelFromZstd(11),
			wantDict:    zstd.EncoderLevelFromZstd(11),
		},
		{
			name:   "per-chunk override wins over global",
			global: 3, blob: 11, solid: -1, catalog: -1, dict: -1,
			wantBlob:    zstd.EncoderLevelFromZstd(11),
			wantSolid:   zstd.EncoderLevelFromZstd(3),
			wantCatalog: zstd.EncoderLevelFromZstd(3),
			wantDict:    zstd.EncoderLevelFromZstd(3),
		},
		{
			name:   "all per-chunk overrides explicit, no global",
			global: -1, blob: 7, solid: 9, catalog: 11, dict: 5,
			wantBlob:    zstd.EncoderLevelFromZstd(7),
			wantSolid:   zstd.EncoderLevelFromZstd(9),
			wantCatalog: zstd.EncoderLevelFromZstd(11),
			wantDict:    zstd.EncoderLevelFromZstd(5),
		},
		{
			name:   "global below valid range errors",
			global: 0, blob: -1, solid: -1, catalog: -1, dict: -1,
			wantErr: true,
		},
		{
			name:   "global above valid range errors",
			global: 12, blob: -1, solid: -1, catalog: -1, dict: -1,
			wantErr: true,
		},
		{
			name:   "per-chunk out of range errors",
			global: -1, blob: 99, solid: -1, catalog: -1, dict: -1,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveZstdLevels(tc.global, tc.blob, tc.solid, tc.catalog, tc.dict)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil; cfg=%+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Blob != tc.wantBlob {
				t.Errorf("Blob: got %v, want %v", got.Blob, tc.wantBlob)
			}
			if got.Solid != tc.wantSolid {
				t.Errorf("Solid: got %v, want %v", got.Solid, tc.wantSolid)
			}
			if got.Catalog != tc.wantCatalog {
				t.Errorf("Catalog: got %v, want %v", got.Catalog, tc.wantCatalog)
			}
			if got.Dict != tc.wantDict {
				t.Errorf("Dict: got %v, want %v", got.Dict, tc.wantDict)
			}
		})
	}
}

// TestZstdLevelFromInt verifies the integer-to-EncoderLevel mapping rejects
// out-of-range input and accepts every level klauspost supports.
func TestZstdLevelFromInt(t *testing.T) {
	t.Parallel()

	for _, n := range []int{minZstdLevel, 3, 7, maxZstdLevel} {
		if _, err := zstdLevelFromInt(n); err != nil {
			t.Errorf("level %d: unexpected error: %v", n, err)
		}
	}

	for _, n := range []int{-1, 0, 12, 100} {
		if _, err := zstdLevelFromInt(n); err == nil {
			t.Errorf("level %d: expected error, got nil", n)
		}
	}
}
