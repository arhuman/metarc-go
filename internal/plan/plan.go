// Package plan holds the transform registry, decision types, and disabled set.
// The actual chain logic (iterate, apply, fallback) lives in the store package.
package plan

import (
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// Registry is the ordered list of transforms. The store iterates this list
// and calls Apply on each applicable transform until one returns handled=true.
var Registry []marc.Transform

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

// RegistryIDs returns a comma-separated list of registered transform IDs.
func RegistryIDs() string {
	ids := make([]string, len(Registry))
	for i, t := range Registry {
		ids[i] = string(t.ID())
	}
	return strings.Join(ids, ",")
}
