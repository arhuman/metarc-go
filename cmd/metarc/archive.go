package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/spf13/cobra"
)

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
			opts := runtime.ArchiveOpts{
				DictCompress:       dictCompress,
				Workers:            workers,
				DisabledTransforms: disableTransforms,
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

			var err error
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
	cmd.Flags().StringVar(&solidBlockSize, "solid-block-size", "4MB", "solid block size threshold")
	cmd.Flags().StringSliceVar(&disableTransforms, "disable-transform", nil, `transform IDs to skip (e.g. "go-line-subst/v1")`)

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

	var total, applied int64
	for _, s := range stats {
		total += s.Applied + s.Skipped
		applied += s.Applied
	}

	cmd.Printf("\n--- Plan Summary ---\n")
	cmd.Printf("Total entries:      %d\n", total)
	cmd.Printf("Transforms applied: %d\n", applied)

	cmd.Printf("\nBreakdown by transform:\n")
	for _, s := range stats {
		cmd.Printf("  %-20s %5d applied  %5d skipped\n", s.TransformID, s.Applied, s.Skipped)
	}
	return nil
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
