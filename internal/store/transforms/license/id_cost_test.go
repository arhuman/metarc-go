package license

import (
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

func TestCanonical_ID(t *testing.T) {
	c := NewCanonical()
	if c.ID() != "license-canonical/v1" {
		t.Errorf("ID() = %q, want %q", c.ID(), "license-canonical/v1")
	}
}

func TestCanonical_CostEstimate(t *testing.T) {
	c := NewCanonical()
	e := marc.Entry{RelPath: "LICENSE"}
	gain, cpu := c.CostEstimate(e, marc.Facts{Size: 2048})
	if gain != 2048 {
		t.Errorf("gain = %d, want 2048", gain)
	}
	if cpu != 512 {
		t.Errorf("cpu = %d, want 512", cpu)
	}
}
