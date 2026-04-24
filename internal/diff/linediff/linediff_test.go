package linediff

import (
	"strings"
	"testing"
)

func TestDiff_identical(t *testing.T) {
	a := []string{"hello", "world"}
	ops := Diff(a, a)
	for _, op := range ops {
		if op.Kind != Equal {
			t.Fatalf("expected all Equal ops, got %c", op.Kind)
		}
	}
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, a, result)
}

func TestDiff_empty(t *testing.T) {
	ops := Diff(nil, nil)
	if len(ops) != 0 {
		t.Fatalf("expected no ops for empty inputs, got %d", len(ops))
	}
}

func TestDiff_emptyToSomething(t *testing.T) {
	b := []string{"hello", "world"}
	ops := Diff(nil, b)
	result, err := Apply(nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestDiff_somethingToEmpty(t *testing.T) {
	a := []string{"hello", "world"}
	ops := Diff(a, nil)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %v", result)
	}
}

func TestDiff_singleInsert(t *testing.T) {
	a := []string{"a", "c"}
	b := []string{"a", "b", "c"}
	ops := Diff(a, b)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestDiff_singleDelete(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "c"}
	ops := Diff(a, b)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestDiff_singleReplace(t *testing.T) {
	a := []string{"a", "OLD", "c"}
	b := []string{"a", "NEW", "c"}
	ops := Diff(a, b)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestDiff_completelyDifferent(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"x", "y", "z"}
	ops := Diff(a, b)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestDiff_copyrightLineReplace(t *testing.T) {
	template := strings.Split("MIT License\n\nCopyright (c) [year] [fullname]\n\nPermission is hereby granted", "\n")
	actual := strings.Split("MIT License\n\nCopyright (c) 2024 Google LLC\n\nPermission is hereby granted", "\n")

	ops := Diff(template, actual)
	result, err := Apply(template, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, actual, result)

	// Verify the diff is minimal: should have exactly one delete+insert pair.
	var deletes, inserts int
	for _, op := range ops {
		switch op.Kind {
		case Delete:
			deletes += len(op.Lines)
		case Insert:
			inserts += len(op.Lines)
		}
	}
	if deletes != 1 || inserts != 1 {
		t.Errorf("expected 1 delete + 1 insert, got %d deletes + %d inserts", deletes, inserts)
	}
}

func TestDiff_multipleEdits(t *testing.T) {
	a := []string{"a", "b", "c", "d", "e"}
	b := []string{"a", "B", "c", "D", "e"}
	ops := Diff(a, b)
	result, err := Apply(a, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertSliceEqual(t, b, result)
}

func TestApply_errorOnOverflow(t *testing.T) {
	ops := []Op{{Kind: Equal, Lines: []string{"a", "b", "c"}}}
	_, err := Apply([]string{"a"}, ops)
	if err == nil {
		t.Fatal("expected error on overflow")
	}
}

func assertSliceEqual(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("length mismatch: want %d, got %d\nwant: %v\ngot:  %v", len(want), len(got), want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("mismatch at index %d: want %q, got %q", i, want[i], got[i])
		}
	}
}
