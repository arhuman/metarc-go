package logtempl

import (
	"testing"

	"github.com/arhuman/metarc/pkg/marc"
)

func TestTemplate_ID(t *testing.T) {
	tmpl := NewTemplate()
	if tmpl.ID() != "log-template/v1" {
		t.Errorf("ID() = %q, want %q", tmpl.ID(), "log-template/v1")
	}
}

func TestTemplate_CostEstimate(t *testing.T) {
	tmpl := NewTemplate()
	e := marc.Entry{RelPath: "app.log"}
	gain, cpu := tmpl.CostEstimate(e, marc.Facts{Size: 3072})
	if gain != 1024 { // 3072/3
		t.Errorf("gain = %d, want 1024", gain)
	}
	if cpu != 12 { // 3072/256
		t.Errorf("cpu = %d, want 12", cpu)
	}
}
