package starlark_test

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// A condRun is the result of running "if COND: return 'then' / return 'else'"
// under a Tracer that records every event in a compact form.
type condRun struct {
	events []string // OnBranch and OnCompare, in order
	last   *branchEvent
	then   bool
}

type branchEvent struct {
	b      starlark.Branch
	tested starlark.Value
	taken  bool
}

func tf(b bool) string {
	if b {
		return "T"
	}
	return "F"
}

// shape renders the part of an event that must not depend on redundant
// parentheses or on "not not": it has no position.
func (e branchEvent) shape() string {
	s := fmt.Sprintf("%s#%d chain=%d parent=%d %s", e.b.Kind, e.b.Operand, e.b.ChainID, e.b.ParentChain, tf(e.taken))
	if e.b.Negated {
		s += " neg"
	}
	return s
}

const condPrelude = `def f(a, b, c, d, xs):
    if %s:
        return "then"
    return "else"
`

// runCond compiles the condition and calls f(a, b, c, d, xs) with the four
// Booleans (the integers 0 or 1: a mix of ints and bools on purpose is not
// needed) and xs = [1, 2].
func runCond(t *testing.T, cond string, a, b, c, d bool) condRun {
	t.Helper()
	var run condRun
	tr := &starlark.Tracer{
		OnBranch: func(_ *starlark.Thread, _ *starlark.Function, br starlark.Branch, tested starlark.Value, taken bool) {
			ev := branchEvent{br, tested, taken}
			run.events = append(run.events, ev.shape())
			run.last = &ev
		},
		OnCompare: func(_ *starlark.Thread, _ *starlark.Function, br starlark.Branch, op syntax.Token, x, y starlark.Value, result bool) {
			sp := ""
			if op == syntax.IN {
				sp = " "
			}
			run.events = append(run.events, fmt.Sprintf("cmp %s#%d %s%s%s%s%s -> %s", br.Kind, br.Operand, x, sp, op, sp, y, tf(result)))
		},
	}
	thread, globals, err := execTraced(t, fmt.Sprintf(condPrelude, cond), nil, nil)
	if err != nil {
		t.Fatalf("if %s: %v", cond, err)
	}
	thread.Trace = tr
	bools := []starlark.Value{starlark.MakeInt(0), starlark.MakeInt(1)}
	pick := func(v bool) starlark.Value {
		if v {
			return bools[1]
		}
		return bools[0]
	}
	xs := starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.MakeInt(2)})
	res, err := starlark.Call(thread, globals["f"], starlark.Tuple{pick(a), pick(b), pick(c), pick(d), xs}, nil)
	if err != nil {
		t.Fatalf("if %s: %v", cond, err)
	}
	run.then = res.(starlark.String) == "then"
	return run
}

func eachBools(n int, fn func(v [4]bool)) {
	for m := 0; m < 1<<n; m++ {
		var v [4]bool
		for i := 0; i < n; i++ {
			v[i] = m&(1<<i) != 0
		}
		fn(v)
	}
}

// TestTraceNotTruthTable: for every way a "not" can stand, over all inputs:
// the branch that really ran is the one that the last event names by
// taken XOR Negated (true: the "then" side), and taken is the truth of the
// expression whatever "not" is around it. The oracle is the condition
// written with Go operators.
func TestTraceNotTruthTable(t *testing.T) {
	for _, test := range []struct {
		cond   string
		oracle func(a, b, c, d bool) bool
	}{
		{"a", func(a, b, c, d bool) bool { return a }},
		{"not a", func(a, b, c, d bool) bool { return !a }},
		{"not not a", func(a, b, c, d bool) bool { return a }},
		{"not not not a", func(a, b, c, d bool) bool { return !a }},
		{"not (a)", func(a, b, c, d bool) bool { return !a }},
		{"a in xs", func(a, b, c, d bool) bool { return a }}, // xs = [1, 2]; a is 0 or 1
		{"a not in xs", func(a, b, c, d bool) bool { return !a }},
		{"not a in xs", func(a, b, c, d bool) bool { return !a }},
		{"not (a not in xs)", func(a, b, c, d bool) bool { return a }},
		{"not (a in xs)", func(a, b, c, d bool) bool { return !a }},
		{"not a not in xs", func(a, b, c, d bool) bool { return a }},
		{"not (a and b)", func(a, b, c, d bool) bool { return !(a && b) }},
		{"not (a or b)", func(a, b, c, d bool) bool { return !(a || b) }},
		{"not not (a and b)", func(a, b, c, d bool) bool { return a && b }},
		{"not (not (a and b))", func(a, b, c, d bool) bool { return a && b }},
		{"(not (a and b))", func(a, b, c, d bool) bool { return !(a && b) }},
		// not outside and inside a chain
		{"not a and b", func(a, b, c, d bool) bool { return !a && b }},
		{"a and not b", func(a, b, c, d bool) bool { return a && !b }},
		{"a or not b", func(a, b, c, d bool) bool { return a || !b }},
		{"not a or b", func(a, b, c, d bool) bool { return !a || b }},
		{"not (a and b) and c", func(a, b, c, d bool) bool { return !(a && b) && c }},
		{"a and not (b and c)", func(a, b, c, d bool) bool { return a && !(b && c) }},
		{"a and not (b or c)", func(a, b, c, d bool) bool { return a && !(b || c) }},
		{"not (a or b) or c", func(a, b, c, d bool) bool { return !(a || b) || c }},
		{"(a and not (b and c))", func(a, b, c, d bool) bool { return a && !(b && c) }},
		{"(not a or not (b and c))", func(a, b, c, d bool) bool { return !a || !(b && c) }},
		{"not (not (a and b))", func(a, b, c, d bool) bool { return a && b }},
		{"not (a and not (b or c))", func(a, b, c, d bool) bool { return !(a && !(b || c)) }},
		{"not (a and b) or not (c and d)", func(a, b, c, d bool) bool { return !(a && b) || !(c && d) }},
		{"not not (a and b) and not not c", func(a, b, c, d bool) bool { return a && b && c }},
		{"a not in xs and not (b not in xs)", func(a, b, c, d bool) bool { return !a && b }},
		{"not a not in xs and b not in xs", func(a, b, c, d bool) bool { return a && !b }},
		{"(a not in xs or b) and c", func(a, b, c, d bool) bool { return (!a || b) && c }},
		{"(not a == 1 or b) and c", func(a, b, c, d bool) bool { return (!a || b) && c }},
		{"not (a == 1 and b == 1) and not (c < 1 or d < 1)", func(a, b, c, d bool) bool { return !(a && b) && !(!c || !d) }},
	} {
		t.Run(test.cond, func(t *testing.T) {
			eachBools(4, func(v [4]bool) {
				run := runCond(t, test.cond, v[0], v[1], v[2], v[3])
				want := test.oracle(v[0], v[1], v[2], v[3])
				if run.then != want {
					t.Fatalf("a,b,c,d=%v: ran the %s branch, want %s", v, map[bool]string{true: "then", false: "else"}[run.then], map[bool]string{true: "then", false: "else"}[want])
				}
				if run.last == nil {
					t.Fatalf("a,b,c,d=%v: no event", v)
				}
				if got := run.last.taken != run.last.b.Negated; got != run.then {
					t.Errorf("a,b,c,d=%v: last event taken=%v Negated=%v says the %v side, the %v side ran (events %v)",
						v, run.last.taken, run.last.b.Negated, got, run.then, run.events)
				}
				if run.last.tested.Truth() != starlark.Bool(run.last.taken) {
					t.Errorf("a,b,c,d=%v: taken=%v but tested=%v", v, run.last.taken, run.last.tested)
				}
			})
		})
	}
}

// TestTraceNotTaken: for each way a "not" can stand, the exact events with
// taken (the truth of the expression), Negated and the branch that ran.
func TestTraceNotTaken(t *testing.T) {
	const (
		then = "then"
		els  = "else"
	)
	for _, test := range []struct {
		cond string
		in   [4]bool // a, b, c, d (xs is [1, 2]; a is 0 or 1)
		want []string
		ran  string
	}{
		{"not a", [4]bool{true}, []string{"if#-1 chain=-1 parent=-1 T neg"}, els},
		{"not a", [4]bool{false}, []string{"if#-1 chain=-1 parent=-1 F neg"}, then},
		{"not not a", [4]bool{true}, []string{"if#-1 chain=-1 parent=-1 T"}, then},
		{"not not a", [4]bool{false}, []string{"if#-1 chain=-1 parent=-1 F"}, els},
		{"a not in xs", [4]bool{true}, []string{"cmp if#-1 1 in [1, 2] -> T", "if#-1 chain=-1 parent=-1 T neg"}, els},
		{"a not in xs", [4]bool{false}, []string{"cmp if#-1 0 in [1, 2] -> F", "if#-1 chain=-1 parent=-1 F neg"}, then},
		{"not (a not in xs)", [4]bool{true}, []string{"cmp if#-1 1 in [1, 2] -> T", "if#-1 chain=-1 parent=-1 T"}, then},
		{"not (a not in xs)", [4]bool{false}, []string{"cmp if#-1 0 in [1, 2] -> F", "if#-1 chain=-1 parent=-1 F"}, els},
		{"not (a and b)", [4]bool{true, true}, []string{"and#0 chain=0 parent=-1 T neg", "and#1 chain=0 parent=-1 T neg"}, els},
		{"not (a and b)", [4]bool{true, false}, []string{"and#0 chain=0 parent=-1 T neg", "and#1 chain=0 parent=-1 F neg"}, then},
		{"not (a and b)", [4]bool{false, true}, []string{"and#0 chain=0 parent=-1 F neg"}, then},
		// the operand's own not, outside and inside a chain
		{"not a and b", [4]bool{true, true}, []string{"and#0 chain=0 parent=-1 T neg"}, els},
		{"not a and b", [4]bool{false, true}, []string{"and#0 chain=0 parent=-1 F neg", "and#1 chain=0 parent=-1 T"}, then},
		{"not a and b", [4]bool{false, false}, []string{"and#0 chain=0 parent=-1 F neg", "and#1 chain=0 parent=-1 F"}, els},
		{"a and not b", [4]bool{true, false}, []string{"and#0 chain=0 parent=-1 T", "and#1 chain=0 parent=-1 F neg"}, then},
		{"a and not b", [4]bool{true, true}, []string{"and#0 chain=0 parent=-1 T", "and#1 chain=0 parent=-1 T neg"}, els},
		{"a and not b", [4]bool{false, false}, []string{"and#0 chain=0 parent=-1 F"}, els},
		{"a or not b", [4]bool{false, false}, []string{"or#0 chain=0 parent=-1 F", "or#1 chain=0 parent=-1 F neg"}, then},
	} {
		run := runCond(t, test.cond, test.in[0], test.in[1], test.in[2], test.in[3])
		diffLines(t, fmt.Sprintf("if %s with %v", test.cond, test.in), run.events, test.want)
		if got := map[bool]string{true: then, false: els}[run.then]; got != test.ran {
			t.Errorf("if %s with %v: the %s branch ran, want %s", test.cond, test.in, got, test.ran)
		}
	}
}

// TestTraceNotInChains: the four programs in which the chain stands under a
// "not" that the compiler evaluates as a value. Every operand reports, with
// the truth of the operand itself and Negated for the "not" around it.
func TestTraceNotInChains(t *testing.T) {
	for _, test := range []struct {
		cond string
		in   [4]bool
		want []string
	}{
		{"not (a and b) and c", [4]bool{true, true, true}, []string{
			"and#0 chain=1 parent=0 T neg", "and#1 chain=1 parent=0 T neg"}},
		{"not (a and b) and c", [4]bool{true, false, true}, []string{
			"and#0 chain=1 parent=0 T neg", "and#1 chain=1 parent=0 F neg", "and#1 chain=0 parent=-1 T"}},
		{"not (a and b) and c", [4]bool{false, true, true}, []string{
			"and#0 chain=1 parent=0 F neg", "and#1 chain=0 parent=-1 T"}},
		{"not (a and b) and c", [4]bool{false, true, false}, []string{
			"and#0 chain=1 parent=0 F neg", "and#1 chain=0 parent=-1 F"}},

		{"(a and not (b and c))", [4]bool{false, true, true}, []string{
			"and#0 chain=0 parent=-1 F"}},
		{"(a and not (b and c))", [4]bool{true, false, true}, []string{
			"and#0 chain=0 parent=-1 T", "and#0 chain=1 parent=0 F neg"}},
		{"(a and not (b and c))", [4]bool{true, true, false}, []string{
			"and#0 chain=0 parent=-1 T", "and#0 chain=1 parent=0 T neg", "and#1 chain=1 parent=0 F neg"}},
		{"(a and not (b and c))", [4]bool{true, true, true}, []string{
			"and#0 chain=0 parent=-1 T", "and#0 chain=1 parent=0 T neg", "and#1 chain=1 parent=0 T neg"}},

		{"(not a or not (b and c))", [4]bool{false, true, true}, []string{
			"or#0 chain=0 parent=-1 F neg"}}, // not a is true
		{"(not a or not (b and c))", [4]bool{true, false, true}, []string{
			"or#0 chain=0 parent=-1 T neg", "and#0 chain=1 parent=0 F neg"}},
		{"(not a or not (b and c))", [4]bool{true, true, true}, []string{
			"or#0 chain=0 parent=-1 T neg", "and#0 chain=1 parent=0 T neg", "and#1 chain=1 parent=0 T neg"}},

		{"not (not (a and b))", [4]bool{false, true}, []string{"and#0 chain=0 parent=-1 F"}},
		{"not (not (a and b))", [4]bool{true, false}, []string{"and#0 chain=0 parent=-1 T", "and#1 chain=0 parent=-1 F"}},
		{"not (not (a and b))", [4]bool{true, true}, []string{"and#0 chain=0 parent=-1 T", "and#1 chain=0 parent=-1 T"}},
	} {
		run := runCond(t, test.cond, test.in[0], test.in[1], test.in[2], test.in[3])
		diffLines(t, fmt.Sprintf("if %s with %v", test.cond, test.in), run.events, test.want)
	}
}

// TestTraceRedundantSyntax: redundant parentheses and a doubled "not" change
// no event but for the positions: the kinds, operand numbers, chains, nesting,
// taken, Negated and the comparisons are the same over all inputs.
func TestTraceRedundantSyntax(t *testing.T) {
	for _, test := range []struct {
		base     string
		variants []string
	}{
		{"a", []string{"(a)", "not not a", "not (not a)", "not not not not a", "(not (not (a)))"}},
		{"not a", []string{"(not a)", "not (a)", "not not not a", "not ((a))"}},
		{"a and b", []string{"(a and b)", "(a) and (b)", "not not (a and b)", "not not a and not not b", "((a)) and (not not (b))"}},
		{"a or b or c", []string{"(a or b or c)", "(a) or (b) or (not not c)", "not not (a or b or c)", "not not a or b or c"}},
		{"a or b and c", []string{"a or (b and c)", "(a or (b and c))", "not not (a or b and c)", "(a) or ((b) and (c))"}},
		{"a and b or c", []string{"(a and b) or c", "((a and b) or c)", "not not (a and b or c)"}},
		{"not (a and b)", []string{"(not (a and b))", "not ((a and b))", "not not not (a and b)", "not (not not (a and b))"}},
		{"not (a and b) and c", []string{
			"(not (a and b)) and c", "not not not (a and b) and c", "not ((a and b)) and c",
			"(not (a and b)) and (c)", "not (not not (a and b)) and c", "(not (a and b) and c)"}},
		{"a and not (b and c)", []string{
			"a and (not (b and c))", "(a and not (b and c))", "a and not not not (b and c)",
			"a and not ((b and c))", "not not a and not (b and c)"}},
		{"not (a or b) or c", []string{"(not (a or b)) or c", "not not not (a or b) or c", "not ((a or b)) or (c)"}},
		{"(not a or not (b and c))", []string{"(not not not a or not (b and c))", "((not a) or (not ((b and c))))", "(not a or not not not (b and c))"}},
		{"not (not (a and b))", []string{"(a and b)", "a and b", "not not (a and b)", "not not (not not (a and b))"}},
		{"(a or b) and (c or d)", []string{"((a) or (b)) and (not not (c or d))", "(a or b) and not not (c or d)"}},
		{"a == 1 and b not in xs", []string{"(a == 1) and (b not in xs)", "(a == 1 and b not in xs)", "not not (a == 1 and b not in xs)", "a == 1 and not (b in xs)"}},
		{"not a == 1 and b", []string{"(not a == 1) and b", "not not not a == 1 and b", "not (a == 1) and b"}},
		{"a not in xs or not (b not in xs)", []string{"(a not in xs) or (not (b not in xs))", "not not (a not in xs) or b in xs"}},
	} {
		for _, variant := range test.variants {
			eachBools(4, func(v [4]bool) {
				want := runCond(t, test.base, v[0], v[1], v[2], v[3])
				got := runCond(t, variant, v[0], v[1], v[2], v[3])
				if got.then != want.then {
					t.Errorf("%q vs %q with %v: ran different branches", test.base, variant, v)
				}
				// "a and not (b in xs)" differs from "... b not in xs" in where
				// the "not" stands but not in the answer: compare the events
				diffLines(t, fmt.Sprintf("events of %q vs %q with %v", test.base, variant, v), got.events, want.events)
			})
		}
	}
}

// TestTraceCompareEverywhere: a comparison that a branch tests reports to
// OnCompare whatever its place among the operands: first, in the middle,
// last, in a chain of its own, whether the compiler needed a DUP and a NOT
// after it or not.
func TestTraceCompareEverywhere(t *testing.T) {
	type cmpCase struct {
		expr string // with a=1 (and xs=[1, 2])
		text string // x op y of the event
		res  bool
	}
	cmps := []cmpCase{
		{"a == 1", "1==1", true},
		{"a != 1", "1!=1", false},
		{"a < 2", "1<2", true},
		{"a >= 2", "1>=2", false},
		{"a in xs", "1 in [1, 2]", true},
		{"a not in xs", "1 in [1, 2]", true},
		{"not a == 1", "1==1", true},
		{"not a in xs", "1 in [1, 2]", true},
		{"not (a not in xs)", "1 in [1, 2]", true},
		{"not not a < 2", "1<2", true},
		{"(a == 1)", "1==1", true},
	}
	// d is 1 (does not stop an and), c is 0 (does not stop an or); a is 1
	type shape struct {
		tmpl    string
		kind    string
		operand int
	}
	shapes := []shape{
		{"X and d and d", "and", 0},
		{"d and X and d", "and", 1},
		{"d and d and X", "and", 2},
		{"X or c or c", "or", 0},
		{"c or X or c", "or", 1},
		{"c or c or X", "or", 2},
		{"(X and d) and d", "and", 0},
		{"d and (X and d)", "and", 0},
		{"d and (d and X)", "and", 1},
		{"(c or X) and d", "or", 1},
		{"c or (X and d)", "and", 0},
		{"not (X and d) and d", "and", 0},
		{"d and not (d and X) and d", "and", 1},
		{"d and not (d and X)", "and", 1},
		{"not (c or X) or d", "or", 1},
		{"X", "if", -1},
		{"not (X)", "if", -1},
	}
	for _, cc := range cmps {
		for _, sh := range shapes {
			cond := strings.ReplaceAll(sh.tmpl, "X", "("+cc.expr+")")
			if sh.tmpl == "X" {
				cond = cc.expr
			}
			// run with a=1, c=0, d=1
			run := runCond(t, cond, true, false, false, true)
			_ = run
			var cmpEvents []string
			for _, e := range run.events {
				if strings.HasPrefix(e, "cmp ") {
					cmpEvents = append(cmpEvents, e)
				}
			}
			want := fmt.Sprintf("cmp %s#%d %s -> %s", sh.kind, sh.operand, cc.text, tf(cc.res))
			if len(cmpEvents) != 1 || cmpEvents[0] != want {
				t.Errorf("if %s: compare events %q, want [%q]\nall events: %q", cond, cmpEvents, want, run.events)
			}
		}
	}
}
