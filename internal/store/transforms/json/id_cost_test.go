package json

import (
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

func TestCanonical_ID(t *testing.T) {
	c := NewCanonical()
	if c.ID() != "json-canonical/v1" {
		t.Errorf("ID() = %q, want %q", c.ID(), "json-canonical/v1")
	}
}

func TestCanonical_CostEstimate(t *testing.T) {
	c := NewCanonical()
	e := makeEntry("data.json", 1024)
	gain, cpu := c.CostEstimate(e, marc.Facts{Size: 1024})
	if gain != 256 { // 1024/4
		t.Errorf("gain = %d, want 256", gain)
	}
	if cpu != 2 { // 1024/512
		t.Errorf("cpu = %d, want 2", cpu)
	}
}
