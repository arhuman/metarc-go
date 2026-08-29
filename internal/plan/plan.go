// Package plan holds the transform registry, decision types, and disabled set.
// The actual chain logic (iterate, apply, fallback) lives in the store package.
package plan

import (
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// Entry pairs a transform with whether the planner may select it when writing.
// Read capability is implicit: presence in Registry means the transform is
// decodable, since removing it would break every archive that already uses it.
type Entry struct {
	Transform marc.Transform
	Write     bool // the planner may select this transform when archiving
}

// Registry is the ordered list of transforms. The store iterates the entries
// whose Write is true and calls Apply on each applicable transform until one
// returns handled=true. Extraction iterates the whole list, so every entry is
// decodable whatever its Write value.
var Registry []Entry

// Disabled is the set of transform IDs to skip during planning.
// Set before archiving; keyed by TransformID string.
var Disabled = map[string]bool{}

// Decision records the planner's reasoning for a single entry.
type Decision struct {
	TransformID   string // "" = raw (no transform applied)
	EstimatedGain int64
	EstimatedCPU  int64
	Applied       bool
	Reason        string // human-readable explanation
}

// RegistryIDs returns a comma-separated list of the write-selectable transform
// IDs, in registry order. Entries that are decode-only are omitted: they name
// no choice a caller can make, so offering them through --disable-transform or
// recording them as the archive's active set would be misleading.
func RegistryIDs() string {
	ids := make([]string, 0, len(Registry))
	for _, e := range Registry {
		if e.Write {
			ids = append(ids, string(e.Transform.ID()))
		}
	}
	return strings.Join(ids, ",")
}
