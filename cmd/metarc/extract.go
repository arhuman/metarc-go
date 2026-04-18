package main

import (
	"github.com/arhuman/metarc/internal/runtime"
	"github.com/spf13/cobra"
)

// newExtractCmd returns the `metarc extract` subcommand.
// Usage: metarc extract <archive.marc> [-C <dest-dir>]
func newExtractCmd() *cobra.Command {
	var destDir string

	cmd := &cobra.Command{
		Use:   "extract <archive.marc>",
		Short: "Extract a .marc archive to a directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if destDir == "" {
				destDir = "."
			}
			return runtime.Extract(cmd.Context(), args[0], destDir)
		},
	}

	cmd.Flags().StringVarP(&destDir, "dest", "C", "", "destination directory (default: current directory)")

	return cmd
}
