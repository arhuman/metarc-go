package plan

import (
	"context"
	"io"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
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

func (f *fakeTransform) Apply(_ context.Context, _ marc.Entry, _ marc.Facts, _ io.Reader, _ marc.BlobSink) (marc.Result, bool, error) {
	return marc.Result{}, false, nil
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

func TestRegistryIDs(t *testing.T) {
	tests := []struct {
		name     string
		registry []marc.Transform
		want     string
	}{
		{
			name:     "empty registry",
			registry: []marc.Transform{},
			want:     "",
		},
		{
			name: "single transform",
			registry: []marc.Transform{
				&fakeTransform{id: "test/v1"},
			},
			want: "test/v1",
		},
		{
			name: "multiple transforms",
			registry: []marc.Transform{
				&fakeTransform{id: "dedup/v1"},
				&fakeTransform{id: "goline/v1"},
				&fakeTransform{id: "license/v1"},
			},
			want: "dedup/v1,goline/v1,license/v1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRegistry(t, tc.registry)
			got := RegistryIDs()
			if got != tc.want {
				t.Errorf("RegistryIDs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisabled(t *testing.T) {
	// Verify Disabled map works as expected.
	orig := Disabled
	Disabled = map[string]bool{"test/v1": true}
	t.Cleanup(func() { Disabled = orig })

	if !Disabled["test/v1"] {
		t.Error("expected test/v1 to be disabled")
	}
	if Disabled["other/v1"] {
		t.Error("expected other/v1 to not be disabled")
	}
}
