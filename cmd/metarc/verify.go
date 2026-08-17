package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/arhuman/metarc-go/internal/runtime"
)

// newVerifyCmd returns the `metarc verify` subcommand.
// Usage: metarc verify <archive.marc>
func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <archive.marc>",
		Short: "Recompute the integrity values stored in a .marc archive",
		Long: "Verify rehashes the whole archive: the footer BLAKE3 prefix over every byte " +
			"before the footer, and the per-blob BLAKE3 prefix recorded in the catalog. " +
			"It detects corruption, not tampering, and exits non-zero on any mismatch.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := runtime.Verify(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch {
			case !rep.FooterChecked:
				cmd.Println("Footer checksum:      absent (legacy split-file archive)")
			case rep.FooterOK:
				cmd.Println("Footer checksum:      ok")
			default:
				cmd.Println("Footer checksum:      MISMATCH")
			}
			cmd.Printf("Blobs verified:       %d\n", rep.BlobsChecked)

			if rep.OK() {
				cmd.Println("\nok")
				return nil
			}

			cmd.Printf("\n%d check(s) failed:\n", len(rep.Mismatches))
			for _, m := range rep.Mismatches {
				cmd.Printf("  %s\n", m)
			}
			return fmt.Errorf("verify: %s: %d integrity check(s) failed", args[0], len(rep.Mismatches))
		},
	}
}
