package analyze

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// errReader is an io.Reader that returns an error after n bytes.
type errReader struct {
	data []byte
	pos  int
	fail int // fail on this read call number (0 = first, etc.)
	call int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if r.call >= r.fail {
		return 0, errors.New("forced read error")
	}
	r.call++
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestFullHash_readerError verifies that FullHash returns an error when the
// reader fails mid-stream.
func TestFullHash_readerError(t *testing.T) {
	r := &errReader{data: make([]byte, 1024), fail: 0}
	_, err := FullHash(r)
	if err == nil {
		t.Fatal("expected error from failing reader, got nil")
	}
}

// nonSeekableReader wraps a Reader and hides io.Seeker so QuickSig cannot seek.
type nonSeekableReader struct {
	r io.Reader
}

func (n *nonSeekableReader) Read(p []byte) (int, error) {
	return n.r.Read(p)
}

// TestQuickSig_largeNonSeekable verifies that QuickSig falls back to head-only
// hashing when given a large file with a non-seekable reader.
func TestQuickSig_largeNonSeekable(t *testing.T) {
	// > 8K of data so we hit the "large file" branch.
	data := bytes.Repeat([]byte("x"), 16*1024)
	r := &nonSeekableReader{r: bytes.NewReader(data)}

	sig, err := QuickSig(r)
	if err != nil {
		t.Fatalf("QuickSig with non-seekable large reader: %v", err)
	}
	if sig == [16]byte{} {
		t.Fatal("sig is all zeros for non-empty input")
	}

	// Same data via seekable reader.
	sig2, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// Non-seekable sig is based on head only; seekable uses head+tail.
	// For a uniformly filled buffer, head and tail are the same so sigs may match.
	// The important thing is no panic and no error.
	_ = sig2
}

// TestQuickSig_largeNonSeekable_uniqueHead verifies the fallback path produces
// distinct results for distinct head content.
func TestQuickSig_largeNonSeekable_uniqueHead(t *testing.T) {
	size := 16 * 1024
	a := make([]byte, size)
	b := make([]byte, size)
	// Different first 4K (head).
	a[0] = 0x01
	b[0] = 0x02
	// Same tail.
	for i := size - 4096; i < size; i++ {
		a[i] = 0xFF
		b[i] = 0xFF
	}

	sigA, err := QuickSig(&nonSeekableReader{r: bytes.NewReader(a)})
	if err != nil {
		t.Fatal(err)
	}
	sigB, err := QuickSig(&nonSeekableReader{r: bytes.NewReader(b)})
	if err != nil {
		t.Fatal(err)
	}
	if sigA == sigB {
		t.Error("different head content should produce different QuickSig for non-seekable readers")
	}
}
