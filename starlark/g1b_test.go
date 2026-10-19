package starlark

// Tests of group G1 (time), part b: the price of startswith/endswith with a
// tuple, of dir, of the first error of sorted, of the scans that are charged
// before they are made (find, partition, split and their reverses), and of
// int(str) of a mid-size digit string. Every number is asserted by equality
// with a formula derived from the price, not read off the code under test.

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

// g1bCall returns what one call of fn (bound to recv) is charged, from Go, on a
// new thread (the work beyond the steps: there are none).
func g1bCall(t *testing.T, fn *Builtin, recv Value, args Tuple) uint64 {
	t.Helper()
	th := &Thread{}
	if recv != nil {
		fn = fn.BindReceiver(recv)
	}
	if _, err := Call(th, fn, args, nil); err != nil {
		t.Fatal(err)
	}
	return th.Work() - th.Steps
}

// g1bRep is the string of n bytes c.
func g1bRep(c string, n int) String { return String(strings.Repeat(c, n)) }

// g1bPrefixWork is the work of startswith/endswith of k prefixes of m bytes on
// a string of n bytes (the effective one): the call, a unit for each element of
// the tuple, and the bytes that can be compared of each, in 64s.
func g1bPrefixWork(k, m, n int) uint64 {
	return isBaseCall + uint64(k)*(1+uint64(min(m, n))/64)
}

// Work is proportional to the bytes compared: the prices are exact for several
// (k, m, n), they grow with m, and a plain string is the same formula without
// the unit of the tuple.
func TestG1bStartsEndsWith_WorkIsBytesCompared(t *testing.T) {
	for _, name := range []string{"startswith", "endswith"} {
		fn := stringMethods[name]
		for _, c := range []struct{ k, m, n int }{
			{1, 0, 100}, {1, 64, 100}, {3, 64, 1000}, {10, 1000, 100}, {10, 6400, 6400},
			{100, 128, 640}, {5, 100000, 50}, {1000, 64, 1 << 20}, {2, 1 << 20, 1 << 20},
		} {
			s, p := g1bRep("b", c.n), g1bRep("a", c.m)
			tup := make(Tuple, c.k)
			for i := range tup {
				tup[i] = p
			}
			if got, want := g1bCall(t, fn, s, Tuple{tup}), g1bPrefixWork(c.k, c.m, c.n); got != want {
				t.Errorf("%s: %d prefixes of %d bytes on %d bytes: work %d, want %d", name, c.k, c.m, c.n, got, want)
			}
			if c.k == 1 {
				want := isBaseCall + uint64(min(c.m, c.n))/64
				if got := g1bCall(t, fn, s, Tuple{p}); got != want {
					t.Errorf("%s: a string of %d bytes on %d bytes: work %d, want %d", name, c.m, c.n, got, want)
				}
			}
		}
		// the start and end indices narrow the string that is compared
		s, p := g1bRep("b", 10000), g1bRep("a", 6400)
		if got, want := g1bCall(t, fn, s, Tuple{Tuple{p, p}, MakeInt(100), MakeInt(740)}), g1bPrefixWork(2, 6400, 640); got != want {
			t.Errorf("%s with indices: work %d, want %d", name, got, want)
		}
		// grows with m (up to n), and with k
		prev := uint64(0)
		for _, m := range []int{64, 640, 6400, 64000} {
			w := g1bCall(t, fn, g1bRep("b", 100000), Tuple{Tuple{g1bRep("a", m)}})
			if w <= prev {
				t.Errorf("%s: work %d for a prefix of %d bytes does not grow (%d before)", name, w, m, prev)
			}
			prev = w
		}
	}
	// the attack of the review: 100000 prefixes of 1 MB. Priced before the call.
	th := &Thread{}
	th.SetMaxWork(1000000)
	p := g1bRep("a", 1<<20)
	tup := make(Tuple, 100000)
	for i := range tup {
		tup[i] = p
	}
	b := stringMethods["startswith"].BindReceiver(g1bRep("b", 1<<20))
	_, err := Call(th, b, Tuple{tup}, nil)
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Errorf("startswith of 100000 prefixes of 1 MB under 1M units: got %v, want a refusal of the work", err)
	}
}

// g1bAttrs is a host value with n attributes.
type g1bAttrs struct{ names []string }

func (g1bAttrs) String() string               { return "g1b" }
func (g1bAttrs) Type() string                 { return "g1b" }
func (g1bAttrs) Freeze()                      {}
func (g1bAttrs) Truth() Bool                  { return True }
func (g1bAttrs) Hash() (uint32, error)        { return 0, nil }
func (g g1bAttrs) Attr(string) (Value, error) { return nil, nil }
func (g g1bAttrs) AttrNames() []string        { return append([]string(nil), g.names...) }

func g1bMakeAttrs(n int) g1bAttrs {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("f%07d", n-i) // not sorted
	}
	return g1bAttrs{names}
}

// dir is charged by the number of names: a call (12), and for each of the n
// names three units (the copy of the names, the boxing of each, the slot of the
// list) and a unit for each level of the sort (a comparison of two strings is
// ~12 ns, n log2 n of them): n * (3 + bits.Len(n)).
func g1bDirWork(n int) uint64 { return 12 + uint64(n)*(3+uint64(bits.Len(uint(n)))) }

func TestG1bDir_WorkIsTheNumberOfNames(t *testing.T) {
	fn := Universe["dir"].(*Builtin)
	prev := uint64(0)
	for _, n := range []int{0, 1, 7, 100, 1000, 20000} {
		got := g1bCall(t, fn, nil, Tuple{g1bMakeAttrs(n)})
		if want := g1bDirWork(n); got != want {
			t.Errorf("dir of a value of %d attributes: work %d, want %d", n, got, want)
		}
		if got < prev {
			t.Errorf("dir: work %d for %d attributes is less than %d", got, n, prev)
		}
		prev = got
	}
	// the types of the language
	for _, v := range []Value{String(""), NewList(nil), NewDict(0), NewSet(0), Bytes("")} {
		n := len(v.(HasAttrs).AttrNames())
		if got, want := g1bCall(t, fn, nil, Tuple{v}), g1bDirWork(n); got != want {
			t.Errorf("dir of a %s (%d attributes): work %d, want %d", v.Type(), n, got, want)
		}
	}
	// a value without attributes
	if got, want := g1bCall(t, fn, nil, Tuple{MakeInt(1)}), g1bDirWork(0); got != want {
		t.Errorf("dir(1): work %d, want %d", got, want)
	}
	// refused by the limit before the sort
	th := &Thread{}
	th.SetMaxWork(1000)
	_, err := Call(th, fn, Tuple{g1bMakeAttrs(20000)}, nil)
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Errorf("dir of 20000 attributes under 1000 units: got %v, want a refusal of the work", err)
	}
}

// g1bBad is a value that is comparable to its kind, and fails every
// comparison, counting them.
type g1bBad struct{ calls *int }

func (g1bBad) String() string        { return "bad" }
func (g1bBad) Type() string          { return "g1bbad" }
func (g1bBad) Freeze()               {}
func (g1bBad) Truth() Bool           { return True }
func (g1bBad) Hash() (uint32, error) { return 0, nil }
func (b g1bBad) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	*b.calls++
	return false, errors.New("g1bbad: incomparable")
}

// g1bExec runs src with env and returns the work between reset() and the end,
// and the error.
func g1bExec(src string, env StringDict, maxWork uint64) (uint64, error) {
	th := &Thread{}
	th.SetMaxWork(maxWork)
	var w0, s0 uint64
	pre := StringDict{"reset": NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		w0, s0 = th.Work(), th.Steps
		return None, nil
	})}
	for k, v := range env {
		pre[k] = v
	}
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "t.star", src, pre)
	return (th.Work() - w0) - (th.Steps - s0), err
}

// sorted stops at the first error, of any kind: no comparison is made after it,
// and the work is that of the work up to it.
func TestG1bSorted_StopsAtTheFirstError(t *testing.T) {
	for _, n := range []int{2, 1000, 100000} {
		calls := 0
		vals := make([]Value, n)
		for i := range vals {
			vals[i] = g1bBad{&calls}
		}
		_, err := g1bExec("reset()\nr = sorted(l)\n", StringDict{"l": NewList(vals)}, 0)
		if err == nil || !strings.Contains(err.Error(), "incomparable") {
			t.Fatalf("n=%d: got %v, want the error of the comparison", n, err)
		}
		if calls != 1 {
			t.Errorf("n=%d: %d comparisons were made, want 1 (none after the first error)", n, calls)
		}
	}
	// reverse=True and a key function stop as well
	calls := 0
	vals := make([]Value, 1000)
	for i := range vals {
		vals[i] = g1bBad{&calls}
	}
	if _, err := g1bExec("reset()\nr = sorted(l, reverse=True)\n", StringDict{"l": NewList(vals)}, 0); err == nil || calls != 1 {
		t.Errorf("reverse: %d comparisons, error %v: want 1 and an error", calls, err)
	}

	// mixed types: the work of the call of 2N elements is that of 2 elements
	// (the first comparison fails), whatever N is
	base, err := g1bExec("l = [1, 'a']\nreset()\nr = sorted(l)\n", nil, 0)
	if err == nil {
		t.Fatal("sorted([1, 'a']) did not fail")
	}
	for _, n := range []int{1000, 100000} {
		w, err := g1bExec(fmt.Sprintf("l = [1, 'a'] * %d\nreset()\nr = sorted(l)\n", n), nil, 0)
		if err == nil {
			t.Fatal("sorted of mixed elements did not fail")
		}
		// the work of the first comparison, and a unit for each element that is
		// copied into the list that is sorted (that is made before)
		if want := base + uint64(2*n-2); w != want {
			t.Errorf("sorted([1, 'a'] * %d): work %d, want %d (that of the first comparison and of the copy)", n, w, want)
		}
	}
	// the work that was recorded before the error is charged (not lost in the meter)
	if w, _ := g1bExec("l = [3, 2, 1, 'a']\nreset()\nr = sorted(l)\n", nil, 0); w == 0 {
		t.Errorf("the work of the comparisons before the error was lost")
	}
}

// The scans that find, index, partition and their reverses make are charged
// before they are made, in chunks: a call that is refused asks for at most a
// chunk of 1024 units, not for the whole scan (which would have been done).
func TestG1bFindFamily_ChargedBeforeTheScan(t *testing.T) {
	const n = 1 << 22
	hay := g1bRep("a", n)
	needle := String(strings.Repeat("a", 99) + "b") // never found: the whole haystack
	for _, name := range []string{"find", "rfind", "index", "rindex", "partition", "rpartition"} {
		fn := stringMethods[name]
		// the whole scan, not found: the old price, exactly
		want := isBaseCall + workSearch(n, 100)
		if got := g1bCall2(fn, hay, Tuple{needle}); got != want {
			t.Errorf("%s: work %d, want %d (call + the scan)", name, got, want)
		}
		// refused under a limit that is below the scan: the request is a chunk
		th := &Thread{}
		th.SetMaxWork(50000)
		_, err := Call(th, fn.BindReceiver(hay), Tuple{needle}, nil)
		var we *WorkBudgetError
		if !errors.As(err, &we) {
			t.Fatalf("%s: got %v, want a refusal of the work", name, err)
		}
		if we.Requested > flushWork+1 {
			t.Errorf("%s: the refused request is %d units, want at most a chunk (%d): the scan was charged after it was made", name, we.Requested, flushWork+1)
		}
		if th.Work() != 50000 {
			t.Errorf("%s: work %d at the refusal, want the limit 50000", name, th.Work())
		}
	}
	// a match near the start costs little (windows grow from 64 bytes), by every
	// needle class
	for _, m := range []int{1, 2, 16, 17, 63, 64, 200} {
		needle := g1bRep("a", m)
		at := 70 // the match starts at the start: the windows are 64, 128, ...
		hay := g1bRep("x", at) + needle + g1bRep("x", n)
		for _, name := range []string{"find", "index", "partition"} {
			got := g1bCall2(stringMethods[name], hay, Tuple{needle}) - isBaseCall
			if max := workSearch(2*(at+m)+64+m, m) + 1; got > max {
				t.Errorf("%s, needle %d: a match at %d is charged %d, want at most %d", name, m, at, got, max)
			}
			if min := workSearch(at+m, m); got < min {
				t.Errorf("%s, needle %d: a match at %d is charged %d, want at least %d", name, m, at, got, min)
			}
		}
	}
}

func g1bCall2(fn *Builtin, recv Value, args Tuple) uint64 {
	th := &Thread{}
	if _, err := Call(th, fn.BindReceiver(recv), args, nil); err != nil {
		if !strings.Contains(err.Error(), "not found") {
			panic(err)
		}
	}
	return th.Work() - th.Steps
}

// split and rsplit count the fields before they allocate them, and the count is
// a scan: it is charged before it is made. (A string of 'a' split by 'a' has
// n+1 fields: the allocation check that follows the count refuses it, and if
// the scan is charged first the refusal is that of the work.)
func TestG1bSplit_ScanChargedBeforeTheCount(t *testing.T) {
	const n = 1 << 22
	for _, c := range []struct {
		name string
		recv String
		args Tuple
	}{
		{"split", g1bRep("a", n), Tuple{String("a")}},
		{"rsplit", g1bRep("a", n), Tuple{String("a")}},
		{"split", g1bRep("a ", n/2), nil},
	} {
		th := &Thread{}
		th.SetMaxWork(1000)
		th.SetMaxAllocBytes(100000)
		_, err := Call(th, stringMethods[c.name].BindReceiver(c.recv), c.args, nil)
		var we *WorkBudgetError
		if !errors.As(err, &we) {
			t.Errorf("%s(%v) on %d bytes: got %v, want the refusal of the work (the scan is charged first)", c.name, c.args, len(c.recv), err)
			continue
		}
		if we.Requested > uint64(n) { // the scan, not the scan and the fields
			t.Errorf("%s: requested %d", c.name, we.Requested)
		}
	}
	// the totals are what they were: the scan and 4 units a field
	th := &Thread{}
	hay := g1bRep("ab", 1000)
	if _, err := Call(th, stringMethods["split"].BindReceiver(hay), Tuple{String("b")}, nil); err != nil {
		t.Fatal(err)
	}
	// (the 6043 of the code before: the call, the scan, the 1001 fields that
	// are charged 4 units for the list of them and 4 for the strings)
	if got, want := th.Work()-th.Steps, uint64(6043); got != want {
		t.Errorf("split: work %d, want %d", got, want)
	}
}

// int(str): a unit for each digit, and digits^2/4096. (The linear term is the
// parse, ~5 ns a digit, the cost of the call and of the big integer that is
// made, 20-100 digits: measured 424 ns at 20 digits, 1.0 us at 100, 5.3 us at
// 600, 54 us at 4300; the quadratic term alone was 2.5-4.5x C at 20-600.)
func TestG1bIntOfStr_WorkIsLinearAndQuadratic(t *testing.T) {
	for _, d := range []int{20, 100, 600, 4300} {
		got := isRun(t, StringDict{"s": g1bRep("7", d)}, "", "r = int(s)")
		want := isBaseCall + isMakeBigInt + uint64(d) + uint64(d)*uint64(d)/4096
		if got.work != want {
			t.Errorf("int of %d digits: work %d, want %d", d, got.work, want)
		}
	}
}
