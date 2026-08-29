package plan

import (
	"github.com/arhuman/metarc-go/internal/store/transforms"
	"github.com/arhuman/metarc-go/internal/store/transforms/goline"
	"github.com/arhuman/metarc-go/internal/store/transforms/jsline"
	"github.com/arhuman/metarc-go/internal/store/transforms/license"
	"github.com/arhuman/metarc-go/internal/store/transforms/logdate"
	"github.com/arhuman/metarc-go/internal/store/transforms/passthrough"
	"github.com/arhuman/metarc-go/internal/store/transforms/pyline"
)

func init() {
	// Only lossless transforms are enabled by default.
	// json-canonical/v1 and log-template/v1 are LOSSY: they discard original
	// formatting and restore a canonical form. They must remain opt-in until
	// they store the original content alongside the canonical form, so they are
	// not registered at all.
	Registry = []Entry{
		{transforms.NewDedup(), true},   // content-addressable dedup (lossless) -- must be first
		{passthrough.New(), true},       // skip zstd for already-compressed extensions (lossless)
		{goline.NewGoLineSubst(), true}, // line substitution for .go files (lossless)
		{license.NewCanonical(), true},  // license template diff (lossless via Myers diff)

		// Decode-only. logdate is lossless but counterproductive: it trades
		// redundant timestamp text for fixed-width binary that zstd cannot
		// model, costing 755 bytes and half the wall time on loghub (ADR-020).
		{logdate.New(), false},
		// Decode-only. Both dictionaries are hand-curated rather than
		// frequency-counted, and measured ~no gain on their loss corpora.
		{jsline.NewJsLineSubst(), false},
		{pyline.NewPyLineSubst(), false},
	}
}
