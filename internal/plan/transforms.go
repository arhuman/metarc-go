package plan

import (
	"github.com/arhuman/metarc-go/internal/store/transforms"
	"github.com/arhuman/metarc-go/internal/store/transforms/goline"
	"github.com/arhuman/metarc-go/internal/store/transforms/license"
	"github.com/arhuman/metarc-go/pkg/marc"
)

func init() {
	// Only lossless transforms are enabled by default.
	// json-canonical/v1 and log-template/v1 are LOSSY: they discard original
	// formatting and restore a canonical form. They must remain opt-in until
	// they store the original content alongside the canonical form.
	Registry = []marc.Transform{
		transforms.NewDedup(),   // content-addressable dedup (lossless) -- must be first
		goline.NewGoLineSubst(), // line substitution for .go files (lossless)
		license.NewCanonical(),  // license template diff (lossless via Myers diff)
	}
}
