package starlark

import (
	"errors"
	"fmt"
	"testing"

	"go.starlark.net/syntax"
)

// sorted() charges a unit for each comparison and a unit for each two swaps
// that the stable sort makes, and nothing for a bound on them: the work of a
// sorted list is the least (a few comparisons), of a reversed one more, of a
// shuffled one the most. The numbers of calls are those of stableSort on the
// same data (the sequence of its calls is held by TestStableSort_...).
func TestSorted_ChargesTheComparisonsAndTheSwaps(t *testing.T) {
	const n = 3000
	for _, c := range []struct {
		name string
		gen  string // an expression of i
		val  func(i int) int
	}{
		{"shuffled", "(i * 7919) % 3001", func(i int) int { return (i * 7919) % 3001 }},
		{"ascending", "i", func(i int) int { return i }},
		{"descending", "%d - i", func(i int) int { return n - i }},
		{"equal", "7", func(i int) int { return 7 }},
		{"few distinct", "i % 5", func(i int) int { return i % 5 }},
	} {
		gen := c.gen
		if gen == "%d - i" {
			gen = fmt.Sprintf(gen, n)
		}
		a := make([]int, n)
		for i := range a {
			a[i] = c.val(i)
		}
		r := newRecorder(a)
		stableSort(r, n)

		setup := fmt.Sprintf("l = [%s for i in range(%d)]\nreset()\n", gen, n)
		sortedExtra, _, _ := extraWork(t, setup+"r = sorted(l)")
		listExtra, _, _ := extraWork(t, setup+"r = list(l)") // the same copy, and a unit for each element
		got := int64(sortedExtra) - int64(listExtra) + int64(n)
		want := int64(r.less) + int64(r.swaps/2)
		if d := got - want; d < -4 || d > 4 {
			t.Errorf("%s: sorted charged %d units for %d comparisons and %d swaps (%d expected)", c.name, got, r.less, r.swaps, want)
		}
		t.Logf("%s: %d comparisons, %d swaps, charged %d", c.name, r.less, r.swaps, got)
	}
}

// A sort that has run out of work stops: it does not go on comparing (the
// comparisons are the CPU that the limit is there to bound), though sort.Stable
// cannot be told to stop.
func TestSorted_StopsComparingAtTheLimit(t *testing.T) {
	resetCounts()
	th := &Thread{}
	th.SetMaxWork(20000)
	extra := countedBuiltins()
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star",
		"t = tuple(mk(200))\nl = [t] * 5000\nreset()\nr = sorted(l)\n", extra)
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("got %v, want a refusal of the work", err)
	}
	if ops := cmpCount.Load(); ops > 20000 {
		t.Errorf("%d comparisons were made after a limit of 20000 units of work", ops)
	}
}
