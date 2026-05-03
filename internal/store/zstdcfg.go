package store

import "github.com/klauspost/compress/zstd"

// ZstdConfig holds per-chunk-type zstd encoder settings used by the Writer.
//
// Each field corresponds to a distinct encoding site in the archive pipeline:
//   - Blob: per-blob compression (sink.go).
//   - Solid: solid block frame compression (solid.go).
//   - Catalog: catalog SQLite chunk compression (store.go finalize).
//   - Dict: dictionary training level and dict-encoded blob compression
//     (dict.go BuildDict / sink.go dict encoder).
//
// WindowSize is forwarded to zstd.WithWindowSize when non-zero; 0 keeps the
// library default. Klauspost's zstd accepts levels 1..11 (SpeedFastest ..
// SpeedBestCompression).
type ZstdConfig struct {
	Blob       zstd.EncoderLevel
	Solid      zstd.EncoderLevel
	Catalog    zstd.EncoderLevel
	Dict       zstd.EncoderLevel
	WindowSize int
}

// DefaultZstdConfig returns the default per-chunk-type zstd configuration.
//
// All four chunk types default to zstd.SpeedDefault (level 3) — the same
// effective behavior as before this knob existed. Level 11 was tested
// across blob, solid, catalog, and dict; the ratio gains were small or
// non-existent at the catalog size produced by typical archives, and the
// encode-time cost on the hot path was unacceptable.
//
// Override individual chunks via --zstd-level{,-blob,-solid,-catalog,-dict}.
func DefaultZstdConfig() ZstdConfig {
	return ZstdConfig{
		Blob:    zstd.SpeedDefault,
		Solid:   zstd.SpeedDefault,
		Catalog: zstd.SpeedDefault,
		Dict:    zstd.SpeedDefault,
	}
}
