package analyze

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestFullHash_consistency(t *testing.T) {
	data := []byte("hello metarc")
	h1, err := FullHash(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := FullHash(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("same content produced different hashes: %x vs %x", h1, h2)
	}
}

func TestFullHash_different(t *testing.T) {
	h1, err := FullHash(strings.NewReader("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := FullHash(strings.NewReader("bbb"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("different content produced same hash")
	}
}

func TestQuickSig_consistency(t *testing.T) {
	data := make([]byte, 1024)
	rand.Read(data)
	s1, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if s1 != s2 {
		t.Fatalf("same content produced different sigs: %x vs %x", s1, s2)
	}
}

func TestQuickSig_small(t *testing.T) {
	// File smaller than 8K should hash fully without panic.
	data := []byte("short")
	sig, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if sig == [16]byte{} {
		t.Fatal("sig is all zeros for non-empty input")
	}
}

func TestQuickSig_large(t *testing.T) {
	// Two files > 8K that differ only in the middle should produce the same QuickSig.
	size := 32 * 1024
	a := make([]byte, size)
	b := make([]byte, size)
	// Same head and tail.
	rand.Read(a[:quickSigChunk])
	copy(b[:quickSigChunk], a[:quickSigChunk])
	rand.Read(a[size-quickSigChunk:])
	copy(b[size-quickSigChunk:], a[size-quickSigChunk:])
	// Different middle.
	rand.Read(a[quickSigChunk : size-quickSigChunk])
	rand.Read(b[quickSigChunk : size-quickSigChunk])

	sigA, err := QuickSig(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	sigB, err := QuickSig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if sigA != sigB {
		t.Fatalf("files differing only in middle should have same QuickSig: %x vs %x", sigA, sigB)
	}
}

func TestQuickSig_exactBoundary(t *testing.T) {
	// File exactly 8K (the boundary).
	data := make([]byte, quickSigChunk*2)
	rand.Read(data)
	sig, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if sig == [16]byte{} {
		t.Fatal("sig is all zeros for 8K input")
	}
	// Should be consistent.
	sig2, err := QuickSig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if sig != sig2 {
		t.Fatalf("inconsistent sig for 8K boundary: %x vs %x", sig, sig2)
	}
}

func FuzzQuickSig(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("hello"))
	f.Add(make([]byte, 4096))
	f.Add(make([]byte, 8192))
	f.Add(make([]byte, 16384))

	f.Fuzz(func(t *testing.T, data []byte) {
		// QuickSig must never panic on arbitrary input.
		_, _ = QuickSig(bytes.NewReader(data))
	})
}
