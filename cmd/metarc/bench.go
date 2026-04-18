package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/spf13/cobra"
)

// benchReport holds the benchmark results.
type benchReport struct {
	Source       string          `json:"source"`
	SourceBytes  int64           `json:"source_bytes"`
	SourceFiles  int64           `json:"source_files"`
	ArchivePath  string          `json:"archive_path"`
	ArchiveSize  int64           `json:"archive_size"`
	DurationMs   int64           `json:"duration_ms"`
	ThroughputMB float64         `json:"throughput_mb_s"`
	RatioPct     float64         `json:"ratio_pct"`
	RSSPeakKB    int64           `json:"rss_peak_kb"`
	Transforms   []transformStat `json:"transforms,omitempty"`
}

// transformStat holds per-transform breakdown.
type transformStat struct {
	ID      string `json:"id"`
	Applied int64  `json:"applied"`
	Skipped int64  `json:"skipped"`
}

// newBenchCmd returns the `metarc bench` subcommand.
func newBenchCmd() *cobra.Command {
	var workers int
	var compressor string
	var output string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "bench <source-dir>",
		Short: "Benchmark archive performance and ratios",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBench(cmd, args[0], workers, compressor, output, jsonOutput)
		},
	}

	cmd.Flags().IntVar(&workers, "workers", 0, "number of analysis workers (default: runtime.NumCPU())")
	cmd.Flags().StringVar(&compressor, "compressor", "zstd", "blob compressor: zstd or none")
	cmd.Flags().StringVar(&output, "output", "", "output path for .marc (default: temp file, deleted after)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON instead of human-readable")

	return cmd
}

func runBench(cmd *cobra.Command, sourceDir string, workers int, compressor, output string, jsonOutput bool) error {
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("bench: resolve source: %w", err)
	}

	// Count source files and total bytes.
	var sourceBytes, sourceFiles int64
	err = filepath.WalkDir(absSource, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sourceBytes += info.Size()
		sourceFiles++
		return nil
	})
	if err != nil {
		return fmt.Errorf("bench: walk source: %w", err)
	}

	// Determine archive output path.
	marcPath := output
	deleteMarcAfter := false
	if marcPath == "" {
		tmpF, err := os.CreateTemp("", "metarc-bench-*.marc")
		if err != nil {
			return fmt.Errorf("bench: create temp: %w", err)
		}
		marcPath = tmpF.Name()
		_ = tmpF.Close()
		_ = os.Remove(marcPath) // Archive will create it.
		deleteMarcAfter = true
	}
	if deleteMarcAfter {
		defer func() { _ = os.Remove(marcPath) }()
	}

	// Run archive with plan log kept for transform breakdown.
	start := time.Now()
	if err := runtime.Archive(cmd.Context(), marcPath, absSource, compressor, true, workers); err != nil {
		return fmt.Errorf("bench: archive: %w", err)
	}
	duration := time.Since(start)

	// Measure archive size.
	arcInfo, err := os.Stat(marcPath)
	if err != nil {
		return fmt.Errorf("bench: stat archive: %w", err)
	}

	// Get RSS peak.
	rssPeakKB := getRSSPeakKB()

	// Read transform breakdown from plan_log.
	var transforms []transformStat
	r, err := store.OpenReader(marcPath)
	if err == nil {
		stats, queryErr := r.QueryPlanLog()
		_ = r.Close()
		if queryErr == nil {
			for _, s := range stats {
				transforms = append(transforms, transformStat{
					ID:      s.TransformID,
					Applied: s.Applied,
					Skipped: s.Skipped,
				})
			}
		}
	}

	throughput := float64(sourceBytes) / (1024 * 1024) / duration.Seconds()
	ratio := float64(arcInfo.Size()) / float64(sourceBytes) * 100
	if sourceBytes == 0 {
		throughput = 0
		ratio = 0
	}

	report := benchReport{
		Source:       filepath.Base(absSource),
		SourceBytes:  sourceBytes,
		SourceFiles:  sourceFiles,
		ArchivePath:  marcPath,
		ArchiveSize:  arcInfo.Size(),
		DurationMs:   duration.Milliseconds(),
		ThroughputMB: throughput,
		RatioPct:     ratio,
		RSSPeakKB:    rssPeakKB,
		Transforms:   transforms,
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	printHumanReport(cmd, report, duration)
	return nil
}

func printHumanReport(cmd *cobra.Command, r benchReport, d time.Duration) {
	cmd.Printf("Source:      %s/ (%s, %d files)\n", r.Source, humanBytes(r.SourceBytes), r.SourceFiles)
	cmd.Printf("Archive:     %s (%s)\n", r.ArchivePath, humanBytes(r.ArchiveSize))
	cmd.Printf("Duration:    %s\n", d.Truncate(time.Millisecond))
	cmd.Printf("Throughput:  %.1f MB/s\n", r.ThroughputMB)
	cmd.Printf("Ratio:       %.1f%% of original\n", r.RatioPct)
	if r.RSSPeakKB > 0 {
		cmd.Printf("RSS peak:    %s\n", humanBytes(r.RSSPeakKB*1024))
	}

	if len(r.Transforms) > 0 {
		cmd.Printf("\nTransform breakdown:\n")
		for _, t := range r.Transforms {
			cmd.Printf("  %-25s %5d applied  %5d skipped\n", t.ID, t.Applied, t.Skipped)
		}
	}
}

// humanBytes formats bytes as a human-readable string.
func humanBytes(b int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

