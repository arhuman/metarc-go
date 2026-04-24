package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"
)

// newInspectCmd returns the `metarc inspect` subcommand.
func newInspectCmd() *cobra.Command {
	var planLog bool
	var raw bool

	cmd := &cobra.Command{
		Use:   "inspect <archive.marc>",
		Short: "Inspect the contents and metadata of a .marc archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			marcPath := args[0]

			if raw {
				cmd.Printf("use: sqlite3 %s\n", marcPath)
				return nil
			}

			if planLog {
				return inspectPlanLog(cmd, marcPath)
			}

			return inspectOverview(cmd, marcPath)
		},
	}

	cmd.Flags().BoolVar(&planLog, "plan-log", false, "pretty-print plan decisions")
	cmd.Flags().BoolVar(&raw, "raw", false, "print sqlite3 command hint")

	return cmd
}

// inspectOverview prints entry count, blob count, total sizes, and compression ratio.
func inspectOverview(cmd *cobra.Command, marcPath string) error {
	r, err := store.OpenReader(marcPath)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	defer func() { _ = r.Close() }()

	ov, err := r.QueryOverview()
	if err != nil {
		return err
	}

	dedupSavingPct := 0.0
	if ov.OriginalSize > 0 {
		dedupSavingPct = (1 - float64(ov.TotalUlen)/float64(ov.OriginalSize)) * 100
	}
	blobRatioPct := 0.0
	if ov.TotalUlen > 0 {
		blobRatioPct = float64(ov.TotalClen) / float64(ov.TotalUlen) * 100
	}

	cmd.Printf("Entries:              %d\n", ov.EntryCount)
	cmd.Printf("Blobs:                %d\n", ov.BlobCount)
	cmd.Printf("Original size:        %s\n", humanBytes(ov.OriginalSize))
	cmd.Printf("After dedup:          %s (%.1f%% dedup saving)\n", humanBytes(ov.TotalUlen), dedupSavingPct)
	cmd.Printf("After compression:    %s (%.1f%% of deduped)\n", humanBytes(ov.TotalClen), blobRatioPct)

	if solidCount := r.QuerySolidBlockCount(); solidCount > 0 {
		cmd.Printf("Solid blocks:         %d\n", solidCount)
	}

	if err := inspectArchiveSizes(cmd, marcPath, ov.OriginalSize); err != nil {
		slog.Debug("skipping archive layout (footer not readable)", "err", err)
	}

	return nil
}

// inspectArchiveSizes reads the footer and prints blob region / catalog / overhead sizes.
func inspectArchiveSizes(cmd *cobra.Command, marcPath string, totalUlen int64) error {
	f, err := os.Open(marcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := info.Size()

	footer, err := marc.ReadFooter(f, fileSize)
	if err != nil {
		return err
	}

	const overhead = int64(len(marc.Magic)) + marc.FooterSize // 8 + 24 = 32
	blobRegion := int64(footer.CatalogOffset) - int64(footer.BlobRegionOffset)
	catalogChunk := fileSize - int64(footer.CatalogOffset) - marc.FooterSize

	archivePct := pct(fileSize, totalUlen)
	compressionPct := 0.0
	if totalUlen > 0 {
		compressionPct = (1 - float64(fileSize)/float64(totalUlen)) * 100
	}

	cmd.Printf("\nArchive layout (%s total, %.1f%% of original size, %.1f%% compression):\n",
		humanBytes(fileSize), archivePct, compressionPct)
	cmd.Printf("  Blob region:        %s (%.1f%% of archive)\n", humanBytes(blobRegion), pct(blobRegion, fileSize))
	cmd.Printf("  Catalog:            %s (%.1f%% of archive)\n", humanBytes(catalogChunk), pct(catalogChunk, fileSize))
	cmd.Printf("  Overhead:           %s (magic + footer)\n", humanBytes(overhead))
	return nil
}

func pct(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// inspectPlanLog pretty-prints plan_log rows grouped by transform_id.
func inspectPlanLog(cmd *cobra.Command, marcPath string) error {
	r, err := store.OpenReader(marcPath)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	defer func() { _ = r.Close() }()

	stats, err := r.QueryPlanLog()
	if err != nil {
		return err
	}
	if len(stats) == 0 {
		cmd.Println("plan_log is empty (archive created without --keep-plan-log)")
		return nil
	}

	var total int64
	for _, s := range stats {
		total += s.Applied + s.Skipped
	}

	cmd.Printf("Plan log (%d entries):\n\n", total)
	cmd.Printf("  %-20s %8s %8s %14s\n", "Transform", "Applied", "Skipped", "Est. gain")
	cmd.Printf("  %-20s %8s %8s %14s\n", "---------", "-------", "-------", "---------")
	for _, s := range stats {
		gain := ""
		if s.EstimatedGain > 0 {
			gain = humanBytes(s.EstimatedGain)
		}
		cmd.Printf("  %-20s %8d %8d %14s\n", s.TransformID, s.Applied, s.Skipped, gain)
	}
	return nil
}
