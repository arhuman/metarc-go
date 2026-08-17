package json

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzJSONCanonical checks that canonicalization is semantically lossy-free:
// when Apply accepts the input, Reverse must produce JSON that decodes to the
// same generic value as the original. Duplicate object keys are not a source
// of ambiguity here because Go's decoder already collapses them (last key
// wins) before the canonical form is ever marshaled, so both sides decode
// through the same collapsing rule.
func FuzzJSONCanonical(f *testing.F) {
	c := NewCanonical()

	f.Add([]byte(`{"a":1,"b":{"c":[1,2,3],"d":null}}`))
	f.Add([]byte(`[1,2,3,"x",true,false,null]`))
	f.Add([]byte(`"héllo wörld é"`))
	f.Add([]byte(`123`))
	f.Add([]byte(`1.5e10`))
	f.Add([]byte(`-0.001`))
	f.Add([]byte(`null`))
	f.Add([]byte(`true`))
	f.Add([]byte(`false`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		sink := newFakeSink()
		e := makeEntry("x.json", int64(len(data)))
		facts := marc.Facts{Size: int64(len(data))}
		result, handled, err := c.Apply(ctx, e, facts, bytes.NewReader(data), sink)
		if err != nil || !handled {
			return
		}

		var out bytes.Buffer
		if err := c.Reverse(ctx, result, &fakeBlobs{blobs: sink.blobs}, &out); err != nil {
			t.Fatalf("Reverse: %v", err)
		}

		var orig, restored any
		if err := json.Unmarshal(data, &orig); err != nil {
			t.Fatalf("original failed to unmarshal after handled=true: %v", err)
		}
		if err := json.Unmarshal(out.Bytes(), &restored); err != nil {
			t.Fatalf("restored failed to unmarshal: %v", err)
		}
		if !reflect.DeepEqual(orig, restored) {
			t.Errorf("semantic mismatch:\norig:     %#v\nrestored: %#v", orig, restored)
		}
	})
}
