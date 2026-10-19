package starlark

// Tests of group G2 of the release review of v0.3.0 (the bytes budget): the
// memory that was retained or allocated and not charged, and the sizes that
// wrapped around in an addition and skipped their check.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"go.starlark.net/syntax"
)

// g2Run is the result of g2Exec.
type g2Run struct {
	th      *Thread
	globals StringDict
	err     error
	marks   []uint64 // AllocatedBytes at each mark()
	memMark uint64   // runtime TotalAlloc at the first mark()
	memEnd  uint64   // runtime TotalAlloc at the end
}

// delta is what the program charged between the first and the second mark().
func (r *g2Run) delta() uint64 { return r.marks[1] - r.marks[0] }

// g2Exec runs src on a thread with the budget (0: none) and the work limit
// (0: none). A scenario that is refused only by an overflowing check could
// allocate until the machine is out of memory, so the run has two guards that
// make the failure of a test a failure and not a crash: a limit of work (the
// loops that fill a table charge it) and a watcher of the heap that ends the
// process with a message when the heap passes 3 GiB.
func g2Exec(t *testing.T, budget, work uint64, src string, extra StringDict) *g2Run {
	t.Helper()
	r := &g2Run{th: &Thread{Name: "g2"}}
	r.th.SetMaxAllocBytes(budget)
	if work != 0 {
		r.th.SetMaxWork(work)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				if ms.HeapAlloc > 3<<30 {
					fmt.Fprintf(os.Stderr, "G2-OOM-GUARD: the heap is %d MiB in %s: %q\n", ms.HeapAlloc>>20, t.Name(), src)
					os.Exit(3)
				}
			}
		}
	}()
	predeclared := StringDict{
		"mark": NewBuiltin("mark", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
			r.marks = append(r.marks, th.AllocatedBytes())
			if len(r.marks) == 1 {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				r.memMark = ms.TotalAlloc
			}
			return None, nil
		}),
	}
	for k, v := range extra {
		predeclared[k] = v
	}
	opts := &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}
	r.globals, r.err = ExecFileOptions(opts, r.th, "g2.star", src, predeclared)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.memEnd = ms.TotalAlloc
	return r
}

func g2StrData(s string) *byte { return unsafe.StringData(s) }

// ---- B2: rsplit slices the receiver, it does not join a copy of its prefix ----

func TestG2_RsplitPrefixSharesTheReceiver(t *testing.T) {
	recv := strings.Repeat("a,", 5000) + "z" // 5001 fields
	for _, c := range []struct {
		op    string
		first string // the first field of the result
		n     int    // fields of the result
	}{
		{"s.rsplit(',', 3)", recv[:len(recv)-len(",a,a,z")], 4},
		{"s.rsplit(',', 1)", recv[:len(recv)-len(",z")], 2},
		{"s.rsplit(',', 0)", recv, 1},
		{"s.rsplit(',', 5000)", "a", 5001},
		{"s.rsplit(',', 9999999)", "a", 5001},
	} {
		r := g2Exec(t, 0, 0, "mark()\nr = "+c.op+"\nmark()\n", StringDict{"s": String(recv)})
		if r.err != nil {
			t.Fatalf("%s: %v", c.op, r.err)
		}
		list := r.globals["r"].(*List)
		if list.Len() != c.n {
			t.Fatalf("%s: %d fields, want %d", c.op, list.Len(), c.n)
		}
		first := string(list.elems[0].(String))
		if first != c.first {
			t.Errorf("%s: first field of %d bytes, want %d", c.op, len(first), len(c.first))
		}
		// The first field is a prefix of the receiver: it IS the receiver's
		// memory (retained <= charged: a copy of it would be retained and not
		// charged).
		if first != "" && g2StrData(first) != g2StrData(recv) {
			t.Errorf("%s: the first field is a copy of the receiver's prefix (%d bytes), not a slice of it", c.op, len(first))
		}
		// All the fields are charged (v0.2.0 behaviour of the count: the split
		// is of the whole string), 32 bytes each and the list.
		want := lb(0) + 32*uint64(5001)
		if got := r.delta(); got != want {
			t.Errorf("%s: charged %d bytes, want %d", c.op, got, want)
		}
	}
}

func TestG2_RsplitRepeatedCallsRetainWhatTheyCharged(t *testing.T) {
	recv := strings.Repeat("abcdefghi,", 200000) // 2 MB, 200001 fields
	r := g2Exec(t, 0, 0, "mark()\nkeep = []\nfor i in range(20):\n  keep.append(s.rsplit(',', 1))\nmark()\n", StringDict{"s": String(recv)})
	if r.err != nil {
		t.Fatal(r.err)
	}
	// 20 results of 2 fields, each with a first field of 2 MB: sliced, they
	// retain no copy. The allocation is the lists and the split of every call
	// (a []string of 200001 headers), not 20 copies of the 2 MB prefix.
	if copies := r.memEnd - r.memMark; copies > 20*(200001*16+(1<<20)) {
		t.Errorf("20 calls of rsplit allocated %d MB: the prefix is copied", copies>>20)
	}
}

// ---- B3, SF8: sizes that are summed wrap around ----

func TestG2_AddIntSaturates(t *testing.T) {
	for _, c := range []struct{ a, b, want int }{
		{1, 2, 3},
		{math.MaxInt, 0, math.MaxInt},
		{math.MaxInt, 1, math.MaxInt},
		{math.MaxInt - 5, 10, math.MaxInt},
		{math.MaxInt / 2, math.MaxInt/2 + 5, math.MaxInt},
		{0, 0, 0},
	} {
		if got := addSizes(c.a, c.b); got != c.want {
			t.Errorf("addSizes(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := addSizes(math.MaxInt, 7, 9); got != math.MaxInt {
		t.Errorf("addSizes of three: %d", got)
	}
}

func TestG2_BigIntAllocBytes(t *testing.T) {
	for _, bits := range []int{-5, 0, 1, 31} {
		if got := BigIntAllocBytes(bits); got != 0 {
			t.Errorf("BigIntAllocBytes(%d) = %d, want 0 (a small int)", bits, got)
		}
	}
	for _, bits := range []int{32, 33, 63, 64, 65, 127, 128, 129, 4000, 64000, 1 << 20, math.MaxInt} {
		want := intBytesOfWords((uint64(bits) + 63) / 64)
		if got := BigIntAllocBytes(bits); got != want {
			t.Errorf("BigIntAllocBytes(%d) = %d, want %d", bits, got, want)
		}
	}
	if got := BigIntAllocBytes(64); got != 64+9 {
		t.Errorf("BigIntAllocBytes(64) = %d, want 73", got)
	}
	// the same as the charge of an integer the package makes
	for _, s := range []string{"1 << 40", "1 << 64", "(1 << 200) + 1", "((1 << 500) * (1 << 500) * (1 << 500) * (1 << 500)) - 1"} {
		r := g2Exec(t, 0, 0, "x = "+s+"\nmark()\nl = [x]\nmark()\n", nil)
		if r.err != nil {
			t.Fatal(r.err)
		}
		x := r.globals["x"].(Int)
		if got, want := bigIntBytes(x), BigIntAllocBytes(x.BigInt().BitLen()); got != want {
			t.Errorf("%s: bigIntBytes = %d, BigIntAllocBytes = %d", s, got, want)
		}
	}
}

func TestG2_SliceLenDoesNotWrap(t *testing.T) {
	for _, c := range []struct{ start, end, step, want int }{
		{0, 10, 1, 10},
		{0, 10, 3, 4},
		{0, 10, 10, 1},
		{0, 10, math.MaxInt, 1},
		{0, 3, math.MaxInt - 1, 1},
		{3, 0, -1, 3},
		{10, 0, -3, 4},
		{10, 0, math.MinInt, 1},
		{10, 0, math.MinInt + 1, 1},
		{5, 5, math.MaxInt, 0},
		{5, 5, math.MinInt, 0},
	} {
		if got := sliceLen(c.start, c.end, c.step); got != c.want {
			t.Errorf("sliceLen(%d, %d, %d) = %d, want %d", c.start, c.end, c.step, got, c.want)
		}
	}
}

func TestG2_SliceWithAHugeStep(t *testing.T) {
	for _, op := range []string{
		// (a step is an int32: the largest, which wraps on a 32-bit platform)
		"[1, 2, 3][::2147483647]",
		"(1, 2, 3)[::2147483647]",
		"'abc'[::2147483647]",
		"b'abc'[::2147483647]",
		"[1, 2, 3][::-2147483647]",
		"[1, 2, 3][::-2147483647 - 1]",
		"'abc'[::-2147483647 - 1]",
	} {
		for _, budget := range []uint64{0, 1 << 20} {
			r := g2Exec(t, budget, 0, "r = "+op+"\n", nil)
			if r.err != nil {
				t.Errorf("%s (budget %d): %v", op, budget, r.err)
			}
		}
	}
}

// The scenarios of the review (a size of 2**63-1 elements, MaxInt) that have
// to be refused before anything is allocated: with a budget by an
// *AllocBudgetError and with none by the error of the ceiling, and in both
// the thread has allocated nothing for them.
var g2OverflowScenarios = []struct{ name, op string }{
	{"set.union", "s.union(range(-(1 << 62), (1 << 62) - 1))"},
	{"set.union of two", "s.union(range(1 << 62), range(1 << 62))"},
	{"set.symmetric_difference", "s.symmetric_difference(range(-(1 << 62), (1 << 62) - 1))"},
	{"set.update", "s.update(range(-(1 << 62), (1 << 62) - 1))"},
	{"set.update of two", "s.update(range(1 << 62), range(1 << 62))"},
	{"list.extend", "l.extend(range(-(1 << 62), (1 << 62) - 1))"},
	{"list +=", "l += range(-(1 << 62), (1 << 62) - 1)"},
	{"f(*range)", "f(1, 2, *range(-(1 << 62), (1 << 62) - 1))"},
	{"f(*range), one positional", "f(1, *range(-(1 << 62), (1 << 62) - 1))"},
}

const g2OverflowSetup = "s = set(range(10))\nl = [1, 2, 3]\ndef f(*a):\n  return 0\nmark()\n"

func TestG2_SumOfSizesThatWrapsIsRefusedBeforeAllocating(t *testing.T) {
	const budget = 100 << 20
	for _, c := range g2OverflowScenarios {
		// The guard of the work (4M units) turns an unrefused scenario into
		// an error of the work and not into an exhausted machine.
		r := g2Exec(t, budget, 1<<22, g2OverflowSetup+c.op+"\n", nil)
		var be *AllocBudgetError
		if !errors.As(r.err, &be) {
			t.Errorf("%s with a budget: error = %v, want an *AllocBudgetError", c.name, r.err)
		}
		if got := r.th.AllocatedBytes(); got > budget {
			t.Errorf("%s: allocated %d bytes, over the budget", c.name, got)
		}
		if spent := r.memEnd - r.memMark; spent > 1<<20 {
			t.Errorf("%s: the process allocated %d bytes before the refusal", c.name, spent)
		}

		r = g2Exec(t, 0, 1<<22, g2OverflowSetup+c.op+"\n", nil)
		if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
			t.Errorf("%s without a budget: error = %v, want the error of the ceiling", c.name, r.err)
		}
		var we *WorkBudgetError
		if errors.As(r.err, &we) {
			t.Errorf("%s without a budget: the refusal came from the work (%v): the size was not checked", c.name, r.err)
		}
		if spent := r.memEnd - r.memMark; spent > 1<<20 {
			t.Errorf("%s without a budget: the process allocated %d bytes before the refusal", c.name, spent)
		}
	}
}

// A dict or set of before entries that grows by bound entries: the sum is
// saturated, so the check sees a size beyond the ceiling.
func TestG2_RoomGrowthWithHugeBound(t *testing.T) {
	th := &Thread{}
	if err := th.roomGrowth(10, math.MaxInt); err != errExcessive {
		t.Errorf("roomGrowth(10, MaxInt) = %v, want errExcessive", err)
	}
	if err := th.roomGrowth(math.MaxInt, math.MaxInt); err != errExcessive {
		t.Errorf("roomGrowth(MaxInt, MaxInt) = %v, want errExcessive", err)
	}
	if err := th.roomEntries(math.MaxInt); err != errExcessive {
		t.Errorf("roomEntries(MaxInt) = %v, want errExcessive", err)
	}
	if b := growthBytes(10, math.MaxInt); b < uint64(maxAlloc) {
		t.Errorf("growthBytes(10, MaxInt) = %d, want over the ceiling", b)
	}
	th.SetMaxAllocBytes(100 << 20)
	var be *AllocBudgetError
	if err := th.roomGrowth(10, math.MaxInt); !errors.As(err, &be) {
		t.Errorf("roomGrowth under a budget = %v, want an *AllocBudgetError", err)
	}
}

// ---- SF7: the elements of a range beyond int32 are boxed ----

func TestG2_RangeBeyondInt32IsChargedByItsBoxes(t *testing.T) {
	const box = 73 // intBytesOfWords(1)
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"list(range) small", "a = 0\nb = 1000", "r = list(range(a, b))", lb(1000)},
		{"list(range) at int32", "a = 2147483000\nb = 2147483647", "r = list(range(a, b))", lb(647)},
		{"list(range) beyond", "a = (1 << 31)\nb = a + 1000", "r = list(range(a, b))", lb(1000) + 1000*box},
		{"list(range) beyond, negative", "a = -((1 << 31)) - 1000\nb = -((1 << 31))", "r = list(range(a, b))", lb(1000) + 1000*box},
		{"list(range) straddling", "a = (1 << 31) - 500\nb = a + 1000", "r = list(range(a, b))", lb(1000) + 1000*box},
		{"list(range) beyond, step", "a = (1 << 31)\nb = a + 6000", "r = list(range(a, b, 6))", lb(1000) + 1000*box},
		{"tuple(range) beyond", "a = (1 << 31)\nb = a + 1000", "r = tuple(range(a, b))", tb(1000) + 1000*box},
		{"tuple(range) small", "a = 0\nb = 1000", "r = tuple(range(a, b))", tb(1000)},
		{"sorted(range) beyond", "a = (1 << 31)\nb = a + 1000", "r = sorted(range(a, b))", lb(1000) + 1000*box},
		{"reversed(range) beyond", "a = (1 << 31)\nb = a + 1000", "r = reversed(range(a, b))", lb(1000) + 1000*box},
	} {
		r := g2Exec(t, 0, 0, c.setup+"\nmark()\n"+c.op+"\nmark()\n", nil)
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.delta(); got != c.want {
			t.Errorf("%s: charged %d bytes, want %d", c.name, got, c.want)
		}
	}
}

func TestG2_RangeBeyondInt32IsRefusedByTheBudget(t *testing.T) {
	// 1M elements of 89 bytes do not fit in a budget of 64 MiB, and are
	// refused before the list is made (16 bytes each would have fitted).
	const budget = 64 << 20
	r := g2Exec(t, budget, 0, "a = (1 << 31)\nb = a + 1000000\nmark()\nr = list(range(a, b))\n", nil)
	var be *AllocBudgetError
	if !errors.As(r.err, &be) {
		t.Fatalf("error = %v, want an *AllocBudgetError", r.err)
	}
	if spent := r.memEnd - r.memMark; spent > 1<<20 {
		t.Errorf("the process allocated %d bytes before the refusal", spent)
	}
}

// ---- SF9: rsplit(None, n) does not preallocate by the length of the input ----

func TestG2_RsplitOfWhitespaceDoesNotPreallocateByInputLength(t *testing.T) {
	s := strings.Repeat(" ", 4_000_000) + "a"
	for _, op := range []string{
		"s.rsplit(None, 1 << 40)",
		"s.rsplit(None, 9223372036854775807)",
		"s.rsplit(None, 100000)",
		"s.rsplit(None)",
		"s.rsplit()",
	} {
		r := g2Exec(t, 1<<20, 0, "mark()\nr = "+op+"\nmark()\n", StringDict{"s": String(s)})
		if r.err != nil {
			t.Errorf("%s: %v", op, r.err)
			continue
		}
		if spent := r.memEnd - r.memMark; spent > 256<<10 {
			t.Errorf("%s: the process allocated %d bytes for a result of one field", op, spent)
		}
		if got := r.globals["r"].(*List); got.Len() != 1 || got.elems[0] != String("a") {
			t.Errorf("%s = %v", op, got)
		}
	}
}

func TestG2_RsplitOfWhitespaceIsStillCorrect(t *testing.T) {
	for _, c := range []struct {
		s        string
		maxsplit int
		want     []string
	}{
		{"a b c d", 0, []string{"a b c d"}},
		{"a b c d", 1, []string{"a b c", "d"}},
		{"a b c d", 2, []string{"a b", "c", "d"}},
		{"a b c d", 3, []string{"a", "b", "c", "d"}},
		{"a b c d", 100, []string{"a", "b", "c", "d"}},
		{"  a  b  ", 1, []string{"  a", "b"}},
		{"   ", 5, nil},
		{"", 5, nil},
	} {
		r := g2Exec(t, 0, 0, fmt.Sprintf("r = s.rsplit(None, %d)\n", c.maxsplit), StringDict{"s": String(c.s)})
		if r.err != nil {
			t.Errorf("rsplit(%q, %d): %v", c.s, c.maxsplit, r.err)
			continue
		}
		var got []string
		for _, v := range r.globals["r"].(*List).elems {
			got = append(got, string(v.(String)))
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("rsplit(%q, %d) = %q, want %q", c.s, c.maxsplit, got, c.want)
		}
	}
}

// ---- SF5: a string that is not valid UTF-8 grows when its case is mapped ----

func TestG2_CaseMapsChargeTheResultNotTheOperand(t *testing.T) {
	// Charged exactly the bytes of the result: an invalid byte becomes U+FFFD
	// (three bytes), a rune may grow (U+023F, 2 bytes, upper-cases to U+2C7E,
	// 3) or shrink (U+2C65, 3 bytes, upper-cases to U+023A, 2), the plain
	// ones keep their size.
	in := StringDict{
		"bad": String(strings.Repeat("\xff", 10000)),    // 10000 bytes -> 30000
		"g":   String(strings.Repeat("ȿ", 1000)),        // 2000 bytes -> 3000
		"h":   String(strings.Repeat("ⱥ", 1000)),        // 3000 bytes -> 2000
		"c":   String(strings.Repeat("привет", 100)),    // 1200 -> 1200
		"a":   String(strings.Repeat("abc", 100)),       // 300 -> 300
		"m":   String("ab" + strings.Repeat("\xff", 5)), // 7 -> 17
	}
	for _, c := range []struct {
		op   string
		want int // bytes of the result
	}{
		{"bad.upper()", 30000},
		{"bad.lower()", 30000},
		{"bad.title()", 30000},
		{"bad.capitalize()", 30000},
		{"m.upper()", 17},
		{"m.title()", 17},
		{"g.upper()", 3000},
		{"h.upper()", 2000},
		{"c.upper()", 1200},
		{"a.upper()", 300},
		{"g.title()", 2000 + 1}, // lower-cased after the first, which is title-cased
		{"g.capitalize()", 2000 + 1},
	} {
		r := g2Exec(t, 0, 0, "mark()\nr = "+c.op+"\nmark()\n", in)
		if r.err != nil {
			t.Errorf("%s: %v", c.op, r.err)
			continue
		}
		res := string(r.globals["r"].(String))
		if len(res) != c.want {
			t.Errorf("%s: the result has %d bytes, the test expects %d", c.op, len(res), c.want)
		}
		if got := r.delta(); got != uint64(len(res)) {
			t.Errorf("%s: charged %d bytes for a result of %d", c.op, got, len(res))
		}
	}
}

// upperLower must give what strings.ToUpper / ToLower give, with its length
// computed (and the width table consulted) exactly: over every rune, and
// over strings that mix valid runes, invalid bytes and truncated sequences.
func TestG2_UpperLowerEqualsTheStringsPackage(t *testing.T) {
	check := func(s string) {
		t.Helper()
		for _, upper := range []bool{true, false} {
			want := strings.ToLower(s)
			if upper {
				want = strings.ToUpper(s)
			}
			n := upperLowerLen(s, upper)
			if n != len(want) {
				t.Fatalf("upperLowerLen(%q, %v) = %d, want %d", s, upper, n, len(want))
			}
			if got := upperLower(s, n, upper); got != want {
				t.Fatalf("upperLower(%q, %v) = %q, want %q", s, upper, got, want)
			}
		}
	}
	var all strings.Builder
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r < 0xE000 {
			continue
		}
		all.WriteRune(r)
		if r%64 == 63 { // check in pieces, one string per 64 runes
			check(all.String())
			all.Reset()
		}
	}
	for _, s := range []string{"", "a", "é", "\xc3", "\xc3a", "\xe2\x82", "\xf0\x9f", "\xff\xfe", "a\xc3", "ȿ\xc3ⱥ", "\xef\xbf\xbd", "ǆ", "\xed\xa0\x80", "\xc0\x80", "\xf4\x90\x80\x80", "ß", "İ", "ΣΑΣ"} {
		check(s)
	}
}

// title and capitalize against the definition they had (a closure over the
// runes that asks isCasedRune of the mapped rune), for every rune and for
// mixes of runes and invalid bytes; and the table of cased runes against
// isCasedRune for every rune.
func TestG2_CaseTablesAreExact(t *testing.T) {
	for r := rune(0); r < caseWidthLimit; r++ {
		if r < utf8.RuneSelf {
			continue
		}
		if got, want := isCasedAfter(r, false), isCasedRune(unicode.ToLower(r)); got != want {
			t.Fatalf("isCasedAfter(%U, lower) = %v, want %v", r, got, want)
		}
		if got, want := isCasedAfter(r, true), isCasedRune(unicode.ToTitle(r)); got != want {
			t.Fatalf("isCasedAfter(%U, title) = %v, want %v", r, got, want)
		}
	}
	ref := func(s string, capitalize bool) string {
		var b strings.Builder
		prevCased := false
		for i, r := range s {
			if capitalize {
				if i == 0 {
					r = unicode.ToTitle(r)
				} else {
					r = unicode.ToLower(r)
				}
			} else {
				if prevCased {
					r = unicode.ToLower(r)
				} else {
					r = unicode.ToTitle(r)
				}
				prevCased = isCasedRune(r)
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	check := func(s string) {
		t.Helper()
		for _, capitalize := range []bool{false, true} {
			want := ref(s, capitalize)
			n := titleCaseLen(s, capitalize)
			if n != len(want) {
				t.Fatalf("titleCaseLen(%q, %v) = %d, want %d", s, capitalize, n, len(want))
			}
			if got := titleCase(s, n, capitalize); got != want {
				t.Fatalf("titleCase(%q, %v) = %q, want %q", s, capitalize, got, want)
			}
		}
	}
	var all strings.Builder
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r < 0xE000 {
			continue
		}
		all.WriteRune(r)
		if r%32 == 31 {
			check(all.String())
			all.Reset()
		}
	}
	pieces := []string{"a", "Z", "1", " ", "é", "É", "ǆ", "ǅ", "ȿ", "ⱥ", "ß", "İ", "\xff", "\xc3", "\xe2\x82", "-", "ΣΑΣ", "ﬃ", "\U00010400"}
	seed := uint32(12345)
	for k := 0; k < 3000; k++ {
		var b strings.Builder
		for j := 0; j < 1+int(seed>>28); j++ {
			seed = seed*1664525 + 1013904223
			b.WriteString(pieces[int(seed>>8)%len(pieces)])
		}
		check(b.String())
	}
	check("")
}

func TestG2_CaseMapOfInvalidUTF8IsRefusedBeforeAllocating(t *testing.T) {
	bad := StringDict{"bad": String(strings.Repeat("\xff", 200000))} // 600000 bytes mapped
	for _, op := range []string{"bad.upper()", "bad.lower()", "bad.title()", "bad.capitalize()"} {
		const budget = 400000
		r := g2Exec(t, budget, 0, "mark()\nr = "+op+"\n", bad)
		var be *AllocBudgetError
		if !errors.As(r.err, &be) {
			t.Errorf("%s: error = %v, want an *AllocBudgetError (the result is 600000 bytes)", op, r.err)
			continue
		}
		if spent := r.memEnd - r.memMark; spent > 100<<10 {
			t.Errorf("%s: allocated %d bytes before the refusal", op, spent)
		}
	}
}

func TestG2_CaseMapsKeepTheirResults(t *testing.T) {
	for _, c := range []struct{ s, upper, lower, title, capitalize string }{
		{"hello wORLD", "HELLO WORLD", "hello world", "Hello World", "Hello world"},
		{"\xffa\xfe", "�A�", "�a�", "�A�", "�a�"},
		{"привет Мир", "ПРИВЕТ МИР", "привет мир", "Привет Мир", "Привет мир"},
		{"ǆ", "Ǆ", "ǆ", "ǅ", "ǅ"},
		{"", "", "", "", ""},
	} {
		for name, want := range map[string]string{"upper": c.upper, "lower": c.lower, "title": c.title, "capitalize": c.capitalize} {
			r := g2Exec(t, 0, 0, "r = s."+name+"()\n", StringDict{"s": String(c.s)})
			if r.err != nil {
				t.Errorf("%q.%s(): %v", c.s, name, r.err)
				continue
			}
			if got := string(r.globals["r"].(String)); got != want {
				t.Errorf("%q.%s() = %q, want %q", c.s, name, got, want)
			}
		}
	}
}

// ---- SF6: the cutset of strip is charged ----

func TestG2_StripCutsetIsCharged(t *testing.T) {
	for _, c := range []struct {
		name, op string
		want     uint64 // bytes charged by the operation
	}{
		// the table of the characters that are not ASCII: a base of 48 and
		// 48 for each of them; the result is a slice of the operand (strip
		// is accounted: Call does not charge it)
		{"3 runes", "s.strip('αβγ')", 48 + 3*48},
		{"1 rune", "s.strip('α')", 48 + 1*48},
		{"lstrip", "s.lstrip('αβγ')", 48 + 3*48},
		{"rstrip", "s.rstrip('αβγ')", 48 + 3*48},
		{"ascii cutset", "'xyz'.strip('x')", 0},
		{"mixed", "s.strip('αx')", 48 + 1*48},
		{"no cutset", "s.strip()", 0},
	} {
		r := g2Exec(t, 0, 0, "mark()\nr = "+c.op+"\nmark()\n", StringDict{"s": String("αβγxyzαβγ")})
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.delta(); got != c.want {
			t.Errorf("%s: charged %d bytes, want %d", c.name, got, c.want)
		}
	}
}

func TestG2_StripCutsetIsRefusedBeforeItIsBuilt(t *testing.T) {
	// 20000 distinct runes: ~1 MB of map, refused by a budget of 64 KiB
	var cut strings.Builder
	for i := 0; i < 20000; i++ {
		cut.WriteRune(rune(0x4e00 + i))
	}
	for _, op := range []string{"s.strip(cut)", "s.lstrip(cut)", "s.rstrip(cut)"} {
		r := g2Exec(t, 64<<10, 0, "mark()\nr = "+op+"\n", StringDict{"s": String("abc"), "cut": String(cut.String())})
		var be *AllocBudgetError
		if !errors.As(r.err, &be) {
			t.Errorf("%s: error = %v, want an *AllocBudgetError", op, r.err)
			continue
		}
		if r.th.AllocatedBytes() > 64<<10 {
			t.Errorf("%s: charged %d, over the budget", op, r.th.AllocatedBytes())
		}
		if spent := r.memEnd - r.memMark; spent > 32<<10 {
			t.Errorf("%s: allocated %d bytes before the refusal", op, spent)
		}
	}
}

func TestG2_StripStillStrips(t *testing.T) {
	for _, c := range []struct{ s, chars, strip, lstrip, rstrip string }{
		{"  ab  ", "", "ab", "ab  ", "  ab"},
		{"xxabxx", "x", "ab", "abxx", "xxab"},
		{"αβabβα", "αβ", "ab", "abβα", "αβab"},
		{"αxaxα", "αx", "a", "axα", "αxa"},
		{"\xff\xfeab", "�", "ab", "ab", "\xff\xfeab"},
	} {
		for name, want := range map[string]string{"strip": c.strip, "lstrip": c.lstrip, "rstrip": c.rstrip} {
			src := "r = s." + name + "(cut)\n"
			if c.chars == "" {
				src = "r = s." + name + "()\n"
			}
			r := g2Exec(t, 0, 0, src, StringDict{"s": String(c.s), "cut": String(c.chars)})
			if r.err != nil {
				t.Errorf("%q.%s(%q): %v", c.s, name, c.chars, r.err)
				continue
			}
			if got := string(r.globals["r"].(String)); got != want {
				t.Errorf("%q.%s(%q) = %q, want %q", c.s, name, c.chars, got, want)
			}
		}
	}
}
