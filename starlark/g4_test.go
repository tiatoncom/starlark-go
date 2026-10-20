package starlark

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"go.starlark.net/syntax"
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

// ord of a string of more than one rune counts its runes for the error text
// (v0.2.0's, unchanged): the count is a rune pass and is charged workRunes of
// the bytes, before it is made, like the other rune passes (it was n/96:
// 2.45x C for a string of 50 MB).
func TestG4OrdOfALongStringChargesTheRunePass(t *testing.T) {
	const n = 1 << 20
	s := String(strings.Repeat("x", n))
	th := &Thread{}
	_, err := ExecFile(th, "ord.star", "ord(L)", StringDict{"L": s})
	const want = "ord: string encodes 1048576 Unicode code points, want 1"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err %v, want it to contain %q", err, want)
	}
	if got := th.Work(); got < workRunes(n) || got > workRunes(n)+100 {
		t.Errorf("work %d, want workRunes(%d) = %d (and a call)", got, n, workRunes(n))
	}
	// the refusal comes before the count: at a limit below the pass, the work is the limit
	th = &Thread{}
	th.SetMaxWork(1000)
	_, err = ExecFile(th, "ord.star", "ord(L)", StringDict{"L": s})
	var we *WorkBudgetError
	if !errors.As(err, &we) || th.Work() != 1000 {
		t.Errorf("work limit 1000: err %v, work %d, want a work refusal at 1000", err, th.Work())
	}
}

// G5-1: the comparison of a small int with a big one is charged whichever side
// the big one is on (the word of the big int, and for the equality of a table
// bigEqualWork), as two big ints are; and it makes no big.Int of the small one.
func TestG4SmallVersusBigComparisonIsChargedBothOrders(t *testing.T) {
	big, small := MakeInt64(1<<40), MakeInt(5)
	units := func(f func(m *meter) error) uint64 {
		m := (&Thread{}).meter()
		if err := f(&m); err != nil {
			t.Fatal(err)
		}
		return m.units
	}
	for _, c := range []struct {
		name string
		x, y Int
		eq   uint64 // equalFast
		cmp  uint64 // compareM
	}{
		{"big, small", big, small, 1 + bigEqualWork, 1},
		{"small, big", small, big, 1 + bigEqualWork, 1},
		{"big, big", big, big, 1 + bigEqualWork, 1},
		{"small, small", small, small, 0, 0},
	} {
		if got := units(func(m *meter) error { _, err := equalFast(m, c.x, c.y, 10); return err }); got != c.eq {
			t.Errorf("equalFast(%s): %d units, want %d", c.name, got, c.eq)
		}
		if got := units(func(m *meter) error { _, err := compareM(m, syntax.LT, c.x, c.y, 10); return err }); got != c.cmp {
			t.Errorf("compareM(%s): %d units, want %d", c.name, got, c.cmp)
		}
	}
}

// The order of a small int and a big one is decided by the sign of the big
// one (a big int is never in the range of the small ones), and Cmp makes no
// big.Int for it.
func TestG4SmallVersusBigCmpDoesNotAllocate(t *testing.T) {
	smalls := []Int{MakeInt(0), MakeInt(5), MakeInt(-5), MakeInt(math.MaxInt32), MakeInt(math.MinInt32)}
	bigs := []Int{MakeInt64(1 << 40), MakeInt64(-(1 << 40)), MakeInt64(math.MaxInt32 + 1), MakeInt64(math.MinInt32 - 1)}
	for _, s := range smalls {
		for _, b := range bigs {
			want := s.bigInt().Cmp(b.bigInt())
			if got, _ := s.Cmp(b, 10); got != want {
				t.Errorf("%v.Cmp(%v) = %d, want %d", s, b, got, want)
			}
			if got, _ := b.Cmp(s, 10); got != -want {
				t.Errorf("%v.Cmp(%v) = %d, want %d", b, s, got, -want)
			}
		}
	}
	a, b := smalls[1], bigs[1]
	if n := testing.AllocsPerRun(100, func() { a.Cmp(b, 10); b.Cmp(a, 10) }); n != 0 {
		t.Errorf("Cmp of a small and a big int allocates %v times, want 0", n)
	}
}

// G6 (calls-0152): tuple() of an empty iterable charges the empty tuple
// (chargeTupleOf with n <= 0 is chargeTuple(n)), which is a charge of nothing
// that still asks the thread whether it is cancelled: on a thread that is
// cancelled the call fails, as the tuple of any other size does.
func TestG6TupleOfNothingOnACancelledThreadFails(t *testing.T) {
	for _, arg := range []Value{NewList(nil), Tuple{}, NewDict(0), String("")} {
		th := &Thread{}
		th.Cancel("stop")
		_, err := Call(th, Universe["tuple"], Tuple{arg}, nil)
		if err == nil || !strings.Contains(err.Error(), "stop") {
			t.Errorf("tuple(%v) on a cancelled thread: err %v, want the cancellation", arg, err)
		}
	}
	// the chargeTupleOf itself, for n = 0 and n < 0, and the same for n > 0
	for _, n := range []int{-1, 0, 1} {
		th := &Thread{}
		th.Cancel("stop")
		if err := th.chargeTupleOf(n, NewList(nil)); err == nil {
			t.Errorf("chargeTupleOf(%d) on a cancelled thread: nil, want the cancellation", n)
		}
	}
}

// G6 (struct-0229): the sort is LEFT at the first error of a comparison (the
// panic of sortStop); the guard "if s.stopped { return false }" of Less hides
// it from a test that counts the comparisons of the values (none is made after
// the error whether the sort is left or goes on), but a sort that goes on
// calls Less and Swap n log n more times, in time that nothing charges (the
// B6). So the calls to the sort interface are counted: after the error there
// is none.
type g6Counting struct {
	*sortSlice
	less, swap int
}

func (c *g6Counting) Less(i, j int) bool { c.less++; return c.sortSlice.Less(i, j) }
func (c *g6Counting) Swap(i, j int)      { c.swap++; c.sortSlice.Swap(i, j) }

func TestG6SortIsLeftAtTheFirstComparisonError(t *testing.T) {
	for _, tc := range []struct {
		name string
		mk   func(i int) Value // the values: all of them fail to compare
	}{
		{"a value that fails to compare", func(int) Value { return g1bBad{new(int)} }},
		{"types that do not compare", func(i int) Value {
			if i%2 == 0 {
				return MakeInt(i)
			}
			return String("s")
		}},
	} {
		for _, n := range []int{2, 25, 1000} {
			vals := make([]Value, n)
			for i := range vals {
				vals[i] = tc.mk(i)
			}
			for _, withKeys := range []bool{false, true} {
				s := &sortSlice{values: vals, m: (&Thread{}).meter()}
				if withKeys {
					s.keys = append([]Value(nil), vals...)
				}
				c := &g6Counting{sortSlice: s}
				func() {
					defer func() {
						if r := recover(); r != nil {
							if _, ok := r.(sortStop); !ok {
								panic(r)
							}
						}
					}()
					stableSort(c, n) // (what run does, with the calls counted)
				}()
				if s.err == nil {
					t.Fatalf("%s n=%d: no error", tc.name, n)
				}
				if c.less != 1 || c.swap != 0 {
					t.Errorf("%s n=%d keys=%v: %d calls of Less and %d of Swap, want 1 and 0: the sort goes on after the error", tc.name, n, withKeys, c.less, c.swap)
				}
			}
		}
	}
}

// G6 (struct-0222): a match is charged to its far end, not to the end of the
// window that finds it: the window of a needle of 100 bytes is 200 bytes, a
// match that begins at the far end of the haystack (n-i = 299, from the end;
// i+m = 299, from the front) is charged 299 bytes' worth, the same as a
// haystack of 299 bytes without the needle (all of it). The sliver of m-1
// bytes is what the final charge adds to the window; without it
// (`return i, nil`) the match is charged 200's worth, 24 units less.
func TestG6AMatchAtTheFarEndOfItsWindowIsChargedToTheEnd(t *testing.T) {
	needle := strings.Repeat("y", 100)
	absent := strings.Repeat("x", 299)
	work := func(src, s string) uint64 {
		th := &Thread{}
		if _, err := ExecFile(th, "s.star", src, StringDict{"S": String(s), "N": String(needle)}); err != nil {
			t.Fatal(err)
		}
		return th.Work()
	}
	for _, c := range []struct{ name, src, found string }{
		{"rfind", "r = S.rfind(N)", needle + strings.Repeat("x", 199)},
		{"find", "r = S.find(N)", strings.Repeat("x", 199) + needle},
	} {
		got, all := work(c.src, c.found), work(c.src, absent)
		if got != all {
			t.Errorf("%s of a match at the far end of its window: work %d, want %d (the same as the haystack without it)", c.name, got, all)
		}
		if want := workSearch(299, 100) - workSearch(200, 100); want != 24 {
			t.Fatalf("the sliver is %d, the test is built for 24", want)
		}
	}
}

// G6 (struct-0206, shown EQUIVALENT): sorted does not flush the meter after a
// refusal of a limit (`if !isResourceError(slice.err)`), and a mutant that
// does changes nothing, because a meter that returned the refusal has no work
// pending: Meter.flush sets charged = units BEFORE it charges the difference,
// and the sort stops at once (sortStop; Less and Swap add nothing after
// stopped), so the flush that the mutant adds charges 0 units, and
// ChargeWork(0) of a cancelled thread returns the cancellation, which for the
// work limit is the very *WorkBudgetError that is recorded. This test holds
// what a reader of the work could see: the refusal is the one of the limit,
// the work after it is the limit, and a later run after Uncancel starts from it.
func TestG6SortedRefusalLeavesTheWorkAtTheLimit(t *testing.T) {
	const limit = 5000
	vals := make([]Value, 10000)
	for i := range vals {
		vals[i] = MakeInt(len(vals) - i)
	}
	th := &Thread{}
	th.SetMaxWork(limit)
	_, err := Call(th, Universe["sorted"], Tuple{NewList(vals)}, nil)
	var we *WorkBudgetError
	if !errors.As(err, &we) || we.Limit != limit {
		t.Fatalf("err %v, want a refusal of the work at %d", err, limit)
	}
	if th.Work() != limit {
		t.Errorf("work after the refusal %d, want the limit %d", th.Work(), limit)
	}
	th.Uncancel()
	th.SetMaxWork(2 * limit)
	if th.Work() != limit {
		t.Errorf("work after Uncancel %d, want %d", th.Work(), limit)
	}
}

// G6 (struct-0057, a timeout of the mutant): the string form of a list, tuple
// or dict that holds an int of more than MaxIntBits bits is refused with the
// error of the conversion, at once, whatever the size of the int; a writer
// without the limit converts a number of a million digits (seconds) instead.
// The int is made in Go (a script cannot make one: its operations refuse it).
func TestG6FormOfABigIntBeyondTheLimitIsRefused(t *testing.T) {
	huge := MakeBigInt(new(big.Int).Lsh(big.NewInt(1), MaxIntBits+1000000)) // 1M bits
	for _, c := range []struct {
		name string
		v    Value
	}{
		{"list", NewList([]Value{huge})},
		{"tuple", Tuple{huge}},
		{"dict key", func() Value { d := NewDict(1); d.SetKey(huge, MakeInt(1)); return d }()},
		{"dict value", func() Value { d := NewDict(1); d.SetKey(MakeInt(1), huge); return d }()},
	} {
		for _, fn := range []string{"str", "repr"} {
			start := time.Now()
			_, err := Call(&Thread{}, Universe[fn], Tuple{c.v}, nil)
			if err == nil || !strings.Contains(err.Error(), "an integer of more than 4300 decimal digits is not converted to a string") {
				t.Errorf("%s of a %s with an int of 1M bits: err %v, want the refusal of the conversion", fn, c.name, err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("%s of a %s: %v, the huge int was converted", fn, c.name, d)
			}
		}
	}
	// the text: the same as for the int alone
	_, err := Call(&Thread{}, Universe["str"], Tuple{huge}, nil)
	_, errList := Call(&Thread{}, Universe["str"], Tuple{NewList([]Value{huge})}, nil)
	t.Logf("str(int): %v / str([int]): %v", err, errList)
}

// G6 (struct-0130, a timeout of the mutant): a call looks at the frames below
// it for its function only while the stack is not deeper than
// recursionScanDepth; deeper, the active calls are counted ONCE in a map
// (thread.active) and kept. Asserted by what the scan reports (scanHook): a
// chain of 300 functions is scanned linearly for the first 33 levels, each at
// most recursionScanDepth frames, and the switch to the map is a single report
// of recursionScanDepth+1 frames; the map exists at the bottom of the chain.
// Without the switch every level reports its depth (up to 299 frames).
func TestG6RecursionScanSwitchesToTheMapBeyondTheDepth(t *testing.T) {
	var src strings.Builder
	src.WriteString("def f0():\n    return probe()\n")
	for i := 1; i < 300; i++ {
		fmt.Fprintf(&src, "def f%d():\n    return f%d()\n", i, i-1)
	}
	src.WriteString("x = f299()\n")
	var reports []int
	scanHook = func(kind string, n int) {
		if kind == "recursion" {
			reports = append(reports, n)
		}
	}
	defer func() { scanHook = nil }()
	var activeAtTheBottom bool
	th := &Thread{}
	probe := NewBuiltin("probe", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		activeAtTheBottom = th.active != nil
		return None, nil
	})
	if _, err := ExecFile(th, "scan.star", src.String(), StringDict{"probe": probe}); err != nil {
		t.Fatal(err)
	}
	maxN, over := 0, 0
	for _, n := range reports {
		maxN = max(maxN, n)
		if n > recursionScanDepth {
			over++
		}
	}
	if maxN != recursionScanDepth+1 || over != 1 {
		t.Errorf("the scans report at most %d frames, %d of them beyond %d, want %d and exactly 1 (the switch)", maxN, over, recursionScanDepth, recursionScanDepth+1)
	}
	if !activeAtTheBottom {
		t.Error("the map of the active calls does not exist at the bottom of a chain of 300 functions")
	}
	if th.active != nil {
		t.Error("the map of the active calls is kept after the chain returned")
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
