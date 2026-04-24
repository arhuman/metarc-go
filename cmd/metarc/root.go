package main

import (
	"github.com/arhuman/metarc-go/internal/plan"
	"github.com/spf13/cobra"
)

// newRootCmd builds the top-level `metarc` command with its subcommands wired.
func newRootCmd() *cobra.Command {
	var verbose bool
	var showVersion bool

	cmd := &cobra.Command{
		Use:   "metarc",
		Short: "Metarc — Metacompression-based archiver",
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				cmd.Printf("metarc version %s (%s, %s)\n", version, commit, date)
				if verbose {
					printTransforms(cmd)
				}
				return nil
			}
			return cmd.Help()
		},
	}

	cmd.Flags().BoolVar(&showVersion, "version", false, "print version information")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	cmd.AddCommand(
		newArchiveCmd(),
		newExtractCmd(),
		newInspectCmd(),
		newBenchCmd(),
	)
	return cmd
}

func printTransforms(cmd *cobra.Command) {
	cmd.Println("\nTransforms:")
	for _, t := range plan.Registry {
		id := string(t.ID())
		state := "enabled"
		if plan.Disabled[id] {
			state = "disabled"
		}
		cmd.Printf("  %-25s %s\n", id, state)
	}
	for _, id := range []string{"near-dup-delta/v1"} {
		cmd.Printf("  %-25s %s\n", id, "stub")
	}
}
