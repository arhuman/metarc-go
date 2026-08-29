package colstream

import (
	"context"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

func TestColStream_ID(t *testing.T) {
	if got := New().ID(); got != "col-stream/v1" {
		t.Errorf("ID() = %q, want %q", got, "col-stream/v1")
	}
}

func TestColStream_CostEstimate(t *testing.T) {
	c := New()
	e := marc.Entry{RelPath: "data.csv"}
	gain, cpu := c.CostEstimate(e, marc.Facts{Size: 640000})
	if gain != 64000 {
		t.Errorf("gain = %d, want 64000", gain)
	}
	if cpu != 10000 {
		t.Errorf("cpu = %d, want 10000", cpu)
	}
	// The planner skips a transform whose gain does not exceed its cost, so a
	// net estimate that never clears its own cost would make the transform
	// unreachable.
	if gain <= cpu {
		t.Errorf("gain (%d) must exceed cpu (%d) for the planner to select it", gain, cpu)
	}
}

func TestColStream_Applicable(t *testing.T) {
	c := New()
	ctx := context.Background()
	const ok = 1 << 20

	cases := []struct {
		path string
		size int64
		want bool
	}{
		{"data.csv", ok, true},
		{"data.CSV", ok, true},
		{"data.tsv", ok, true},
		{"nested/dir/rows.csv", ok, true},
		{"data.json", ok, false},
		{"data.log", ok, false},
		{"data", ok, false},
		{"data.csv", minSize - 1, false},
		{"data.csv", maxSize + 1, false},
	}
	for _, tc := range cases {
		e := marc.Entry{RelPath: tc.path}
		if got := c.Applicable(ctx, e, marc.Facts{Size: tc.size}); got != tc.want {
			t.Errorf("Applicable(%q, size=%d) = %v, want %v", tc.path, tc.size, got, tc.want)
		}
	}
}

func TestColStream_delimiterFor(t *testing.T) {
	if got := delimiterFor("a.tsv"); got != '\t' {
		t.Errorf("delimiterFor(.tsv) = %q, want tab", got)
	}
	if got := delimiterFor("a.csv"); got != ',' {
		t.Errorf("delimiterFor(.csv) = %q, want comma", got)
	}
}
