package plan

import (
	"context"
	"io"
	"testing"

	"github.com/arhuman/metarc/pkg/marc"
)

// fakeTransform is a controllable marc.Transform for testing.
type fakeTransform struct {
	id         marc.TransformID
	applicable bool
	gain       int64
	cpu        int64
}

func (f *fakeTransform) ID() marc.TransformID { return f.id }

func (f *fakeTransform) Applicable(_ context.Context, _ marc.Entry, _ marc.Facts) bool {
	return f.applicable
}

func (f *fakeTransform) CostEstimate(_ marc.Entry, _ marc.Facts) (int64, int64) {
	return f.gain, f.cpu
}

func (f *fakeTransform) Apply(_ context.Context, _ marc.Entry, _ io.Reader, _ marc.BlobSink) (marc.Result, error) {
	return marc.Result{}, nil
}

func (f *fakeTransform) Reverse(_ context.Context, _ marc.Result, _ marc.BlobReader, _ io.Writer) error {
	return nil
}

// withRegistry temporarily replaces Registry for the duration of a test.
func withRegistry(t *testing.T, transforms []marc.Transform) {
	t.Helper()
	orig := Registry
	Registry = transforms
	t.Cleanup(func() { Registry = orig })
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	entry := marc.Entry{}
	facts := marc.Facts{Size: 1024}

	tests := []struct {
		name           string
		registry       []marc.Transform
		wantTransform  bool   // true = non-nil transform returned
		wantApplied    bool
		wantTransformID string
		wantReason     string
		wantGain       int64
		wantCPU        int64
	}{
		{
			name: "applicable and gain > cpu returns transform applied=true",
			registry: []marc.Transform{
				&fakeTransform{id: "test/v1", applicable: true, gain: 100, cpu: 10},
			},
			wantTransform:   true,
			wantApplied:     true,
			wantTransformID: "test/v1",
			wantReason:      "test/v1 selected",
			wantGain:        100,
			wantCPU:         10,
		},
		{
			name: "applicable and gain < cpu returns nil applied=false with reason",
			registry: []marc.Transform{
				&fakeTransform{id: "test/v1", applicable: true, gain: 5, cpu: 50},
			},
			wantTransform:   false,
			wantApplied:     false,
			wantTransformID: "test/v1",
			wantReason:      "gain (5) <= cpu cost (50), skipped",
			wantGain:        5,
			wantCPU:         50,
		},
		{
			name: "gain equals cpu is skipped (gain > cpu is false)",
			registry: []marc.Transform{
				&fakeTransform{id: "test/v1", applicable: true, gain: 42, cpu: 42},
			},
			wantTransform:   false,
			wantApplied:     false,
			wantTransformID: "test/v1",
			wantReason:      "gain (42) <= cpu cost (42), skipped",
			wantGain:        42,
			wantCPU:         42,
		},
		{
			name: "no applicable transform returns nil with no-applicable reason",
			registry: []marc.Transform{
				&fakeTransform{id: "test/v1", applicable: false, gain: 100, cpu: 10},
			},
			wantTransform: false,
			wantApplied:   false,
			wantReason:    "no applicable transform",
		},
		{
			name:          "empty registry returns nil with no-applicable reason",
			registry:      []marc.Transform{},
			wantTransform: false,
			wantApplied:   false,
			wantReason:    "no applicable transform",
		},
		{
			name: "multiple transforms first applicable wins",
			registry: []marc.Transform{
				&fakeTransform{id: "first/v1", applicable: false, gain: 200, cpu: 10},
				&fakeTransform{id: "second/v1", applicable: true, gain: 150, cpu: 20},
				&fakeTransform{id: "third/v1", applicable: true, gain: 300, cpu: 5},
			},
			wantTransform:   true,
			wantApplied:     true,
			wantTransformID: "second/v1",
			wantReason:      "second/v1 selected",
			wantGain:        150,
			wantCPU:         20,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRegistry(t, tc.registry)

			got, decision := Decide(ctx, entry, facts)

			if tc.wantTransform {
				if got == nil {
					t.Fatal("expected a non-nil transform, got nil")
				}
			} else {
				if got != nil {
					t.Fatalf("expected nil transform, got %v", got)
				}
			}

			if decision.Applied != tc.wantApplied {
				t.Errorf("Applied = %v, want %v", decision.Applied, tc.wantApplied)
			}
			if decision.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", decision.Reason, tc.wantReason)
			}
			if tc.wantTransformID != "" && decision.TransformID != tc.wantTransformID {
				t.Errorf("TransformID = %q, want %q", decision.TransformID, tc.wantTransformID)
			}
			if tc.wantGain != 0 && decision.EstimatedGain != tc.wantGain {
				t.Errorf("EstimatedGain = %d, want %d", decision.EstimatedGain, tc.wantGain)
			}
			if tc.wantCPU != 0 && decision.EstimatedCPU != tc.wantCPU {
				t.Errorf("EstimatedCPU = %d, want %d", decision.EstimatedCPU, tc.wantCPU)
			}
		})
	}
}
