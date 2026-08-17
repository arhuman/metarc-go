package runtime

import (
	"context"
	"fmt"

	"github.com/arhuman/metarc-go/internal/store"
)

// VerifyReport summarizes the integrity checks run over one archive.
type VerifyReport struct {
	FooterChecked bool     // false for legacy split-file archives, which carry no footer
	FooterOK      bool     // meaningful only when FooterChecked is true
	BlobsChecked  int      // blobs whose stored hash matched
	Mismatches    []string // one line per failed check, footer first
}

// OK reports whether every check that ran passed.
func (rep VerifyReport) OK() bool {
	return len(rep.Mismatches) == 0
}

// Verify recomputes the integrity values a .marc archive stores: the footer
// BLAKE3 prefix covering every byte before the footer, and the per-blob BLAKE3
// prefix in blobs.sha. It reads the whole archive, unlike inspect and unlike
// store.OpenReader, both of which stay proportional to the catalog.
//
// A returned error means the archive could not be checked at all. A checkable
// archive that fails its own hashes returns a report with Mismatches populated
// and a nil error; callers decide the exit status from VerifyReport.OK.
func Verify(ctx context.Context, marcPath string) (VerifyReport, error) {
	var rep VerifyReport

	r, err := store.OpenReader(marcPath)
	if err != nil {
		return rep, fmt.Errorf("runtime.Verify: %w", err)
	}
	defer func() { _ = r.Close() }()

	checked, footerOK, err := r.VerifyFooter()
	if err != nil {
		return rep, fmt.Errorf("runtime.Verify: %w", err)
	}
	rep.FooterChecked = checked
	rep.FooterOK = footerOK
	if checked && !footerOK {
		rep.Mismatches = append(rep.Mismatches, "footer checksum does not match the archive content")
	}

	ids, err := r.QueryBlobIDs()
	if err != nil {
		return rep, fmt.Errorf("runtime.Verify: %w", err)
	}

	for _, id := range ids {
		select {
		case <-ctx.Done():
			return rep, fmt.Errorf("runtime.Verify: %w", ctx.Err())
		default:
		}
		if err := r.VerifyBlob(id); err != nil {
			rep.Mismatches = append(rep.Mismatches, err.Error())
			continue
		}
		rep.BlobsChecked++
	}

	return rep, nil
}
