package plan

import (
	"github.com/arhuman/metarc-go/internal/store/transforms"
	"github.com/arhuman/metarc-go/internal/store/transforms/colstream"
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
		// Decode-only: missed its own kill criterion. col-stream is lossless
		// and its axis is real (LZ77 cannot subtract), but it took the loghub
		// archive only from 2,047,883 to 2,003,983 bytes, -2.14%, against the
		// >= 5% bar set before implementation. Forcing it onto every CSV in
		// the corpus, predicate disabled, reaches just -2.64%, so the shortfall
		// is the axis on this corpus and not the selection rule. The ceiling is
		// capped by the corpus: loghub holds 6.5 MB of CSV in 12.2 MB of input,
		// and the byte-exactness machinery the contract requires (a quoting bit
		// per field, a line-ending bit per row) spends part of what
		// transposition wins. Re-enable for writing only against a corpus whose
		// CSV share is dominant, and re-measure rather than assume.
		{colstream.New(), false},
	}
}
