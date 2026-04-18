package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/arhuman/metarc/internal/store"
	"github.com/arhuman/metarc/internal/runtime"
	"github.com/spf13/cobra"
)

// newArchiveCmd returns the `metarc archive` subcommand.
// Usage: metarc archive <output.marc> <source-dir>
func newArchiveCmd() *cobra.Command {
	var compressor string
	var keepPlanLog bool
	var explain bool
	var workers int
	var dictCompress string
	var noSolid bool
	var solidBlockSize string

	cmd := &cobra.Command{
		Use:   "archive <output.marc> <source-dir>",
		Short: "Create a .marc archive from a directory",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			keep := keepPlanLog || explain
			opts := runtime.ArchiveOpts{
				DictCompress: dictCompress,
				Workers:      workers,
			}
			if !noSolid {
				size, err := parseByteSize(solidBlockSize)
				if err != nil {
					return fmt.Errorf("invalid --solid-block-size %q: %w", solidBlockSize, err)
				}
				opts.SolidBlockSize = size
			}
			if err := runtime.ArchiveWithOpts(cmd.Context(), args[0], args[1], compressor, keep, opts); err != nil {
				return err
			}
			if explain {
				return printPlanSummary(cmd, args[0])
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
