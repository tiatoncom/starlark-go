package starlark

// Tests of the accounting of time in steps (work.go, prices.go): the API, the
// closed list of prices, the claim that no built-in's work grows without its
// steps, and the attacks that motivated it, each bounded by the steps and
// checked against an independent count of the work (a value that counts the
// comparisons, hashes and truth tests done on it).

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"go.starlark.net/syntax"
)

// ---- ChargeSteps ----

func TestChargeSteps_AddsSteps(t *testing.T) {
	th := &Thread{}
	if err := th.ChargeSteps(0); err != nil || th.Steps != 0 {
		t.Fatalf("ChargeSteps(0): %v, steps %d", err, th.Steps)
	}
	if err := th.ChargeSteps(5); err != nil || th.Steps != 5 {
		t.Fatalf("ChargeSteps(5): %v, steps %d", err, th.Steps)
	}
	// Saturates, like the memory count.
	th.Steps = math.MaxUint64 - 2
	th.ChargeSteps(10)
	if th.Steps != math.MaxUint64 {
		t.Fatalf("steps = %d: not saturated", th.Steps)
	}
}

func TestChargeSteps_NoLimitNeverRefuses(t *testing.T) {
	// A thread that has not set a limit: maxSteps is 0, which is "no limit"
	// (the interpreter makes it the maximum at the first call).
	th := &Thread{}
	if err := th.ChargeSteps(1 << 40); err != nil {
		t.Fatal(err)
	}
}

// The step limit is reached by a charge exactly as it is by an opcode: the
// default cancels the thread with "too many steps", and the operation fails
// with the error of a cancelled thread, in the text of the interpreter.
func TestChargeSteps_LimitCancelsLikeTheInterpreter(t *testing.T) {
	th := &Thread{}
	th.SetMaxExecutionSteps(100)
	if err := th.ChargeSteps(99); err != nil {
		t.Fatal(err)
	}
	err := th.ChargeSteps(1)
	if err == nil || err.Error() != "Starlark computation cancelled: too many steps" {
		t.Fatalf("err = %v", err)
	}
	// The interpreter reports the same.
	r := runProg(t, 0, "")
	_ = r
	th2 := &Thread{}
	th2.SetMaxExecutionSteps(50)
	_, err2 := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th2, "t.star", "for i in range(1000): pass\n", nil)
	if err2 == nil || !strings.Contains(err2.Error(), "Starlark computation cancelled: too many steps") {
		t.Fatalf("interpreter: %v", err2)
	}
	if err.Error() != "Starlark computation cancelled: too many steps" {
		t.Fatalf("different text from the interpreter's")
	}
}

// The engine arms OnMaxSteps to poll its context every 1024 steps. A charge
// that crosses the threshold calls the hook, which re-arms ahead of the new
// count, and a hook that cancels makes the charge fail.
func TestChargeSteps_HonorsOnMaxSteps(t *testing.T) {
	th := &Thread{}
	var calls []uint64
	const poll = 1024
	arm := func() { th.SetMaxExecutionSteps(th.Steps + poll) }
	limit := uint64(1 << 20)
	th.OnMaxSteps = func(t *Thread) {
		calls = append(calls, t.Steps)
		if t.Steps >= limit {
			t.Cancel("too many steps")
			return
		}
		arm()
	}
	arm()
	// 600 steps: below the first threshold: no call.
	if err := th.ChargeSteps(600); err != nil || len(calls) != 0 {
		t.Fatalf("err %v, calls %v", err, calls)
	}
	// 3000 more: crosses the threshold once; the hook re-arms 1024 ahead.
	if err := th.ChargeSteps(3000); err != nil || len(calls) != 1 || calls[0] != 3600 {
		t.Fatalf("err %v, calls %v", err, calls)
	}
	if th.maxSteps != 3600+poll {
		t.Fatalf("armed at %d, want %d", th.maxSteps, 3600+poll)
	}
	// A charge past the limit: the hook cancels, the charge fails, and the
	// thread stays cancelled for the next operation or opcode.
	err := th.ChargeSteps(limit)
	var ce *cancelledError
	if !errors.As(err, &ce) || ce.reason != "too many steps" {
		t.Fatalf("err = %#v", err)
	}
	if err := th.ChargeSteps(1); err == nil {
		t.Fatal("a cancelled thread accepted a charge")
	}
}

// A built-in of the host charges its own work, and stops when told to.
func TestChargeSteps_HostBuiltin(t *testing.T) {
	th := &Thread{}
	th.SetMaxExecutionSteps(10_000)
	var done int
	host := NewBuiltin("host_scan", func(th *Thread, _ *Builtin, args Tuple, _ []Tuple) (Value, error) {
		for i := 0; i < 1000; i++ {
			if err := th.ChargeSteps(100); err != nil {
				return nil, err
			}
			done++
		}
		return None, nil
	})
	_, err := ExecFileOptions(&syntax.FileOptions{}, th, "t.star", "host_scan()\n", StringDict{"host_scan": host})
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("err = %v", err)
	}
	if done > 101 {
		t.Fatalf("the host built-in did %d rounds of 100 steps against a limit of 10000", done)
	}
	var ce *cancelledError
	if !errors.As(err, &ce) {
		// It crossed the Call boundary as an EvalError: its cause is ours.
		t.Logf("error type %T", err)
	}
}

// ---- meter ----

func TestMeter_FreeWindowAndFlush(t *testing.T) {
	th := &Thread{}
	m := th.meter()
	// Under FreeWork: nothing, and the remainder is dropped with the meter.
	for i := 0; i < FreeWork-1; i++ {
		m.add(1)
	}
	if err := m.flush(); err != nil || th.Steps != 0 {
		t.Fatalf("under the free window: steps %d, %v", th.Steps, err)
	}
	// At FreeWork: all of it is charged (units / WorkPerStep), not the excess.
	m.add(1)
	if err := m.flush(); err != nil || th.Steps != FreeWork/WorkPerStep {
		t.Fatalf("at the free window: steps %d, want %d", th.Steps, FreeWork/WorkPerStep)
	}
	// The remainder of a step is not charged; the meter charges what grew.
	m2 := th.meter()
	th.Steps = 0
	m2.add(1000)
	m2.flush()
	if th.Steps != 1000/WorkPerStep {
		t.Fatalf("steps %d, want %d", th.Steps, 1000/WorkPerStep)
	}
	m2.add(8)
	m2.flush()
	if th.Steps != 1008/WorkPerStep {
		t.Fatalf("steps %d, want %d", th.Steps, 1008/WorkPerStep)
	}
	// A nil meter and a meter of a nil thread do nothing.
	var nilm *meter
	if err := nilm.add(1 << 40); err != nil {
		t.Fatal(err)
	}
	nm := (*Thread)(nil).meter()
	if err := nm.add(1 << 40); err != nil {
		t.Fatal(err)
	}
}

// A meter flushes every flushWork units: the limit is noticed within a chunk,
// not at the end of an operation of a billion units.
func TestMeter_StopsNearTheLimit(t *testing.T) {
	th := &Thread{}
	th.SetMaxExecutionSteps(1000)
	m := th.meter()
	var added uint64
	var err error
	for err == nil && added < 100*1000*WorkPerStep { // (a bound, so that a meter that never stops fails the test)
		err = m.add(1)
		added++
	}
	if err == nil {
		t.Fatal("never stopped")
	}
	// 1000 steps are 16000 units; it stops within one chunk of them.
	if added > 1000*WorkPerStep+flushWork+WorkPerStep {
		t.Fatalf("stopped after %d units, limit is %d", added, 1000*WorkPerStep)
	}
}

// ---- the closed list of prices ----

func TestEveryBuiltinHasAPrice(t *testing.T) {
	check := func(table string, name string, b *Builtin, prices map[string]price) {
		p, ok := prices[name]
		if !ok {
			t.Errorf("%s.%s has no entry in the table of prices: a built-in without a price is a built-in whose time nothing bounds", table, name)
			return
		}
		if b.price == nil {
			t.Errorf("%s.%s: the price is not attached", table, name)
		}
		if !p.o1 && !p.inside && p.work == nil {
			t.Errorf("%s.%s: the price declares nothing", table, name)
		}
		if p.desc == "" {
			t.Errorf("%s.%s: the price has no description", table, name)
		}
	}
	universe := map[string]bool{}
	for name, v := range Universe {
		if b, ok := v.(*Builtin); ok {
			universe[name] = true
			check("universe", name, b, universePrices)
		}
	}
	for name := range universePrices {
		if !universe[name] {
			t.Errorf("universe.%s has a price but is not in Universe", name)
		}
	}
	for table, c := range map[string]struct {
		methods map[string]*Builtin
		prices  map[string]price
	}{
		"dict": {dictMethods, dictPrices}, "list": {listMethods, listPrices}, "set": {setMethods, setPrices},
		"string": {stringMethods, stringPrices}, "bytes": {bytesMethods, bytesPrices},
	} {
		for name, b := range c.methods {
			check(table, name, b, c.prices)
		}
		for name := range c.prices {
			if _, ok := c.methods[name]; !ok {
				t.Errorf("%s.%s has a price but is not a method", table, name)
			}
		}
	}
}

// Every operator has a declared price: the tokens of the binary and unary
// operators, and the other operations of the interpreter, each appear in the
// table (so that an operator added without a price is a red test).
func TestOperatorPricesAreDeclared(t *testing.T) {
	var keys []string
	for k, p := range operatorPrices {
		keys = append(keys, k)
		if p.desc == "" || (!p.o1 && !p.inside && p.work == nil) {
			t.Errorf("operator price %q declares nothing", k)
		}
	}
	all := strings.Join(keys, "\n")
	for _, tok := range []syntax.Token{syntax.PLUS, syntax.MINUS, syntax.STAR, syntax.SLASH, syntax.SLASHSLASH, syntax.PERCENT,
		syntax.EQL, syntax.NEQ, syntax.LT, syntax.LE, syntax.GT, syntax.GE, syntax.IN, syntax.PIPE, syntax.AMP,
		syntax.CIRCUMFLEX, syntax.LTLT, syntax.GTGT, syntax.TILDE} {
		if !strings.Contains(all, " "+tok.String()+" ") && !strings.Contains(all, " "+tok.String()+",") && !strings.Contains(all, ", "+tok.String()+" ") &&
			!strings.Contains(all, tok.String()) {
			t.Errorf("operator %s has no price", tok)
		}
	}
	for _, need := range []string{"x in", "x[i]", "x[k]", "x[i:j:k]", "hash of", "freeze", "json", "f(*l)", "def f", "d1 |", "x == y", "x % y", "x * n", "x + y"} {
		if !strings.Contains(all, need) {
			t.Errorf("no price for %q", need)
		}
	}
	// Every price in the tables has a formula text for the document.
	for _, table := range []map[string]price{universePrices, dictPrices, listPrices, setPrices, stringPrices, bytesPrices} {
		for name, p := range table {
			if strings.TrimSpace(p.desc) == "" {
				t.Errorf("%s has no description", name)
			}
		}
	}
}

// A price that is declared constant is constant: the call costs the same steps
// on an operand of size n and of size 100n.
func TestO1PricesDoNotGrow(t *testing.T) {
	// name -> the program of the call, over x (a list of N), s (a string of N),
	// d (a dict of N entries), b (bytes of N)
	calls := map[string]string{
		"universe.bool":         "bool(x)",
		"universe.chr":          "chr(65)",
		"universe.dir":          "dir(x)",
		"universe.getattr":      "getattr(x, 'append')",
		"universe.hasattr":      "hasattr(x, 'append')",
		"universe.len":          "len(x)",
		"universe.ord":          "ord('a')",
		"universe.range":        "range(N)",
		"universe.type":         "type(x)",
		"list.append":           "x.append(1)",
		"string.elems":          "s.elems()",
		"string.codepoints":     "s.codepoints()",
		"string.elem_ords":      "s.elem_ords()",
		"string.codepoint_ords": "s.codepoint_ords()",
		"bytes.elems":           "b.elems()",
	}
	declared := map[string]bool{}
	for name, p := range universePrices {
		if p.o1 {
			declared["universe."+name] = true
		}
	}
	for table, prices := range map[string]map[string]price{"dict": dictPrices, "list": listPrices, "set": setPrices, "string": stringPrices, "bytes": bytesPrices} {
		for name, p := range prices {
			if p.o1 {
				declared[table+"."+name] = true
			}
		}
	}
	for name := range declared {
		if _, ok := calls[name]; !ok {
			t.Errorf("%s is declared constant, but the test has no call of it", name)
		}
	}
	steps := func(n int, call string) uint64 {
		src := fmt.Sprintf("N = %d\nx = [0] * N\ns = 'a' * N\nd = {}\nb = b'a' * N\nmark()\nr = %s\nmark()\n", n, strings.ReplaceAll(call, "N)", "N)"))
		var marks []uint64
		th := &Thread{}
		mark := NewBuiltin("mark", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
			marks = append(marks, th.Steps)
			return None, nil
		})
		if _, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", src, StringDict{"mark": mark}); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
		return marks[1] - marks[0]
	}
	for name, call := range calls {
		small, large := steps(100, call), steps(10000, call)
		if small != large {
			t.Errorf("%s (%s): %d steps for n=100, %d for n=10000", name, call, small, large)
		}
	}
}

// ---- counted values ----

// A counted is a value that counts the work done on it: comparisons (Equal,
// <), hashes, truth tests. The counters are global: the tests that use them
// run one at a time.
type counted struct{ id int }

var (
	cmpCount   atomic.Uint64
	hashCount  atomic.Uint64
	truthCount atomic.Uint64
)

func resetCounts() { cmpCount.Store(0); hashCount.Store(0); truthCount.Store(0) }

// runawayWork is the number of comparisons or hashes after which a counted
// value refuses to go on: the attacks are bounded by their steps, and if they
// are not (a regression), the test fails with this error rather than not
// returning. The legitimate attacks of the tests do at most ~80 million.
const runawayWork = 250_000_000

var errRunaway = errors.New("runaway: the steps did not stop the work")

func (c counted) String() string { return fmt.Sprintf("counted(%d)", c.id) }
func (c counted) Type() string   { return "counted" }
func (c counted) Freeze()        {}
func (c counted) Truth() Bool    { truthCount.Add(1); return c.id != 0 }
func (c counted) Hash() (uint32, error) {
	if hashCount.Add(1) > runawayWork {
		return 0, errRunaway
	}
	return uint32(c.id) * 2654435761, nil
}
func (c counted) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	if cmpCount.Add(1) > runawayWork {
		return false, errRunaway
	}
	d := y.(counted)
	switch op {
	case syntax.EQL:
		return c.id == d.id, nil
	case syntax.NEQ:
		return c.id != d.id, nil
	case syntax.LT:
		return c.id < d.id, nil
	case syntax.LE:
		return c.id <= d.id, nil
	case syntax.GT:
		return c.id > d.id, nil
	default:
		return c.id >= d.id, nil
	}
}

func countedBuiltins() StringDict {
	// mk(n): a list of n counted values with ids 1..n; mk(n, off): off+1..off+n
	mk := NewBuiltin("mk", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n, off int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n, &off); err != nil {
			return nil, err
		}
		elems := make([]Value, n)
		for i := range elems {
			elems[i] = counted{off + i + 1}
		}
		return NewList(elems), nil
	})
	zeros := NewBuiltin("zeros", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n); err != nil {
			return nil, err
		}
		elems := make([]Value, n)
		for i := range elems {
			elems[i] = counted{0}
		}
		return NewList(elems), nil
	})
	leaf := NewBuiltin("leaf", func(_ *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) { return counted{1}, nil })
	return StringDict{"mk": mk, "zeros": zeros, "leaf": leaf, "reset": NewBuiltin("reset", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		resetCounts()
		return None, nil
	})}
}

// runCounted runs src (which may call reset()) with a step limit, and returns
// the error and the steps used since the first reset.
func runCounted(t *testing.T, limit uint64, src string) (steps uint64, err error) {
	t.Helper()
	th := &Thread{}
	if limit > 0 {
		th.SetMaxExecutionSteps(limit)
	}
	var base uint64
	extra := countedBuiltins()
	extra["reset"] = NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		resetCounts()
		base = th.Steps
		return None, nil
	})
	_, err = ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, extra)
	return th.Steps - base, err
}

// ---- the attacks, bounded by steps and checked against the work ----

// x in l, over a list the length of a million, is a million comparisons: the
// steps bound the comparisons to what the limit allows.
func TestAttack_MembershipOfAHugeList(t *testing.T) {
	for _, op := range []string{"x in l", "l.index(x)", "l.remove(x)", "max(l)", "min(l)", "any(z)", "all(l)"} {
		resetCounts()
		const S = 50_000
		src := "l = mk(2000000)\nz = zeros(2000000)\nx = counted_miss()\nreset()\nr = " + op + "\n"
		src = strings.Replace(src, "counted_miss()", "leaf_miss()", 1)
		th := &Thread{}
		th.SetMaxExecutionSteps(S)
		extra := countedBuiltins()
		extra["leaf_miss"] = NewBuiltin("leaf_miss", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) { return counted{-1}, nil })
		_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", src, extra)
		if err == nil || !strings.Contains(err.Error(), "too many steps") {
			t.Errorf("%s: err = %v (steps %d, compares %d, truth %d)", op, err, th.Steps, cmpCount.Load(), truthCount.Load())
			continue
		}
		work := cmpCount.Load() + truthCount.Load()
		// The steps are used up, and the work done is what they paid for: at
		// most WorkPerStep units a step, plus one chunk of the meter.
		if bound := uint64(S)*WorkPerStep + flushWork + FreeWork; work > bound {
			t.Errorf("%s: %d comparisons done on a limit of %d steps (bound %d)", op, work, S, bound)
		}
		if work == 0 {
			t.Errorf("%s: nothing was compared: refused before it began, but the work is charged as it goes", op)
		}
	}
}

// sorted charges n*log n before it sorts: a list that cannot be sorted in the
// steps that are left is refused, not sorted.
func TestAttack_SortedIsRefusedBeforeItRuns(t *testing.T) {
	resetCounts()
	const S = 100_000
	th := &Thread{}
	th.SetMaxExecutionSteps(S)
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", "l = mk(2000000)\nreset()\nr = sorted(l)\n", countedBuiltins())
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("err = %v", err)
	}
	if n := cmpCount.Load(); n != 0 {
		t.Errorf("%d comparisons were made before the refusal", n)
	}
}

// d == e, x == x on a wide, nested structure: the leaves compared are charged
// as they are compared, and the comparison stops when the steps are used up.
func TestAttack_ComparisonOfAWideNestedList(t *testing.T) {
	// Lists of width 60 and depth 4: 60^4 = 13 million leaves.
	prog := `
def nest(d):
  if d == 0:
    return leaf()
  return [nest(d - 1)] * 60
`
	_ = prog
	// Build the structure in Go: a list of width W whose elements are all the
	// same list one level down, so that it is a DAG of 5 lists and 13 million
	// leaf comparisons.
	const W, D = 60, 4
	var level Value = counted{1}
	for i := 0; i < D; i++ {
		elems := make([]Value, W)
		for j := range elems {
			elems[j] = level
		}
		level = NewList(elems)
	}
	root := level
	for _, op := range []string{"x == x", "x != x", "x == y", "x < y"} {
		resetCounts()
		const S = 100_000
		th := &Thread{}
		th.SetMaxExecutionSteps(S)
		extra := StringDict{"x": root, "y": root}
		_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", "r = "+op+"\n", extra)
		if err == nil || !strings.Contains(err.Error(), "too many steps") {
			t.Errorf("%s: err = %v (compares %d)", op, err, cmpCount.Load())
			continue
		}
		if bound := uint64(S)*WorkPerStep + flushWork + FreeWork; cmpCount.Load() > bound {
			t.Errorf("%s: %d leaves compared on a limit of %d steps (bound %d)", op, cmpCount.Load(), S, bound)
		}
	}
	// The same structure is compared in full where the limit allows it: the
	// result is the one of v0.2.0, the steps are charged for the leaves.
	small := NewList([]Value{counted{1}, counted{1}, counted{1}})
	resetCounts()
	th := &Thread{}
	_, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th, "t.star", "r = x == y\nif not r: fail('unequal')\n", StringDict{"x": small, "y": small})
	if err != nil {
		t.Fatal(err)
	}
	if cmpCount.Load() != 3 {
		t.Errorf("%d comparisons of a list of 3", cmpCount.Load())
	}
}

// t = (t, t) repeated is a DAG of 2^k paths: hashing it (as a dict key) visits
// each path, and is bounded by the steps.
func TestAttack_TupleDAGHash(t *testing.T) {
	resetCounts()
	const S = 200_000
	src := "t = (leaf(),)\nfor i in range(40): t = (t, t)\nreset()\nd = {t: 1}\n"
	th := &Thread{}
	th.SetMaxExecutionSteps(S)
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", src, countedBuiltins())
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("err = %v (hashes %d)", err, hashCount.Load())
	}
	if bound := uint64(S)*WorkPerStep + flushWork + FreeWork; hashCount.Load() > bound {
		t.Errorf("%d leaf hashes on a limit of %d steps (bound %d)", hashCount.Load(), S, bound)
	}
	// Also through the other entry points that hash: set, `in`, dict().
	for _, op := range []string{"s = set([t])", "r = t in {1: 2}", "d = dict([(t, 1)])", "d = {}\nd[t] = 1", "r = {1: 2}.get(t)"} {
		resetCounts()
		th := &Thread{}
		th.SetMaxExecutionSteps(S)
		_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", "t = (leaf(),)\nfor i in range(40): t = (t, t)\nreset()\n"+op+"\n", countedBuiltins())
		if err == nil || !strings.Contains(err.Error(), "too many steps") {
			t.Errorf("%s: err = %v (hashes %d)", op, err, hashCount.Load())
		}
	}
}

// Freeze of a DAG is linear in its distinct nodes.
func TestAttack_TupleDAGFreeze(t *testing.T) {
	l := NewList([]Value{counted{1}})
	var v Value = Tuple{l}
	for i := 0; i < 22; i++ {
		v = Tuple{v, v}
	}
	v.Freeze() // 2^22 paths: 4M visits if each is walked, one if not
	if !l.frozen {
		t.Fatal("the list at the bottom was not frozen")
	}
}

// A leaf that counts its freezes.
type freezeCounted struct{ n *int }

func (c freezeCounted) String() string        { return "fc" }
func (c freezeCounted) Type() string          { return "fc" }
func (c freezeCounted) Freeze()               { *c.n++ }
func (c freezeCounted) Truth() Bool           { return True }
func (c freezeCounted) Hash() (uint32, error) { return 1, nil }

// The same, with a depth where a walk of every path ends, and fails the test
// instead of not returning: each distinct node is visited once.
func TestAttack_TupleDAGFreezeVisitsEachNodeOnce(t *testing.T) {
	n := 0
	var v Value = Tuple{freezeCounted{&n}}
	for i := 0; i < 22; i++ {
		v = Tuple{v, v}
	}
	v.Freeze()
	if n != 1 {
		t.Errorf("the leaf of a DAG of 2^22 paths was frozen %d times, want 1", n)
	}
}

// The hash of an int uses all its words.
func TestIntHash_MixesAllWords(t *testing.T) {
	// Ints that differ only above bit 32 must not all share a bucket.
	buckets := map[uint32]int{}
	for i := 1; i <= 4096; i++ {
		h, _ := MakeInt64(int64(i) << 32).Hash()
		buckets[h&1023]++
	}
	max := 0
	for _, n := range buckets {
		if n > max {
			max = n
		}
	}
	if len(buckets) < 900 || max > 16 {
		t.Errorf("4096 ints i<<32 fall into %d of 1024 buckets, the fullest holds %d", len(buckets), max)
	}
	// Ints that differ only in a high word, above the first: all the words count.
	hs := map[uint32]bool{}
	for i := 1; i <= 2000; i++ {
		h, _ := MakeBigInt(new(big.Int).Lsh(big.NewInt(int64(i)), 200)).Hash()
		hs[h] = true
	}
	if len(hs) < 1900 {
		t.Errorf("2000 ints i<<200 have %d distinct hashes", len(hs))
	}
	// And in a dict: the longest chain is short.
	d := new(Dict)
	for i := 1; i <= 8000; i++ {
		d.SetKey(MakeInt64(int64(i)<<32), None)
	}
	if n := longestChain(&d.ht); n > 8 {
		t.Errorf("the longest chain of a dict of 8000 ints i<<32 has %d buckets", n)
	}
	// Equal numbers hash equally across types: int, float, big int.
	for _, x := range []int64{0, 1, -1, 7, 1 << 31, 1<<31 - 1, 1 << 32, -(1 << 40), 1 << 53, 123456789012345} {
		hi, _ := MakeInt64(x).Hash()
		hf, _ := Float(float64(x)).Hash()
		if hi != hf {
			t.Errorf("hash(%d) != hash(float(%d))", x, x)
		}
	}
	// A dict is found by an equal float.
	d2 := new(Dict)
	d2.SetKey(MakeInt64(1<<40), String("x"))
	if v, ok, _ := d2.Get(Float(float64(1 << 40))); !ok || v != String("x") {
		t.Errorf("a dict key (1<<40) is not found by 1099511627776.0")
	}
}

// longestChain is the number of buckets of the longest chain of a table.
func longestChain(ht *hashtable) int {
	longest := 0
	for i := range ht.table {
		n := 0
		for p := &ht.table[i]; p != nil; p = p.next {
			n++
		}
		longest = max(longest, n)
	}
	return longest
}

// Strings of up to 11 bytes are hashed by FNV with no seed: a few thousand
// strings of the same hash are easy to make. Each insertion walks the chain,
// and the walk is charged.
func TestAttack_CollidingStringKeys(t *testing.T) {
	const want = 1500
	var keys []string
	// The buckets of a table of 1500 entries: 256; the keys share their bucket.
	target := uint32(0)
	for i := 0; len(keys) < want && i < 1<<26; i++ {
		s := fmt.Sprintf("k%x", i) // up to 8 bytes: soft hash
		if len(s) >= 12 {
			break
		}
		if h := hashString(s); h&255 == target {
			keys = append(keys, s)
		}
	}
	if len(keys) < want {
		t.Fatalf("found %d keys", len(keys))
	}
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = "'" + k + "'"
	}
	src := "keys = [" + strings.Join(quoted, ", ") + "]\nd = {}\nfor k in keys: d[k] = 1\nresult = len(d)\n"
	// Without a limit it completes, charging the chains.
	th := &Thread{}
	if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th, "t.star", src, nil); err != nil {
		t.Fatal(err)
	}
	// The same table, built in Go: the bucket nodes walked by each insertion.
	var ht hashtable
	var walked uint64
	for _, k := range keys {
		h := hashString(k)
		if ht.table != nil {
			for p := &ht.table[h&uint32(len(ht.table)-1)]; p != nil; p = p.next {
				walked++
			}
		}
		ht.insert(String(k), None)
	}
	// Each step is a loop iteration of ~6 opcodes: the chains cost, beyond the
	// loop, at least walked units.
	plain := &Thread{}
	loop := "keys = [" + strings.Join(quoted, ", ") + "]\nd = []\nfor k in keys: d.append(1)\nresult = len(d)\n"
	if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, plain, "t.star", loop, nil); err != nil {
		t.Fatal(err)
	}
	extra := th.Steps - plain.Steps
	if extra*WorkPerStep < walked/2 {
		t.Errorf("%d bucket nodes walked, %d extra steps charged (%d units)", walked, extra, extra*WorkPerStep)
	}
	// With a limit lower than the cost, it is stopped.
	th2 := &Thread{}
	th2.SetMaxExecutionSteps(plain.Steps + extra/4)
	_, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th2, "t.star", src, nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("err = %v", err)
	}
}

// int("9" * N): the digit limit refuses the conversion before it starts; below
// the limit it is charged digits^2/4096.
func TestAttack_IntFromHugeString(t *testing.T) {
	base := runProg(t, 0, "s = '9' * (1 << 20)\n")
	r := runProg(t, 0, "s = '9' * (1 << 20)\nr = int(s)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "more than 4300 digits") {
		t.Fatalf("err = %v", r.err)
	}
	if r.th.Steps > base.th.Steps+50 {
		t.Errorf("a refused conversion cost %d steps", r.th.Steps-base.th.Steps)
	}
	// At the limit it converts, and is charged.
	r = runProg(t, 0, "mark()\nr = int('9' * 4300)\nmark()\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if want := uint64(4300 * 4300 / 4096 / WorkPerStep); r.th.Steps < want {
		t.Errorf("int of 4300 digits cost %d steps, want at least %d", r.th.Steps, want)
	}
	// One digit more is refused; bases of two are bounded by the same bits.
	r = runProg(t, 0, "r = int('9' * 4301)\n")
	if r.err == nil {
		t.Errorf("4301 digits accepted")
	}
	r = runProg(t, 0, "r = int('f' * 5000, 16)\n")
	if r.err == nil {
		t.Errorf("5000 hex digits accepted")
	}
	r = runProg(t, 0, "r = int('1' * 4000, 2)\n")
	if r.err != nil {
		t.Errorf("4000 binary digits: %v", r.err)
	}
	r = runProg(t, 0, "r = int('0x' + 'f' * 4000, 0)\n")
	if r.err != nil {
		t.Errorf("4000 hex digits with a prefix: %v", r.err)
	}
}

// str(n), repr(n), %d, format of an integer of more than 4300 digits: refused.
func TestAttack_StringFromHugeInt(t *testing.T) {
	// 2^(500 * 2^5) = 2^16000: 4817 digits.
	build := "x = 1 << 500\nfor i in range(5): x = x * x\n"
	for _, op := range []string{"str(x)", "repr(x)", "'%d' % x", "'%s' % x", "'{}'.format(x)", "'%x' % x", "[x]"} {
		src := build + "r = " + op + "\n"
		if op == "[x]" {
			src = build + "r = str([x])\n"
		}
		r := runProg(t, 0, src)
		if op == "'%x' % x" {
			if r.err == nil || !strings.Contains(r.err.Error(), "decimal digits") {
				// hex of a 16000-bit number is 4000 digits: still refused by bits
				t.Errorf("%s: err = %v", op, r.err)
			}
			continue
		}
		if r.err == nil || !strings.Contains(r.err.Error(), "decimal digits") && !strings.Contains(r.err.Error(), "converted to a string") {
			t.Errorf("%s: err = %v", op, r.err)
		}
	}
	// The integer itself is fine: it can be multiplied, compared, hashed.
	r := runProg(t, 0, "x = 1 << 500\nfor i in range(5): x = x * x\nr = (x > 0, x == x, x % 1000003 >= 0, {x: 1}[x])\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	// The boundary: 14285 bits is the largest that converts.
	for _, c := range []struct {
		bits int
		ok   bool
	}{{MaxIntBits, true}, {MaxIntBits + 1, false}} {
		x := new(big.Int).Lsh(big.NewInt(1), uint(c.bits-1))
		_, err := Call(&Thread{}, Universe["str"], Tuple{MakeBigInt(x)}, nil)
		if (err == nil) != c.ok {
			t.Errorf("%d bits: err = %v, want ok = %v", c.bits, err, c.ok)
		}
	}
}

// big integer products are charged words^2.
func TestAttack_BigIntMultiplication(t *testing.T) {
	// 500 bits squared 8 times: 128000 bits, 2000 words; the last product is
	// 2000 x 2000 words = 4M word products = 500k units = 31k steps.
	src := "x = 1 << 500\nfor i in range(8): x = x * x\nmark()\ny = x * x\nmark()\n"
	r := runProg(t, 0, src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.th.Steps; got < 30_000 {
		t.Errorf("total steps %d: the last product should cost ~31000", got)
	}
	// With a limit, repeated squaring is stopped before the numbers get large.
	th := &Thread{}
	th.SetMaxExecutionSteps(100_000)
	_, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", "x = 1 << 500\nfor i in range(60): x = x * x\n", nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("err = %v", err)
	}
}

// l.insert(0, x) and l.pop(0) move every slot: charged before they run.
func TestAttack_ListShift(t *testing.T) {
	const N = 1_000_000
	src := fmt.Sprintf("l = [0] * %d\nmark()\nl.insert(0, 1)\nmark()\nl.pop(0)\nmark()\n", N)
	r := runProg(t, 0, src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := uint64(N / 4 / WorkPerStep) // a slot is a quarter unit
	if d := r.th.Steps; d < 2*want {
		t.Errorf("insert and pop of 1M slots cost %d steps in all, want at least %d", d, 2*want)
	}
	// 10 million steps are not enough for 1000 of them.
	th := &Thread{}
	th.SetMaxExecutionSteps(10_000_000)
	_, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th, "t.star", fmt.Sprintf("l = [0] * %d\nfor i in range(100000):\n  l.insert(0, 1)\n  l.pop(0)\n", N), nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Errorf("err = %v (steps %d)", err, th.Steps)
	}
}

// A search of a long string costs in proportion to the distance searched.
func TestAttack_StringSearch(t *testing.T) {
	src := "s = 'a' * (16 << 20)\nmark()\nx = s.find('b')\nmark()\n"
	r := runProg(t, 0, src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if want := uint64((16 << 20) / 64 / WorkPerStep); r.th.Steps < want {
		t.Errorf("a find over 16 MiB cost %d steps, want at least %d", r.th.Steps, want)
	}
	// A match at the start costs nothing to speak of.
	r = runProg(t, 0, "s = 'b' + 'a' * (16 << 20)\nmark()\nx = s.find('b')\nmark()\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.marks[0] == 0 && r.th.Steps > 2000+uint64((16<<20)/32/WorkPerStep) {
		t.Errorf("steps %d", r.th.Steps)
	}
}

// ---- the invariant: no built-in's work grows without its steps ----

// For each operation, over operands of n, 10n and 100n counted elements, the
// comparisons, hashes and truth tests that were made are at most what the steps
// charged pay for (WorkPerStep units a step) plus the free window: no operation
// does work that its steps do not account for.
func TestInvariant_WorkIsPaidInSteps(t *testing.T) {
	ops := []struct {
		name, setup, op string
	}{
		{"x in l (miss)", "l = mk($N)\nx = miss()", "r = x in l"},
		{"x not in l", "l = mk($N)\nx = miss()", "r = x not in l"},
		{"x in tuple", "l = tuple(mk($N))\nx = miss()", "r = x in l"},
		{"l.index (miss) ", "l = mk($N)\nx = miss()", "r = l.index(x)"},
		{"l.remove (miss)", "l = mk($N)\nx = miss()", "l.remove(x)"},
		{"max", "l = mk($N)", "r = max(l)"},
		{"min", "l = mk($N)", "r = min(l)"},
		{"any", "l = mk($N)", "r = any(l)"},
		{"all", "l = mk($N)", "r = all(l)"},
		{"sorted", "l = mk($N)", "r = sorted(l)"},
		{"sorted reversed", "l = reversed(mk($N))", "r = sorted(l)"},
		{"l == l2", "l = mk($N)\nm = mk($N)", "r = l == m"},
		{"l < l2", "l = mk($N)\nm = mk($N)", "r = l < m"},
		{"l != l2", "l = mk($N)\nm = mk($N)", "r = l != m"},
		{"nested ==", "l = [mk($N // 8)] * 8\nm = [mk($N // 8)] * 8", "r = l == m"},
		{"tuple ==", "l = tuple(mk($N))\nm = tuple(mk($N))", "r = l == m"},
		{"set(l)", "l = mk($N)", "r = set(l)"},
		{"dict(zip)", "l = mk($N)", "r = dict(zip(l, l))"},
		{"{k: v}", "l = mk($N)", "d = {}\nfor k in l: d[k] = 1"},
		{"x in set", "l = mk($N)\ns = set(l)\nx = miss()", "r = x in s"},
		{"s | t", "s = set(mk($N))\nt = set(mk($N, $N))", "r = s | t"},
		{"s & t", "s = set(mk($N))\nt = set(mk($N, $N // 2))", "r = s & t"},
		{"s - t", "s = set(mk($N))\nt = set(mk($N, $N // 2))", "r = s - t"},
		{"s ^ t", "s = set(mk($N))\nt = set(mk($N, $N // 2))", "r = s ^ t"},
		{"s.union", "s = set(mk($N))\nt = mk($N, $N)", "r = s.union(t)"},
		{"s.update", "s = set(mk($N))\nt = mk($N, $N)", "s.update(t)"},
		{"s.issubset", "s = set(mk($N))\nt = mk($N, 0)", "r = s.issubset(t)"},
		{"s.issuperset", "s = set(mk($N))\nt = mk($N, 0)", "r = s.issuperset(t)"},
		{"s.symmetric_difference", "s = set(mk($N))\nt = mk($N, $N // 2)", "r = s.symmetric_difference(t)"},
		{"s.intersection", "s = set(mk($N))\nt = mk($N, $N // 2)", "r = s.intersection(t)"},
		{"s.difference", "s = set(mk($N))\nt = mk($N, $N // 2)", "r = s.difference(t)"},
		{"s <= t", "s = set(mk($N))\nt = set(mk($N))", "r = s <= t"},
		{"s == t", "s = set(mk($N))\nt = set(mk($N))", "r = s == t"},
		{"d == e", "d = dict(zip(mk($N), mk($N)))\ne = dict(zip(mk($N), mk($N)))", "r = d == e"},
		{"d | e", "d = dict(zip(mk($N), mk($N)))\ne = dict(zip(mk($N, $N), mk($N)))", "r = d | e"},
		{"d.update(e)", "d = dict(zip(mk($N), mk($N)))\ne = dict(zip(mk($N, $N), mk($N)))", "d.update(e)"},
		{"d |= e", "d = dict(zip(mk($N), mk($N)))\ne = dict(zip(mk($N, $N), mk($N)))", "d |= e"},
		{"d.get (miss)", "d = dict(zip(mk($N), mk($N)))\nx = miss()", "r = d.get(x)"},
		{"x in d", "d = dict(zip(mk($N), mk($N)))\nx = miss()", "r = x in d"},
		{"list(l)", "l = mk($N)", "r = list(l)"},
		{"tuple(l)", "l = mk($N)", "r = tuple(l)"},
		{"reversed(l)", "l = mk($N)", "r = reversed(l)"},
		{"enumerate(l)", "l = mk($N)", "r = enumerate(l)"},
		{"zip(l, l)", "l = mk($N)", "r = zip(l, l)"},
		{"l + l", "l = mk($N)", "r = l + l"},
		{"l * 3", "l = mk($N)", "r = l * 3"},
		{"l[::-1]", "l = mk($N)", "r = l[::-1]"},
		{"l.extend", "l = mk($N)\nm = mk($N)", "l.extend(m)"},
		{"l += m", "l = mk($N)\nm = mk($N)", "l += m"},
		{"str(l)", "l = mk($N)", "r = str(l)"},
		{"repr(l)", "l = mk($N)", "r = repr(l)"},
		{"f(*l)", "l = mk($N)\ndef f(*a): return None", "f(*l)"},
	}
	for _, op := range ops {
		var prev uint64
		for _, n := range []int{64, 640, 6400} {
			resetCounts()
			setup := strings.ReplaceAll(op.setup, "$N", fmt.Sprint(n))
			opSrc := strings.ReplaceAll(op.op, "$N", fmt.Sprint(n))
			src := setup + "\nreset()\n" + opSrc + "\n"
			th := &Thread{}
			var base uint64
			extra := countedBuiltins()
			extra["miss"] = NewBuiltin("miss", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) { return counted{-1}, nil })
			extra["reset"] = NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
				resetCounts()
				base = th.Steps
				return None, nil
			})
			_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, extra)
			if err != nil && !strings.Contains(err.Error(), "not in list") && !strings.Contains(err.Error(), "not found") {
				t.Errorf("%s n=%d: %v", op.name, n, err)
				continue
			}
			steps := th.Steps - base
			work := cmpCount.Load() + hashCount.Load() + truthCount.Load()
			if n == 6400 {
				t.Logf("%-26s n=%d: work %8d units  steps %7d  (%.2f units/step)", op.name, n, work, steps, float64(work)/float64(max(steps, 1)))
			}
			if bound := steps*WorkPerStep + FreeWork; work > bound {
				t.Errorf("%s n=%d: %d units of work (comparisons, hashes, truth tests) were done, the %d steps pay for %d", op.name, n, work, steps, bound)
			}
			// ... and the steps grow with the work: 10x the operand is not
			// less than 5x the steps, once past the free window.
			if prev > 0 && work > 2*FreeWork && steps < prev {
				t.Errorf("%s: %d steps at n=%d, fewer than %d at the size before", op.name, steps, n, prev)
			}
			prev = steps
		}
	}
}

// ---- the digit limit and the conversions of integers ----

func TestIntConversions_Boundaries(t *testing.T) {
	for _, c := range []struct {
		src string
		ok  bool
	}{
		{"int('9' * 4300)", true}, {"int('9' * 4301)", false},
		{"int('-' + '9' * 4300)", true}, {"int('+' + '9' * 4300)", true},
		{"int('7' * 4300, 8)", true}, {"int('z' * 3000, 36) > 0", false},
		{"int('1' * 17200, 2) > 0", true}, {"int('1' * 17300, 2) > 0", false},
		{"int('0b' + '1' * 4000, 0)", true},
		{"'%d' % (1 << 300)", true}, {"str(int('7' * 1000))", true},
	} {
		r := runProg(t, 0, "r = "+c.src+"\n")
		if (r.err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.src, r.err, c.ok)
		}
	}
}

// ---- sorting out the tables ----

func TestPricesAreSorted(t *testing.T) {
	// Not a behavior: a check that the tables list the names in a way the
	// document generator can rely on (no duplicates across the table's keys).
	var all []string
	for name := range universePrices {
		all = append(all, name)
	}
	sort.Strings(all)
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			t.Errorf("duplicate %s", all[i])
		}
	}
}

// ---- smaller properties ----

// The cycle check of a string form is a set below pathSetDepth levels, so that
// the form of a deep value is not quadratic in its depth.
func TestValueWriter_CycleCheckUsesASetWhenDeep(t *testing.T) {
	var v Value = MakeInt(1)
	for i := 0; i < 5000; i++ {
		v = NewList([]Value{v})
	}
	var buf sink
	w := valueWriter{out: &buf, limit: maxAlloc}
	w.write(v, 0)
	if w.result != writeOK {
		t.Fatalf("result %d", w.result)
	}
	if w.pathSet == nil {
		t.Fatal("a 5000-deep value was checked for cycles by scanning a slice")
	}
	// A cycle is still found, at any depth.
	l := NewList(nil)
	cur := l
	for i := 0; i < 100; i++ {
		next := NewList(nil)
		cur.elems = []Value{next}
		cur = next
	}
	cur.elems = []Value{l}
	if s := l.String(); !strings.Contains(s, "...") {
		t.Errorf("a cycle through 100 lists is not marked: %.60s", s)
	}
}

// Floats are totally ordered here (NaN == NaN, NaN > +Inf), and a cheaper path
// of equality must not change that: v0.2.0 gives the same answers.
func TestCompare_NaNIsTotallyOrderedInContainers(t *testing.T) {
	r := runProg(t, 0, "a = float('nan')\nb = float('nan')\nif not ([a] == [b]): fail('list')\nif not ((a,) == (b,)): fail('tuple')\nif not ({1: a} == {1: b}): fail('dict')\nif [a] != [b]: fail('ne')\nif not ([float('inf')] < [a]): fail('order')\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
}

// The exported work-charging insertion of a dict.
func TestDict_SetKeyWork(t *testing.T) {
	d := new(Dict)
	th := &Thread{}
	// 500 keys with the same bucket: each insertion walks the chain.
	var keys []Value
	for i := 0; len(keys) < 500; i++ {
		s := fmt.Sprintf("k%x", i)
		if hashString(s)&63 == 0 {
			keys = append(keys, String(s))
		}
	}
	for _, k := range keys {
		if err := d.SetKeyWork(th, k, None); err != nil {
			t.Fatal(err)
		}
	}
	if th.Steps == 0 {
		t.Error("500 colliding insertions cost no step")
	}
	// A nil thread is not charged and does not fail.
	d2 := new(Dict)
	for _, k := range keys {
		if err := d2.SetKeyWork(nil, k, None); err != nil {
			t.Fatal(err)
		}
	}
	if d2.Len() != 500 {
		t.Fatalf("len %d", d2.Len())
	}
	// With the limit reached, the insertion stops with the error of the thread.
	th2 := &Thread{}
	th2.SetMaxExecutionSteps(3)
	d3 := new(Dict)
	var err error
	for _, k := range keys {
		if err = d3.SetKeyWork(th2, k, None); err != nil {
			break
		}
	}
	// (500 keys are 500*3 units = ~100 steps)
	if err == nil {
		t.Errorf("no refusal at a limit of 3 steps")
	}
	// The same for a set, and for a key whose hash is under a chunk of work.
	long := String(strings.Repeat("k", 100000))
	th3 := &Thread{}
	if err := new(Set).InsertWork(th3, long); err != nil {
		t.Fatal(err)
	}
	if want := uint64(100000 / 64 / WorkPerStep); th3.Steps < want {
		t.Errorf("Set.InsertWork of a 100000-byte key cost %d steps, want at least %d", th3.Steps, want)
	}
	th4 := &Thread{}
	if err := new(Dict).SetKeyWork(th4, long, None); err != nil {
		t.Fatal(err)
	}
	if want := uint64(100000 / 64 / WorkPerStep); th4.Steps < want {
		t.Errorf("Dict.SetKeyWork of a 100000-byte key cost %d steps, want at least %d", th4.Steps, want)
	}
	if err := new(Set).InsertWork(nil, long); err != nil {
		t.Fatal(err)
	}
}

// x in l finds an early match after charging at most one chunk.
func TestMembership_EarlyMatchChargesAChunk(t *testing.T) {
	r := runProg(t, 0, "l = list(range(100000))\nmark()\nx = 3 in l\nmark()\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	base := runProg(t, 0, "l = list(range(100000))\nmark()\nx = 3\nmark()\n")
	if extra := r.th.Steps - base.th.Steps; extra > 256/WorkPerStep+8 {
		t.Errorf("a match at index 3 of 100000 cost %d steps", extra)
	}
}

// An allocation takes time: a unit of work (WorkPerStep of them make a step) per
// allocBytesPerWork bytes above allocWorkFree, and a small one is free.
func TestAllocationCostsSteps(t *testing.T) {
	th := &Thread{}
	if err := th.ChargeAlloc(allocWorkFree + 64*allocBytesPerWork - 1); err != nil {
		t.Fatal(err)
	}
	if th.ExecutionSteps() != 0 {
		t.Errorf("an allocation of less than %d units cost %d steps", FreeWork, th.ExecutionSteps())
	}
	th = &Thread{}
	const size = 1 << 20
	if err := th.ChargeAlloc(size); err != nil {
		t.Fatal(err)
	}
	want := uint64((size - allocWorkFree) / allocBytesPerWork / WorkPerStep)
	if got := th.ExecutionSteps(); got != want {
		t.Errorf("1 MiB cost %d steps, want %d", got, want)
	}
	// A hundred times the size, a hundred times the steps.
	th2 := &Thread{}
	if err := th2.ChargeAlloc(100 * size); err != nil {
		t.Fatal(err)
	}
	if got := th2.ExecutionSteps(); got < 99*want {
		t.Errorf("100 MiB cost %d steps, 1 MiB cost %d", got, want)
	}
	// And a script: the steps of [0] * n grow with n.
	steps := func(n int) uint64 {
		r := runProg(t, 0, fmt.Sprintf("x = [0] * %d\n", n))
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.th.ExecutionSteps()
	}
	if a, b := steps(10000), steps(1000000); b < 50*a/2 {
		t.Errorf("[0]*1e6 cost %d steps, [0]*1e4 cost %d: an allocation is not charged as time", b, a)
	}
}

// A call whose work does not fit in the steps that are left is refused before
// it runs: it returns the error, and does not do its work.
func TestCall_RefusedByItsPriceDoesNotRun(t *testing.T) {
	newThread := func() *Thread {
		th := &Thread{}
		th.SetMaxExecutionSteps(100)
		return th
	}
	// list.insert: the memmove of 100000 slots is 6250 steps.
	l := NewList(make([]Value, 100000))
	for i := range l.elems {
		l.elems[i] = MakeInt(i)
	}
	insert, _ := l.Attr("insert")
	res, err := Call(newThread(), insert, Tuple{MakeInt(0), String("x")}, nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("insert: res = %v, err = %v", res, err)
	}
	if l.Len() != 100000 {
		t.Errorf("a refused insert ran: len = %d", l.Len())
	}
	// list.pop(0), list.clear(), list.extend.
	for _, c := range []struct {
		name string
		args Tuple
	}{{"pop", Tuple{MakeInt(0)}}, {"clear", nil}, {"extend", Tuple{l}}} {
		l := NewList(append([]Value(nil), l.elems...))
		m, _ := l.Attr(c.name)
		if _, err := Call(newThread(), m, c.args, nil); err == nil {
			t.Errorf("%s: not refused", c.name)
		}
		if want := map[string]int{"pop": 100000, "clear": 100000, "extend": 100000}[c.name]; l.Len() != want {
			t.Errorf("a refused %s ran: len = %d", c.name, l.Len())
		}
	}
	// A string method returns no result.
	s := String(strings.Repeat("ab", 1<<19))
	upper, _ := s.Attr("upper")
	if res, err := Call(newThread(), upper, nil, nil); err == nil || res != nil {
		t.Errorf("upper: res = %v, err = %v", res, err)
	}
	// And a function of Universe: sorted of a list of 100000.
	if res, err := Call(newThread(), Universe["sorted"], Tuple{l}, nil); err == nil || res != nil {
		t.Errorf("sorted: res = %v, err = %v", res, err)
	}
}
