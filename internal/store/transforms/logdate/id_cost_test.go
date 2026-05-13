package logdate

import (
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

func TestLogDateSubst_ID(t *testing.T) {
	ld := New()
	if ld.ID() != "log-date-subst/v2" {
		t.Errorf("ID() = %q, want %q", ld.ID(), "log-date-subst/v2")
	}
}

func TestLogDateSubst_CostEstimate(t *testing.T) {
	ld := New()
	e := marc.Entry{RelPath: "app.log"}
	gain, cpu := ld.CostEstimate(e, marc.Facts{Size: 10240})
	if gain != 10240/3 {
		t.Errorf("gain = %d, want %d", gain, 10240/3)
	}
	if cpu != 10240/512 {
		t.Errorf("cpu = %d, want %d", cpu, 10240/512)
	}
}
