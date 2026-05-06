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
// Defaults were picked from sweep data on real-world corpora (see
// docs/tune-results/) using scripts/tune_zstd.sh:
//
//   - Blob: zstd.SpeedDefault (level 3). Most corpora pack every blob into
//     solid blocks, so this path rarely runs; raising the level is wasted
//     CPU on the hot path when it fires.
//   - Solid: zstd.SpeedBestCompression (level 11). The bulk of the bytes
//     flow through here, and level 11 is where the real ratio wins live
//     (-6.81% on kubernetes vs default; archive still lands ~30% under
//     tar+zstd's wall-clock). For users prioritizing encode speed,
//     `--zstd-level-solid 7` keeps about half the ratio gain (-3.83% on
//     kubernetes) at roughly 2× faster solid compression — a strong
//     trade if you'd rather not pay the level-11 CPU bill.
//   - Catalog: zstd.SpeedBetterCompression (level 7). The catalog is
//     small and compressed once per archive; level 7 captures nearly all
//     of level 11's gain (-0.59% vs -0.86% on kubernetes) for negligible
//     extra time (~+0.6%).
//   - Dict: zstd.SpeedDefault (level 3). Active only under opt-in
//     `--dict-compress`, where it sits on the hot path. Users who turn
//     dict-compress on can override with `--zstd-level-dict 11`.
//
// Override individual chunks via --zstd-level{,-blob,-solid,-catalog,-dict}.
func DefaultZstdConfig() ZstdConfig {
	return ZstdConfig{
		Blob:    zstd.SpeedDefault,
		Solid:   zstd.SpeedBestCompression,
		Catalog: zstd.SpeedBetterCompression,
		Dict:    zstd.SpeedDefault,
	}
}

// ResolveZstdConfig fills any zero field in user with the corresponding
// default from DefaultZstdConfig. WindowSize=0 means "library default"
// and is left as-is.
func ResolveZstdConfig(user ZstdConfig) ZstdConfig {
	def := DefaultZstdConfig()
	if user.Blob == 0 {
		user.Blob = def.Blob
	}
	if user.Solid == 0 {
		user.Solid = def.Solid
	}
	if user.Catalog == 0 {
		user.Catalog = def.Catalog
	}
	if user.Dict == 0 {
		user.Dict = def.Dict
	}
	return user
}
