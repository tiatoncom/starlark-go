package starlark

// Tests of group G1 (time), part A: the stale work error after Uncancel (B4),
// the size of the text of an error (B7), the hash of a big int (SF11), the
// depth of the form of a value (SF12) and the conversion of an int to a string
// (SF10). Exact, deterministic: no time is measured.

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

var g1aOpts = &syntax.FileOptions{While: true, Set: true, TopLevelControl: true, GlobalReassign: true, Recursion: true}

func g1aExec(t *testing.T, th *Thread, src string) error {
	t.Helper()
	_, err := ExecFileOptions(g1aOpts, th, "g1a.star", src, nil)
	return err
}

// B4: after a refusal by the work limit, Uncancel and a new limit make the
// thread run again, and the new limit is a limit: a loop of opcodes only is
// refused exactly at it (the stale work error used to keep the gate shut: the
// loop ran unbounded).
func TestG1aStaleWorkErrorAfterUncancel(t *testing.T) {
	const l1, l2 = 10_000, 50_000
	// ("for ... in range" would end by itself: the bound is only here so that
	// a thread without a gate ends too)
	const loop = "for i in range(3000000):\n    pass\n"
	for _, order := range []string{"uncancel-then-set", "set-then-uncancel"} {
		t.Run(order, func(t *testing.T) {
			th := &Thread{}
			th.SetMaxWork(l1)
			var we *WorkBudgetError
			if err := g1aExec(t, th, loop); !errors.As(err, &we) || we.Limit != l1 {
				t.Fatalf("first run: err %v, want *WorkBudgetError at %d", err, l1)
			}
			if th.Work() != l1 {
				t.Fatalf("work after the refusal %d, want %d", th.Work(), l1)
			}
			if order == "uncancel-then-set" {
				th.Uncancel()
				th.SetMaxWork(l2)
			} else {
				th.SetMaxWork(l2)
				th.Uncancel()
			}
			we = nil
			err := g1aExec(t, th, loop)
			if !errors.As(err, &we) || we.Limit != l2 {
				t.Fatalf("second run: err %v, want *WorkBudgetError at %d", err, l2)
			}
			if th.Work() != l2 {
				t.Errorf("work after the second refusal %d, want exactly %d", th.Work(), l2)
			}
		})
	}
}

// B7: every error text of an operator that embeds an operand is bounded, whatever
// the operand: an int of 8 million bits, a string of 50 MB. The bound is
// errValueLimit (256) bytes of the operand, plus the fixed words of the text
// and the mark of the cut ("...<N bytes>": at most 24); the fixed part is
// under g1aErrFixed bytes.
const g1aErrFixed = 160

func g1aCheckErr(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if n := len(err.Error()); n > errValueLimit+g1aErrFixed {
		head := err.Error()
		if len(head) > 80 {
			head = head[:80]
		}
		t.Errorf("%s: the text of the error is %d bytes (> %d): %q...", what, n, errValueLimit+g1aErrFixed, head)
	}
}

func TestG1aErrorTextsAreBounded(t *testing.T) {
	huge := MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 8_000_000))
	hugeNeg := MakeBigInt(new(big.Int).Neg(huge.bigInt()))
	bigStr := String(strings.Repeat("x", 50<<20))
	bigBytes := Bytes(strings.Repeat("y", 50<<20))
	list := NewList([]Value{String("a"), MakeInt(1)})
	tup := Tuple{String("a"), MakeInt(1)}
	operands := []struct {
		name string
		v    Value
	}{
		{"huge", huge}, {"-huge", hugeNeg}, {"str50M", bigStr}, {"bytes50M", bigBytes},
		{"list", list}, {"tuple", tup}, {"small", MakeInt(3)}, {"zero", MakeInt(0)},
		{"float", Float(1.5)}, {"None", None},
	}

	// The operators of eval.go: Binary (arithmetic, repeat of str, bytes, list
	// and tuple, shifts, comparison, in, %-format, |, &, ^, +) and Unary.
	binary := []syntax.Token{
		syntax.PLUS, syntax.MINUS, syntax.STAR, syntax.SLASH, syntax.SLASHSLASH,
		syntax.PERCENT, syntax.AMP, syntax.PIPE, syntax.CIRCUMFLEX,
		syntax.LTLT, syntax.GTGT, syntax.IN, syntax.NOT_IN,
		syntax.EQL, syntax.NEQ, syntax.LT, syntax.LE, syntax.GT, syntax.GE,
	}
	unary := []syntax.Token{syntax.PLUS, syntax.MINUS, syntax.TILDE, syntax.NOT}

	// (the thread is limited so that the results that would succeed -- a
	// string of 100 MB -- are refused instead of made)
	th := &Thread{}
	th.SetMaxAllocBytes(1 << 20)

	n := 0
	for _, op := range binary {
		for _, x := range operands {
			for _, y := range operands {
				_, err := binaryOp(th, op, x.v, y.v)
				n++
				g1aCheckErr(t, fmt.Sprintf("%s %s %s", x.name, op, y.name), err)
			}
		}
	}
	for _, op := range unary {
		for _, x := range operands {
			_, err := Unary(op, x.v)
			n++
			g1aCheckErr(t, fmt.Sprintf("%s %s", op, x.name), err)
		}
	}

	// The repeats, called directly: the count is the huge int, and the
	// operand the large one.
	for _, c := range []struct {
		name string
		fn   func() error
	}{
		{"stringRepeat", func() error { _, err := stringRepeat(th, bigStr, huge); return err }},
		{"stringRepeat -huge", func() error { _, err := stringRepeat(th, bigStr, hugeNeg); return err }},
		{"bytesRepeat", func() error { _, err := bytesRepeat(th, bigBytes, huge); return err }},
		{"tupleRepeat", func() error { _, err := tupleRepeat(th, tup, huge, false); return err }},
		{"listRepeat", func() error { _, err := tupleRepeat(th, Tuple(list.elems), huge, true); return err }},
	} {
		err := c.fn()
		if err == nil {
			t.Errorf("%s: no error with a count of 8M bits", c.name)
		}
		g1aCheckErr(t, c.name, err)
		n++
	}

	// The operators in a program: the interpreter paths (index, slice, the
	// format conversions, the calls) with the same operands.
	for _, src := range []string{
		"x = [1, 2]\nx[H]",
		"x = [1, 2]\nx[H] = 1",
		"x = [1, 2]\nx[H:]",
		"x = [1, 2]\nx[:H]",
		"x = [1, 2]\nx[::H]",
		"x = 'abc'\nx[H]",
		"x = 'abc'\nx[H:]",
		"x = {'a': 1}\nx[H]",
		"x = {'a': 1}\nx[S]",
		"x = [1] * H",
		"x = H * [1]",
		"x = (1,) * H",
		"x = 'a' * H",
		"x = b'a' * H",
		"x = H * 'a'",
		"x = 1 << H",
		"x = 1 >> H",
		"x = H << H",
		"x = 'a' + H",
		"x = H + 'a'",
		"x = 'a' % H",
		"x = '%c' % H",
		"x = '%s %s' % H",
		"x = '%(k)s' % {'j': S}",
		"x = '%(k' % {}",
		"x = '%d' % S",
		"x = '%d' % B",
		"x = '%d' % H",
		"x = '%x' % H",
		"x = '%c' % S",
		"x = '%f' % S",
		"x = '%z' % H",
		"x = S.nosuch",
		"x = H.nosuch",
		"S()",
		"H()",
		"def f(a): pass\nf(**{S: 1})",
		"def f(a): pass\nf(a=1, **{'a': S})",
		"def f(a): pass\nf(H)",
		"def f(a): pass\nf(zz=H)",
		"x = H // 0",
		"x = H % 0",
		"x = H / 0",
		"x = 1 // H.bit_length()",
		"x = S < H",
		"x = H in S",
		"x = S in H",
		"x = -S",
		"x = ~S",
		"x = -B",
		"x = [H][S]",
		"x = {H: 1}[S]",
		"x = {[]: 1}",
		"x = {S: 1}[B]",
		"x = len(H)",
		"x = int(S)",
		"x = int(S, 7)",
		"x = float(S)",
		"x = chr(H)",
		"x = range(H)[S]",
	} {
		th := &Thread{}
		th.SetMaxAllocBytes(1 << 20)
		predeclared := StringDict{"H": huge, "S": bigStr, "B": bigBytes}
		_, err := ExecFileOptions(g1aOpts, th, "g1a.star", src, predeclared)
		n++
		g1aCheckErr(t, strings.ReplaceAll(src, "\n", "; "), err)
	}
	t.Logf("%d error paths checked", n)
}

// SF11: the hash of a big int takes part with all its words, and is not
// invertible by hand: the family of ints that collided under the old hash
// (h = lo ^ hi*0x85EBCA6B for one word, the halves of the word being lo and hi)
// has distinct hashes now.
func TestG1aBigIntHashDoesNotCollideOnTheOldFamily(t *testing.T) {
	const k2 = 0x85EBCA6B
	old := func(w uint64) uint32 { return uint32(w) ^ uint32(w>>32)*k2 }
	// words w = hi<<32 | lo of a big int (>= 2^63), all with the same old hash
	target := old(0x8000_0000_0000_1234)
	var fam []uint64
	for k := uint32(0); k < 2000; k++ {
		hi := 0x8000_0000 | k
		lo := target ^ hi*k2
		fam = append(fam, uint64(hi)<<32|uint64(lo))
	}
	for _, w := range fam {
		if old(w) != target {
			t.Fatalf("test family is wrong: %#x", w)
		}
	}
	// the same family two words long: the old hash of a number of two words
	// is h = old(w0)*K1 ^ old(w1)
	check := func(name string, ints []Int) {
		seen := map[uint32]int{}
		for _, x := range ints {
			h, err := x.Hash()
			if err != nil {
				t.Fatal(err)
			}
			seen[h]++
		}
		if len(seen) != len(ints) {
			t.Errorf("%s: %d ints have %d distinct hashes", name, len(ints), len(seen))
		}
	}
	var one, two []Int
	for _, w := range fam {
		one = append(one, MakeBigInt(new(big.Int).SetUint64(w)))
		two = append(two, MakeBigInt(new(big.Int).Or(new(big.Int).Lsh(new(big.Int).SetUint64(w), 64), big.NewInt(7))))
	}
	check("one word", one)
	check("two words", two)
	// the words above the first count: ints that differ only in the high word
	var hiw []Int
	for k := uint64(1); k <= 2000; k++ {
		hiw = append(hiw, MakeBigInt(new(big.Int).Or(new(big.Int).Lsh(new(big.Int).SetUint64(k), 128), big.NewInt(5))))
	}
	check("high word", hiw)
	// equal ints have the same hash
	a := MakeBigInt(new(big.Int).Lsh(big.NewInt(3), 100))
	b := MakeBigInt(new(big.Int).Lsh(big.NewInt(3), 100))
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Errorf("equal ints, hashes %d and %d", ha, hb)
	}
}

// g1aNested returns a list nested d levels deep (d lists), made in Go.
func g1aNested(d int) Value {
	var x Value = NewList(nil)
	for i := 1; i < d; i++ {
		x = NewList([]Value{x})
	}
	return x
}

func g1aStrWork(t *testing.T, x Value) uint64 {
	t.Helper()
	th := &Thread{}
	if _, err := Call(th, Universe["str"], Tuple{x}, nil); err != nil {
		t.Fatal(err)
	}
	return th.Work()
}

// SF12: the form of a value is charged a constant for each level of nesting
// beyond pathSetDepth (the stack, the cache and the set of the path cost more
// than the element): the work of str() of a list nested d levels is, from the
// 34th list, the old work + (d-33) * deepFormWork, exactly.
func TestG1aDeepFormIsChargedPerLevelBeyond32(t *testing.T) {
	// The work of str() of a list of d lists made in Go (no steps) before the
	// price of the deep levels existed: 4 a level (the element), 8 more for
	// each container from the 33rd (the set of the path), and the bytes of the
	// form at a unit for 64. These numbers are the old ones and do not move for
	// d <= 33 (TestMU_FormOfANestedListChargesFromDepth32 pins the same
	// prices with the steps of a loop: 365 and 577 for 20 and 32 levels).
	for _, c := range []struct {
		d   int
		old uint64
	}{{20, 80}, {21, 84}, {32, 128}, {33, 140}, {34, 152}, {40, 224}, {65, 525}, {100, 946}, {133, 1342}} {
		want := c.old
		if c.d > 33 {
			want += uint64(c.d-33) * g1aDeepFormWork // the levels beyond the 33rd list, exactly
		}
		if got := g1aStrWork(t, g1aNested(c.d)); got != want {
			t.Errorf("str of a list of %d levels: work %d, want %d (= %d + %d * %d)", c.d, got, want, c.old, max(c.d-33, 0), g1aDeepFormWork)
		}
	}
}

// g1aDeepFormWork (= deepFormWork of value.go, repeated so that a change of it
// is seen) is the price of a level beyond the 33rd list. 32 is derived from the measurement of
// str() of a list nested 9000 levels: 217-278 ns a level on darwin/arm64
// (against 27 at depth 30), and the reviewers' 2-3.5 times C on the slow
// machine, at the 12 units a level that were charged: 3.5 * 12 = 42 <= 12 + 32.
const g1aDeepFormWork = 32

// SF10: the conversion of an int to a decimal string is charged a unit for two
// digits besides the d*d/4096 (d = BitLen/3+1, the bound of the digits that the
// code uses): math/big Text(10) takes 135 ns for 20 digits, 457 for 100, 2269
// for 600 and 13155 for 4300 (darwin/arm64), i.e. 11, 38, 189 and 1096 units of
// 12 ns, against 0, 3, 107 and 5536 charged: d/2 covers the small and mid sizes.
//
// One price for every path (intToStringWork): the table charges each path of
// the text of an int (steps excluded, as isRun measures) and checks that the
// work is the conversion, intToStringWork(x), plus what that path charges
// besides it: the call of the builtin, and a unit for each 96 bytes of the
// result string for the paths that charge it (not repr: its string is charged
// no work, a gap that is not the conversion's).
func TestG1aIntToStringIsOneFunctionForEveryPath(t *testing.T) {
	type path struct {
		op       string
		decimal  bool
		overhead func(n int) uint64 // n: the bytes of the result
	}
	str96 := func(n int) uint64 { return uint64(n) / 96 }
	paths := []path{
		{"r = str(x)", true, func(n int) uint64 { return 4 + str96(n) }},
		{"r = repr(x)", true, func(n int) uint64 { return 4 + str96(n) }}, // the same as str: one rule for the result string
		{"r = '%s' % x", true, str96},
		{"r = '%r' % x", true, str96},
		{"r = '%d' % x", true, str96},
		{"r = '%i' % x", true, str96},
		{"r = '{}'.format(x)", true, func(n int) uint64 { return 10 + str96(n) }},
		{"r = '%x' % x", false, str96},
		{"r = '%X' % x", false, str96},
		{"r = '%o' % x", false, str96},
	}
	for _, digits := range []int{20, 100, 600, 4300} {
		x := MakeBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil))
		// the function itself, as chargeIntToString (the % operator) charges it
		d := uint64(x.bigInt().BitLen()/3 + 1)
		if got, want := intToStringWork(x.bigInt(), true), d/2+d*d/4096; got != want {
			t.Fatalf("%d digits: intToStringWork %d, want d/2 + d*d/4096 = %d", digits, got, want)
		}
		for _, p := range paths {
			m := isRun(t, StringDict{"x": x}, "", p.op)
			n := int(m.alloc) // the bytes of the result: the string is the only allocation
			want := intToStringWork(x.bigInt(), p.decimal) + p.overhead(n)
			if m.work != want {
				t.Errorf("%d digits, %s: work %d, want %d = conversion %d + %d", digits, p.op, m.work, want, intToStringWork(x.bigInt(), p.decimal), p.overhead(n))
			}
		}
	}
}
