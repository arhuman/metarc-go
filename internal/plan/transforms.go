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
	// jsline (js-line-subst/v1) is intentionally NOT registered: its static
	// hand-curated dictionary measured ~no gain on the JS/TS loss corpora
	// (zstd's window already exploits these short patterns inside a 16 MiB
	// solid block, and JS/TS source has high format/quote-style variance
	// that breaks line-level lookup). The package is kept in the tree so it
	// can be re-registered once a frequency-counted js_token.txt is built
	// from a real corpus, or removed altogether in favour of a per-extension
	// trained zstd dict (Item 4 of the compression roadmap).
	Registry = []marc.Transform{
		transforms.NewDedup(),   // content-addressable dedup (lossless) -- must be first
		passthrough.New(),       // skip zstd for already-compressed extensions (lossless)
		goline.NewGoLineSubst(), // line substitution for .go files (lossless)
		license.NewCanonical(),  // license template diff (lossless via Myers diff)
	}
}
