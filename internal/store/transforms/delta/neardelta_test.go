package delta

import (
	"bytes"
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/pkg/marc"
)

type fakeFileInfo struct{ size int64 }

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestNearDupDelta_gainGate(t *testing.T) {
	// The near-dup-delta transform requires a benchmark showing >10% additional
	// gain over dedup alone on a real corpus (react or numpy).
	// Since we don't have such corpora in the test environment, and the stub
	// always returns handled=false, we skip.
	t.Skip("near-dup-delta gain < 10% (stub implementation), deferring full implementation")
}

func TestNearDup_ID(t *testing.T) {
	n := NewNearDup()
	if n.ID() != "near-dup-delta/v1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "near-dup-delta/v1")
	}
}

func TestNearDup_Applicable_alwaysFalse(t *testing.T) {
	n := NewNearDup()
	ctx := context.Background()
	e := marc.Entry{RelPath: "file.bin", Info: fakeFileInfo{size: 1024}}
	if n.Applicable(ctx, e, marc.Facts{Size: 1024}) {
		t.Error("Applicable should always return false (stub)")
	}
}

func TestNearDup_CostEstimate_zero(t *testing.T) {
	n := NewNearDup()
	e := marc.Entry{RelPath: "file.bin", Info: fakeFileInfo{size: 4096}}
	gain, cpu := n.CostEstimate(e, marc.Facts{Size: 4096})
	if gain != 0 || cpu != 0 {
		t.Errorf("CostEstimate: got (%d, %d), want (0, 0)", gain, cpu)
	}
}

func TestNearDup_Apply_returnsNotHandled(t *testing.T) {
	n := NewNearDup()
	ctx := context.Background()
	e := marc.Entry{RelPath: "file.bin", Info: fakeFileInfo{size: 10}}
	facts := marc.Facts{Size: 10}
	_, handled, err := n.Apply(ctx, e, facts, bytes.NewReader([]byte("data")), nil)
	if err != nil {
		t.Errorf("Apply: unexpected error %v", err)
	}
	if handled {
		t.Error("Apply: expected handled=false (stub)")
	}
}

func TestNearDup_Reverse_noOp(t *testing.T) {
	n := NewNearDup()
	ctx := context.Background()
	var out bytes.Buffer
	if err := n.Reverse(ctx, marc.Result{}, nil, &out); err != nil {
		t.Errorf("Reverse: got %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Errorf("Reverse wrote %d bytes, want 0", out.Len())
	}
}
