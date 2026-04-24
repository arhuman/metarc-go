// Package linediff implements a minimal line-level Myers diff algorithm.
// It computes the shortest edit script between two slices of strings and
// can apply that script to reconstruct the target from the base.
package linediff

import "fmt"

// OpKind identifies the type of edit operation.
type OpKind byte

const (
	Equal  OpKind = '='
	Insert OpKind = '+'
	Delete OpKind = '-'
)

// Op is a single edit operation in a diff script.
type Op struct {
	Kind  OpKind
	Lines []string
}

// Diff computes the minimal line-level edit script from a to b using the
// Myers O(ND) algorithm. The returned ops, when applied to a, produce b.
func Diff(a, b []string) []Op {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n == 0 {
		return []Op{{Kind: Insert, Lines: b}}
	}
	if m == 0 {
		return []Op{{Kind: Delete, Lines: a}}
	}

	// Myers shortest edit script.
	max := n + m
	// v[k+max] = furthest reaching x on diagonal k.
	v := make([]int, 2*max+1)
	// trace records a copy of v for each value of d, used for backtracking.
	trace := make([][]int, 0, max+1)

	for d := 0; d <= max; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				x = v[k+1+max] // move down (insert)
			} else {
				x = v[k-1+max] + 1 // move right (delete)
			}
			y := x - k

			// follow diagonal (equal lines)
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+max] = x

			if x >= n && y >= m {
				return backtrack(trace, max, a, b)
			}
		}
	}
	// Unreachable for valid inputs.
	return nil
}

// backtrack walks the trace in reverse to build the edit script.
func backtrack(trace [][]int, max int, a, b []string) []Op {
	x, y := len(a), len(b)

	type edit struct {
		kind OpKind
		line string
	}
	var edits []edit

	for d := len(trace) - 1; d > 0; d-- {
		// trace[d] is the v snapshot taken at the START of forward iteration d,
		// i.e. the v state produced by iteration d-1. This is the state the
		// forward pass used to decide whether to insert or delete at step d.
		prevV := trace[d]
		k := x - y

		var prevK int
		isInsert := k == -d || (k != d && prevV[k-1+max] < prevV[k+1+max])
		if isInsert {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prevV[prevK+max]
		prevY := prevX - prevK

		// Diagonal (equal) lines between the edit point and (x,y).
		for x > prevX && y > prevY {
			x--
			y--
			edits = append(edits, edit{kind: Equal, line: a[x]})
		}

		// The edit itself.
		if isInsert {
			y--
			edits = append(edits, edit{kind: Insert, line: b[y]})
		} else {
			x--
			edits = append(edits, edit{kind: Delete, line: a[x]})
		}
	}

	// Remaining diagonal at d=0 (only equals).
	for x > 0 && y > 0 {
		x--
		y--
		edits = append(edits, edit{kind: Equal, line: a[x]})
	}

	// Reverse to forward order.
	for i, j := 0, len(edits)-1; i < j; i, j = i+1, j-1 {
		edits[i], edits[j] = edits[j], edits[i]
	}

	// Group consecutive edits of the same kind.
	var ops []Op
	for _, e := range edits {
		if len(ops) > 0 && ops[len(ops)-1].Kind == e.kind {
			ops[len(ops)-1].Lines = append(ops[len(ops)-1].Lines, e.line)
		} else {
			ops = append(ops, Op{Kind: e.kind, Lines: []string{e.line}})
		}
	}
	return ops
}

// Apply applies the edit script ops to base and returns the reconstructed
// target. Returns an error if the script is inconsistent with base.
func Apply(base []string, ops []Op) ([]string, error) {
	var result []string
	idx := 0
	for _, op := range ops {
		switch op.Kind {
		case Equal:
			end := idx + len(op.Lines)
			if end > len(base) {
				return nil, fmt.Errorf("linediff.Apply: equal op overflows base at index %d (need %d, have %d)", idx, end, len(base))
			}
			result = append(result, base[idx:end]...)
			idx = end
		case Delete:
			end := idx + len(op.Lines)
			if end > len(base) {
				return nil, fmt.Errorf("linediff.Apply: delete op overflows base at index %d (need %d, have %d)", idx, end, len(base))
			}
			idx = end
		case Insert:
			result = append(result, op.Lines...)
		}
	}
	return result, nil
}
