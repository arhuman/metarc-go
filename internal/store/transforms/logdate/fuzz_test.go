package logdate

import (
	"bytes"
	"context"
	"testing"

	"github.com/arhuman/metarc-go/pkg/marc"
)

// FuzzLogDateReverse_corrupt feeds arbitrary bytes as both Result.Params and
// the backing blob content, simulating a corrupted or maliciously crafted
// archive. Reverse parses self-describing binary tokens straight from this
// data, so it must not panic; any returned error is acceptable.
//
// Note: round-trip coverage for Apply+Reverse already exists as FuzzRoundTrip
// in logdate_test.go; this target only covers the untrusted-input Reverse path.
func FuzzLogDateReverse_corrupt(f *testing.F) {
	ld := New()

	f.Add([]byte(`{"fmt":0}`), []byte(""))
	f.Add([]byte(`{"fmt":0}`), []byte("\x00"))
	f.Add([]byte(`{"fmt":0}`), []byte("\x00\x01"))
	f.Add([]byte(`{"fmt":0}`), []byte("start \x00\x02\x00\x00\x00\x00\x00\x00\x00\x00 end"))
	f.Add([]byte(`not json`), []byte("garbage"))
	f.Add([]byte(``), []byte(``))

	f.Fuzz(func(t *testing.T, paramsBytes, blobBytes []byte) {
		blobs := &fakeBlobs{blobs: map[marc.BlobID][]byte{1: blobBytes}}
		result := marc.Result{BlobIDs: []marc.BlobID{1}, Params: paramsBytes}

		var buf bytes.Buffer
		ctx := context.Background()
		_ = ld.Reverse(ctx, result, blobs, &buf) // error is fine, panic is not
	})
}
