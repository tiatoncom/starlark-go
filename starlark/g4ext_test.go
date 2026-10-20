package starlark_test

import (
	"errors"
	"strconv"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// g4CollidingInts is the source of a list ks of 2000 big ints (of one word)
// that all have the hash of starlark.Int.Hash: that hash is a multiply-xorshift
// of the words, and so it is invertible (SF11 of the second review): the state
// s of the last word must have (s*K)>>32 = T, i.e. s = Kinv*(T<<32 | i), and
// the word is the one that the state came from: (Kinv*(s ^ s>>32)) ^ h0. The
// family is made here the way a script makes it, in 15 lines; no hash can stop
// it (the hash is not keyed), so the price of a chain of ints of one hash is
// that of the comparisons (TestG4EqualHashBigIntsAreCharged).
const g4CollidingInts = "M = 0xFFFFFFFFFFFFFFFF\nK = 0x9E3779B97F4A7C15\nkinv = K\nfor _ in range(6):\n    kinv = kinv * (2 - K * kinv) & M\nks = []\nfor i in range(2000):\n    s = kinv * ((777 << 32) | (i * 7919 + 1)) & M\n    ks.append(((kinv * (s ^ (s >> 32))) & M) ^ 0xCBF29CE484222325)\n"

func g4ExtWork(t *testing.T, src string) (uint64, starlark.StringDict) {
	t.Helper()
	th := &starlark.Thread{}
	g, err := starlark.ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, While: true, Set: true}, th, "g4.star", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	return th.Work(), g
}

// g4Refused runs src with the limit of work and of bytes and returns the error,
// the work and the bytes allocated.
func g4Refused(t *testing.T, src string, pre starlark.StringDict, maxWork, maxBytes uint64) (error, uint64, uint64) {
	t.Helper()
	th := &starlark.Thread{}
	th.SetMaxWork(maxWork)
	if maxBytes > 0 {
		th.SetMaxAllocBytes(maxBytes)
	}
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, While: true, Set: true}, th, "g4.star", src, pre)
	return err, th.Work(), th.AllocatedBytes()
}

func g4IsWork(err error) bool {
	var we *starlark.WorkBudgetError
	return errors.As(err, &we)
}

// S2/F5: splitlines charges the scan before it counts the lines (the count is
// the scan of the string): the refusal of a string that is too long for the
// work is a work refusal at the limit, and the count of its lines (which the
// bytes would refuse) does not come first.
func TestG4SplitlinesChargesBeforeItCounts(t *testing.T) {
	pre := starlark.StringDict{"S": starlark.String(string(make([]byte, 0)) + repeat("\n", 100000))}
	err, work, _ := g4Refused(t, "r = S.splitlines()", pre, 500, 1<<20)
	if !g4IsWork(err) || work != 500 {
		t.Errorf("splitlines of 100000 lines, work limit 500, bytes limit 1 MB: err %v, work %d, want a work refusal at 500", err, work)
	}
	// the totals are the same as before: the scan of the bytes and 4 a line
	w, _ := g4ExtWork(t, "r = ('ab\\n' * 1000).splitlines()")
	if w != 6098 { // (the same before the order of the charges was changed)
		t.Errorf("splitlines of 1000 lines: work %d, want 6098", w)
	}
}

func repeat(s string, n int) string {
	b := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}

// F5: string.join charges the elements (a unit each) before it sizes the result
// over them: a list with a non-string last element is refused for the work,
// not for the type, when the work does not cover its elements.
func TestG4JoinChargesBeforeItSizes(t *testing.T) {
	var elems []starlark.Value
	d := starlark.NewDict(5001)
	for i := 0; i < 5000; i++ {
		elems = append(elems, starlark.String("a"))
		_ = d.SetKey(starlark.String(strconv.Itoa(i)), starlark.None)
	}
	elems = append(elems, starlark.MakeInt(1))
	_ = d.SetKey(starlark.MakeInt(1), starlark.None)
	pre := starlark.StringDict{
		"L": starlark.NewList(elems),
		"T": starlark.Tuple(elems),
		"D": d,
	}
	for _, c := range []string{"L", "T", "D"} {
		err, work, _ := g4Refused(t, "r = ','.join("+c+")", pre, 3000, 0)
		if !g4IsWork(err) || work != 3000 {
			t.Errorf("join of %s (5000 strings and an int), work limit 3000: err %v, work %d, want a work refusal at 3000 (the work of the elements first)", c, err, work)
		}
	}
	w, _ := g4ExtWork(t, "r = ','.join(['ab'] * 1000)")
	if w != 2067 { // (the same before the order of the charges was changed)
		t.Errorf("join of 1000 elements: work %d, want 2067", w)
	}
}

// S1: a chain of big ints of one hash is charged 3 units a comparison (the
// word, 1, and bigEqualWork, 2; it took 1.1 for 17-26 ns): the lookup of the
// last of 2000 of them compares it with each (2000 * 3) and walks the buckets
// of the chain. (The work of the same lookup before the price: 2249, red.)
func TestG4EqualHashBigIntsAreCharged(t *testing.T) {
	const build = "d = {}\nfor k in ks:\n    d[k] = 1\n"
	base, _ := g4ExtWork(t, g4CollidingInts+build)
	w, _ := g4ExtWork(t, g4CollidingInts+build+"x = ks[1999] in d\n")
	// (the statement itself: 3 steps and the subscript, whatever the steps cost)
	_, g := g4ExtWork(t, g4CollidingInts+build)
	_ = g
	steps, _ := g4ExtWork(t, g4CollidingInts+build+"x = ks[1999]\n")
	got := w - steps
	t.Logf("base %d, with the lookup %d, with the subscript only %d: the lookup %d", base, w, steps, got)
	if want := uint64(2000*3 + 237); got != want { // (237: the buckets of the chain, the hash, lookupWork)
		t.Errorf("the lookup of the last of 2000 big ints of one hash: work %d, want %d", got, want)
	}
}

// G5-1: the family has the hash of the small int 777 (T = 777: the hash of a
// small int is 12582917 * (777 + 3)), so a small key probes the whole chain of
// big ints, and each comparison of the small key with a big int is charged as
// the one of two big ints is (3 units: the word and bigEqualWork). The work of
// the same lookup before the fix: 254 (the buckets and the hash: the
// comparisons were free).
func TestG4SmallKeyInAChainOfBigIntsIsCharged(t *testing.T) {
	const build = "d = {}\nfor k in ks:\n    d[k] = 1\n"
	steps, _ := g4ExtWork(t, g4CollidingInts+build+"x = 777\n")
	w, _ := g4ExtWork(t, g4CollidingInts+build+"x = 777 in d\n")
	got := w - steps
	t.Logf("the lookup of 777: %d", got)
	if want := uint64(2000*3 + 254); got != want {
		t.Errorf("the lookup of a small key in 2000 big ints of its hash: work %d, want %d", got, want)
	}
}

// G6 (calls-0115, calls-0197): the join of an iterable that is not a list or a
// tuple (the keys of a dict) is charged a unit for each element as it goes
// (the meter), besides the bytes of the elements it collects and the scan of
// the result: the exact work, for 1000 keys of 3 or 4 bytes with a separator
// of 1.
func TestG4JoinOfADictChargesAUnitForEachKey(t *testing.T) {
	d := starlark.NewDict(1000)
	var elems []starlark.Value
	for i := 0; i < 1000; i++ {
		d.SetKey(starlark.String(strconv.Itoa(1000+i)), starlark.None)
		elems = append(elems, starlark.String(strconv.Itoa(1000+i)))
	}
	work := func(v starlark.Value) uint64 {
		th := &starlark.Thread{}
		if _, err := starlark.ExecFile(th, "j.star", "r = ','.join(V)", starlark.StringDict{"V": v}); err != nil {
			t.Fatal(err)
		}
		return th.Work()
	}
	wd, wl := work(d), work(starlark.NewList(elems))
	t.Logf("join of 1000 keys: dict %d, list %d", wd, wl)
	// the list: its length + the scan of the 4999 bytes of the result (+ the call);
	// the dict: the same, and the elements it collects are charged to the budget
	// (a slot each: allocBytesPerValue / allocBytesPerWork units)
	// list: 1000 (a unit for each element, charged before the sizing) + 89 (the
	// scan of the result); dict: 1000 (the meter, a unit for each key as it
	// comes) + 1000 (the slots it collects: 16 bytes each, a unit for 16) + 89.
	// Without the meter's unit (calls-0115) the dict is 1089, with less than
	// the unit (calls-0197, halved) less than 2089: both are red here.
	if wd != 2089 || wl != 1089 {
		t.Errorf("join of 1000 keys: dict %d, list %d, want 2089 and 1089", wd, wl)
	}
}

// The family is real: 2000 distinct ints of one hash.
func TestG4CollidingFamilyIsOneHash(t *testing.T) {
	_, g := g4ExtWork(t, g4CollidingInts)
	ks := g["ks"].(*starlark.List)
	seen := map[uint32]bool{}
	ints := map[string]bool{}
	for i := 0; i < ks.Len(); i++ {
		x := ks.Index(i).(starlark.Int)
		h, err := x.Hash()
		if err != nil {
			t.Fatal(err)
		}
		seen[h] = true
		ints[x.String()] = true
	}
	if len(seen) != 1 || len(ints) != 2000 {
		t.Errorf("%d hashes for %d distinct ints, want 1 for 2000 (the formula of the inverse is wrong)", len(seen), len(ints))
	}
}
