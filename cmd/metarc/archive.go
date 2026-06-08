package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
)

// minZstdLevel and maxZstdLevel are the inclusive bounds accepted by
// klauspost/compress/zstd: SpeedFastest=1 .. SpeedBestCompression=11.
const (
	minZstdLevel = 1
	maxZstdLevel = 11
)

// zstdLevelFromInt maps a 1..11 integer to a zstd.EncoderLevel. It returns an
// error for out-of-range input.
func zstdLevelFromInt(n int) (zstd.EncoderLevel, error) {
	if n < minZstdLevel || n > maxZstdLevel {
		return 0, fmt.Errorf("zstd level must be between %d and %d, got %d", minZstdLevel, maxZstdLevel, n)
	}
	return zstd.EncoderLevelFromZstd(n), nil
}

// resolveZstdLevels translates the CLI level flags into a store.ZstdConfig.
// global is the value of --zstd-level (-1 = unset). The four perChunk values
// are the per-chunk overrides (-1 = unset, takes precedence over global).
// Any chunk left unset returns 0 in that field, signalling "use the runtime
// default" downstream.
func resolveZstdLevels(global, blob, solid, catalog, dict int) (store.ZstdConfig, error) {
	var cfg store.ZstdConfig
	pick := func(perChunk int) (zstd.EncoderLevel, error) {
		if perChunk >= 0 {
			return zstdLevelFromInt(perChunk)
		}
		if global >= 0 {
			return zstdLevelFromInt(global)
		}
		return 0, nil // 0 means "leave default"
	}
	var err error
	if cfg.Blob, err = pick(blob); err != nil {
		return cfg, fmt.Errorf("--zstd-level-blob: %w", err)
	}
	if cfg.Solid, err = pick(solid); err != nil {
		return cfg, fmt.Errorf("--zstd-level-solid: %w", err)
	}
	if cfg.Catalog, err = pick(catalog); err != nil {
		return cfg, fmt.Errorf("--zstd-level-catalog: %w", err)
	}
	if cfg.Dict, err = pick(dict); err != nil {
		return cfg, fmt.Errorf("--zstd-level-dict: %w", err)
	}
	return cfg, nil
}

// resolveZstdWindow parses the --zstd-window value into a byte count suitable
// for zstd.WithWindowSize. An empty string means "unset": the solid encoder
// then defaults its window to the solid block size, and the other encoders
// keep the library default.
func resolveZstdWindow(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	n, err := parseByteSize(s)
	if err != nil {
		return 0, fmt.Errorf("--zstd-window: %w", err)
	}
	if n < zstd.MinWindowSize || n > zstd.MaxWindowSize {
		return 0, fmt.Errorf("--zstd-window: must be between %d and %d bytes, got %d", zstd.MinWindowSize, zstd.MaxWindowSize, n)
	}
	if n&(n-1) != 0 {
		return 0, fmt.Errorf("--zstd-window: must be a power of two, got %d", n)
	}
	return int(n), nil
}

// newArchiveCmd returns the `metarc archive` subcommand.
//
// Usage:
//
//	metarc archive <output.marc> <source-dir> [additional-source-dir...]
//
// When a single source is given, the archive is laid out with that source as
// its synthetic "." root (original behavior). When multiple sources are
// given, each one becomes a top-level directory inside the archive, named
// after its basename; basename collisions are rejected upfront.
func newArchiveCmd() *cobra.Command {
	var compressor string
	var keepPlanLog bool
	var explain bool
	var workers int
	var dictCompress string
	var noSolid bool
	var solidBlockSize string
	var disableTransforms []string
	var zstdLevel, zstdLevelBlob, zstdLevelSolid, zstdLevelCatalog, zstdLevelDict int
	var zstdWindow string

	cmd := &cobra.Command{
		Use:   "archive <output.marc> <source-dir> [source-dir...]",
		Short: "Create a .marc archive from one or more directories",
		Long: `Create a .marc archive from one or more source directories.

With a single source directory, the tree is archived with its root recorded
as ".", matching the original behaviour.

With multiple source directories, each source becomes a top-level directory
inside the archive, named after its basename. For example:

  metarc archive out.marc ./frontend ./backend ./docs

produces an archive containing "frontend/...", "backend/...", and "docs/..."
side by side. Extracting such an archive restores all three directories as
siblings under the destination. Two sources may not share the same basename.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			keep := keepPlanLog || explain
			zCfg, err := resolveZstdLevels(zstdLevel, zstdLevelBlob, zstdLevelSolid, zstdLevelCatalog, zstdLevelDict)
			if err != nil {
				return err
			}
			if zCfg.WindowSize, err = resolveZstdWindow(zstdWindow); err != nil {
				return err
			}
			opts := runtime.ArchiveOpts{
				DictCompress:       dictCompress,
				Workers:            workers,
				DisabledTransforms: disableTransforms,
				ZstdLevels:         zCfg,
			}
			if !noSolid {
				size, err := parseByteSize(solidBlockSize)
				if err != nil {
					return fmt.Errorf("invalid --solid-block-size %q: %w", solidBlockSize, err)
				}
				opts.SolidBlockSize = size
			}

			marcPath := args[0]
			sources := args[1:]

			if len(sources) == 1 {
				err = runtime.ArchiveWithOpts(cmd.Context(), marcPath, sources[0], compressor, keep, opts)
			} else {
				err = runtime.ArchiveMultiWithOpts(cmd.Context(), marcPath, sources, compressor, keep, opts)
			}
			if err != nil {
				return err
			}
			if explain {
				return printPlanSummary(cmd, marcPath)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&compressor, "final-compressor", "zstd", "blob compressor: zstd or none")
	cmd.Flags().BoolVar(&keepPlanLog, "keep-plan-log", false, "retain plan decisions in the archive for inspection")
	cmd.Flags().BoolVar(&explain, "explain", false, "retain and print plan decisions after archive")
	cmd.Flags().IntVar(&workers, "workers", 0, "number of analysis workers (default: runtime.NumCPU())")
	cmd.Flags().StringVar(&dictCompress, "dict-compress", "", `dictionary compression mode: "prescan" (walk tree first) or "simple" (train mid-stream)`)
	cmd.Flags().BoolVar(&noSolid, "no-solid", false, "disable solid block compression (use per-blob compression)")
	cmd.Flags().StringVar(&solidBlockSize, "solid-block-size", "16MB", "solid block size threshold")
	cmd.Flags().StringSliceVar(&disableTransforms, "disable-transform", nil, `transform IDs to skip (e.g. "go-line-subst/v1")`)

	cmd.Flags().IntVar(&zstdLevel, "zstd-level", -1, "zstd encoder level (1..11) applied to all chunk types; per-chunk overrides take precedence")
	cmd.Flags().IntVar(&zstdLevelBlob, "zstd-level-blob", -1, "zstd encoder level for per-blob chunks (1..11); overrides --zstd-level")
	cmd.Flags().IntVar(&zstdLevelSolid, "zstd-level-solid", -1, "zstd encoder level for solid blocks (1..11); overrides --zstd-level")
	cmd.Flags().IntVar(&zstdLevelCatalog, "zstd-level-catalog", -1, "zstd encoder level for the catalog chunk (1..11); overrides --zstd-level")
	cmd.Flags().IntVar(&zstdLevelDict, "zstd-level-dict", -1, "zstd encoder level for dictionary build/encode (1..11); overrides --zstd-level")
	cmd.Flags().StringVar(&zstdWindow, "zstd-window", "", "zstd match window, power of two (e.g. 32MB); default: the solid block size")

	return cmd
}

// printPlanSummary prints a summary of the plan_log table using store.OpenReader
// to correctly read the catalog from single-file archives.
func printPlanSummary(cmd *cobra.Command, marcPath string) error {
	r, err := store.OpenReader(marcPath)
	if err != nil {
		return fmt.Errorf("open archive for plan summary: %w", err)
	}
	defer func() { _ = r.Close() }()

	stats, err := r.QueryPlanLog()
	if err != nil {
		return fmt.Errorf("query plan summary: %w", err)
	}

	var total, applied, totalGain int64
	for _, s := range stats {
		total += s.Applied + s.Skipped
		applied += s.Applied
		totalGain += s.EstimatedGain
	}

	cmd.Printf("\n--- Plan Summary ---\n")
	cmd.Printf("Total entries:      %d\n", total)
	cmd.Printf("Transforms applied: %d\n", applied)
	cmd.Printf("Estimated gain:     %s\n", formatBytes(totalGain))

	cmd.Printf("\nBreakdown by transform:\n")
	for _, s := range stats {
		cmd.Printf("  %-20s %5d applied  %5d skipped  ~%s saved\n",
			s.TransformID, s.Applied, s.Skipped, formatBytes(s.EstimatedGain))
	}
	return nil
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parseByteSize parses a human-readable byte size like "4MB", "16mb", "1024".
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	upper := strings.ToUpper(s)
	multiplier := int64(1)
	numStr := s

	switch {
	case strings.HasSuffix(upper, "GB"):
		multiplier = 1024 * 1024 * 1024
		numStr = s[:len(s)-2]
	case strings.HasSuffix(upper, "MB"):
		multiplier = 1024 * 1024
		numStr = s[:len(s)-2]
	case strings.HasSuffix(upper, "KB"):
		multiplier = 1024
		numStr = s[:len(s)-2]
	}

	n, err := strconv.ParseInt(strings.TrimSpace(numStr), 10, 64)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	return n * multiplier, nil
}
