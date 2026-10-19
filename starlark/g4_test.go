package starlark

import (
	"strings"
	"testing"
)

// g4Nested returns a tuple nested d levels deep (d tuples), made in Go.
func g4Nested(d int) Value {
	var x Value = Tuple{}
	for i := 1; i < d; i++ {
		x = Tuple{x}
	}
	return x
}

// N2 (round 2): the tuples are pushed in the form writer like every other
// container (the depth rule DeepFormWork and the cycle set see them): the work
// of str() of a tuple nested d levels is the list's formula
// (TestG1aDeepFormIsChargedPerLevelBeyond32: 80, 140, 946 + 67 * 32 for 20, 33
// and 100 levels), plus the unit for the bytes of the "," a level that a
// one-element tuple has more (the bytes are charged a unit for 64): 0, 1, 1.
func TestG4TupleDepthIsChargedLikeAList(t *testing.T) {
	for _, c := range []struct {
		d          int
		list, want uint64
	}{{20, 80, 80}, {33, 140, 141}, {100, 946 + 67*g1aDeepFormWork, 946 + 67*g1aDeepFormWork + 1}} {
		if got := g1aStrWork(t, g1aNested(c.d)); got != c.list {
			t.Errorf("str of a list of %d levels: work %d, want %d", c.d, got, c.list)
		}
		if got := g1aStrWork(t, g4Nested(c.d)); got != c.want {
			t.Errorf("str of a tuple of %d levels: work %d, want %d (the list's %d + the bytes)", c.d, got, c.want, c.list)
		}
	}
}

// A tuple on the path does not enter the set of the cycle check (it cannot be
// a key), and the lists around it are found: a list inside a tuple inside the
// list, 40 levels deep, closes the cycle.
func TestG4TupleOnThePathAndACycle(t *testing.T) {
	l := NewList(nil)
	var x Value = l
	for i := 0; i < 40; i++ {
		x = Tuple{x}
	}
	x = Tuple{x}
	_ = l.Append(x) // l contains a tuple that contains l, 41 tuples down
	th := &Thread{}
	v, err := Call(th, Universe["str"], Tuple{l}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(v.(String))
	if !strings.Contains(s, "[...]") || !strings.HasPrefix(s, "[((((") {
		t.Errorf("str of the cycle: %q", s)
	}
}

// g4Work runs the Starlark source and returns the work of the thread.
func g4Work(t *testing.T, src string) uint64 {
	t.Helper()
	th := &Thread{}
	if _, err := ExecFile(th, "g4.star", src, nil); err != nil {
		t.Fatal(err)
	}
	return th.Work()
}

// N3 (round 2): d.update(list of pairs) with the keys that are there already
// took ~110 ns a pair (the iterator of the pair, its length, two Next, the
// insert) and was charged 2 units (4.5-5x C at 12 ns a unit). The price of one
// pair of the pairs path is pairWork = 6 units; and the insert of a key that is
// there already (found in the chain, the value written) is charged
// overwriteWork = 2 units more (insertWork 1 + bucket 1 = 2 against ~42 ns
// measured for d.update(d2) of the same keys: 3.5 units; 4 charged now).
// (The same price moves the existing keys of d.update(d2) and set.update, and
// countWork the issubset: the rows of prices.golden.)
func TestG4DictUpdatePairsExisting(t *testing.T) {
	const setup = "d = {i: i for i in range(1000)}\ne = [(i, i) for i in range(1000)]\n"
	base := g4Work(t, setup)
	got := g4Work(t, setup+"d.update(e)") - base
	t.Logf("update(pairs) of 1000 existing keys: %d", got)
	// 1000 * (insertWork 1 + bucket 1 + overwriteWork 2 + pairWork 6) + the call
	if want := uint64(10000 + 9); got != want {
		t.Errorf("work %d, want %d", got, want)
	}
}
