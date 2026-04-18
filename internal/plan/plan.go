// Package plan decides which transform to apply per entry, using a cost/gain
// heuristic. The planner must be willing to say "do nothing".
package plan

import (
	"context"
	"fmt"

	"github.com/arhuman/metarc/pkg/marc"
)

// Registry is the ordered list of transforms. First applicable transform wins.
var Registry []marc.Transform

// Decision records the planner's reasoning for a single entry.
type Decision struct {
	TransformID   string // "" = raw (no transform applied)
	EstimatedGain int64
	EstimatedCPU  int64
	Applied       bool
	Reason        string // human-readable explanation
}

// Decide returns the chosen transform (or nil) and a Decision record for logging.
func Decide(ctx context.Context, e marc.Entry, facts marc.Facts) (marc.Transform, Decision) {
	for _, t := range Registry {
		if t.Applicable(ctx, e, facts) {
			gain, cpu := t.CostEstimate(e, facts)
			id := string(t.ID())
			if gain > cpu {
				return t, Decision{
					TransformID:   id,
					EstimatedGain: gain,
					EstimatedCPU:  cpu,
					Applied:       true,
					Reason:        fmt.Sprintf("%s selected", id),
				}
			}
			return nil, Decision{
				TransformID:   id,
				EstimatedGain: gain,
				EstimatedCPU:  cpu,
				Applied:       false,
				Reason:        fmt.Sprintf("gain (%d) <= cpu cost (%d), skipped", gain, cpu),
			}
		}
	}
	return nil, Decision{
		Reason: "no applicable transform",
	}
}
