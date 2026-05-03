package plan

import (
	"github.com/arhuman/metarc-go/internal/store/transforms"
	"github.com/arhuman/metarc-go/internal/store/transforms/goline"
	"github.com/arhuman/metarc-go/internal/store/transforms/license"
	"github.com/arhuman/metarc-go/internal/store/transforms/passthrough"
	"github.com/arhuman/metarc-go/pkg/marc"
)

func init() {
	// Only lossless transforms are enabled by default.
	// json-canonical/v1 and log-template/v1 are LOSSY: they discard original
	// formatting and restore a canonical form. They must remain opt-in until
	// they store the original content alongside the canonical form.
	//
	// jsline (js-line-subst/v1) and pyline (py-line-subst/v1) are
	// intentionally NOT registered: their hand-curated dictionaries showed
	// no measurable ratio gain on the bench corpora. Both packages remain
	// in the tree and can be re-registered once a frequency-counted
	// token list is built from a real corpus, or removed altogether in
	// favour of per-extension trained zstd dicts (Item 4 of the
	// compression roadmap).
	Registry = []marc.Transform{
		transforms.NewDedup(),   // content-addressable dedup (lossless) -- must be first
		passthrough.New(),       // skip zstd for already-compressed extensions (lossless)
		goline.NewGoLineSubst(), // line substitution for .go files (lossless)
		license.NewCanonical(),  // license template diff (lossless via Myers diff)
	}
}
