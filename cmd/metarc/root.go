package main

import "github.com/spf13/cobra"

// newRootCmd builds the top-level `metarc` command with its subcommands wired.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "metarc",
		Short:   "Metarc — Metacompression-based archiver",
		Version: version + " (" + commit + ", " + date + ")",
	}
	cmd.AddCommand(
		newArchiveCmd(),
		newExtractCmd(),
		newInspectCmd(),
		newBenchCmd(),
	)
	return cmd
}
