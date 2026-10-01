package json_test

// Tests of the allocation accounting of the json module (see alloc.go in
// package starlark): the strings and values it builds are charged to the
// thread's budget before they are built.

import (
	"errors"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	budget1MiB   = 1 << 20
	budgetPrefix = "starlark: allocation budget exhausted: "
)

type run struct {
	th    *starlark.Thread
	err   error
	marks []uint64
}

func exec(t *testing.T, budget uint64, src string) *run {
	t.Helper()
	r := &run{th: &starlark.Thread{Name: "t"}}
	r.th.SetMaxAllocBytes(budget)
	predeclared := starlark.StringDict{
		"json": json.Module,
		"mark": starlark.NewBuiltin("mark", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			r.marks = append(r.marks, th.AllocatedBytes())
			return starlark.None, nil
		}),
	}
	opts := &syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}
	_, r.err = starlark.ExecFileOptions(opts, r.th, "t.star", src, predeclared)
	return r
}

func TestJSON_ChargesExactly(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"encode", "x = [1, 2, 3]", "r = json.encode(x)", 7},                                           // [1,2,3]
		{"encode/string", "x = 'abc'", "r = json.encode(x)", 5},                                        // "abc"
		{"encode_indent", "x = [1, 2]", "r = json.encode_indent(x, indent=' ')", 5 + 10},               // encode + the indented form
		{"indent", "x = '[1,2]'", "r = json.indent(x, indent=' ')", 10},                                // [\n 1,\n 2\n]
		{"decode/list", "x = '[1,2,3]'", "r = json.decode(x)", 3 * 16},                                 // a slot per element
		{"decode/string", "x = '\"abc\"'", "r = json.decode(x)", 3},                                    // by length
		{"decode/dict", "x = '{\"a\":1,\"b\":2}'", "r = json.decode(x)", 2*96 + 2},                     // an entry per key, the key strings
		{"decode/nested", "x = '[[1],[2,3]]'", "r = json.decode(x)", 2*16 + 1*16 + 2*16},               // slots of the outer and inner lists
		{"decode/bigint", "x = '123456789012345678901234567890'", "r = json.decode(x)", 13},            // 97 bits
		{"decode/scalars are free", "x = 'true'", "r = json.decode(x)", 0},                             // not a container
		{"decode/invalid with default", "x = '[1,'", "r = json.decode(x, 5)", 16},                      // a syntax error: the default; charged as built
		{"decode/invalid with default, partly built", "x = '[1,2,x'", "r = json.decode(x, 5)", 2 * 16}, // charged as it was built
	} {
		src := c.setup + "\nmark()\n" + c.op + "\nmark()\n"
		for _, budget := range []uint64{0, 1 << 30} {
			r := exec(t, budget, src)
			if r.err != nil {
				t.Errorf("%s: %v", c.name, r.err)
				continue
			}
			if got := r.marks[1] - r.marks[0]; got != c.want {
				t.Errorf("%s (budget %d): charged %d, want %d", c.name, budget, got, c.want)
			}
		}
	}
}

func wantBudgetError(t *testing.T, name string, r *run, budget uint64) {
	t.Helper()
	if r.err == nil {
		t.Errorf("%s: succeeded (charged %d of %d)", name, r.th.AllocatedBytes(), budget)
		return
	}
	var be *starlark.AllocBudgetError
	if !errors.As(r.err, &be) || !strings.HasPrefix(r.err.Error(), budgetPrefix) {
		t.Errorf("%s: not the budget error: %v", name, r.err)
		return
	}
	if r.th.AllocatedBytes() > budget {
		t.Errorf("%s: charged %d over the budget %d", name, r.th.AllocatedBytes(), budget)
	}
}

func TestJSON_RefusesOverBudget(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		// shared substructure: the output is exponential in the repetitions
		{"encode/shared", "x = [1]\nfor i in range(40): x = [x, x]\nr = json.encode(x)"},
		{"encode_indent/shared", "x = [1]\nfor i in range(40): x = [x, x]\nr = json.encode_indent(x)"},
		{"encode/large", "s = 'a' * 400000\nr = json.encode([s, s, s])"},
		{"encode/large dict", "s = 'a' * 400000\nr = json.encode({'a': s, 'b': s, 'c': s})"},
		// the indent string is repeated per nesting level and line
		{"indent/long indent", "s = '[' + '1,' * 2000 + '1]'\nr = json.indent(s, indent='x' * 1000)"},
		{"indent/nesting", "s = '[' * 1000 + ']' * 1000\nr = json.indent(s, indent='x' * 100)"},
		{"encode_indent/long indent", "r = json.encode_indent([1] * 2000, indent='x' * 1000)"},
		// decode: slots and entries per element
		{"decode/list", "s = '[' + '1,' * 100000 + '1]'\nr = json.decode(s)"},
		{"decode/dict", "s = '{' + ','.join(['\"k%d\":1' % i for i in range(10000)]) + '}'\nr = json.decode(s)"},
		{"decode/with default", "s = '[' + '1,' * 100000 + '1]'\nr = json.decode(s, 5)"}, // not swallowed as a syntax error
	} {
		r := exec(t, budget1MiB, c.src)
		wantBudgetError(t, c.name, r, budget1MiB)
	}
}

// Without a budget, the ceiling of one operation (1<<30 bytes) applies: an
// indent whose output is terabytes is refused by arithmetic, before any
// allocation.
func TestJSON_IndentCeilingWithoutBudget(t *testing.T) {
	r := exec(t, 0, "s = '[' * 5000 + ']' * 5000\nind = 'x' * (1 << 20)\nmark()\nr = json.indent(s, indent=ind)")
	if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
		t.Fatalf("err = %v", r.err)
	}
	var be *starlark.AllocBudgetError
	if errors.As(r.err, &be) {
		t.Fatalf("budget error without a budget: %v", r.err)
	}
	if r.th.AllocatedBytes() != r.marks[0] {
		t.Fatalf("a refused indent was charged: %d, %d", r.th.AllocatedBytes(), r.marks[0])
	}
}

func TestJSON_DecodeStillReturnsDefaultOnSyntaxError(t *testing.T) {
	r := exec(t, budget1MiB, "r = json.decode('[1,', 5)\nif r != 5: fail('no default')\ns = json.decode('{\"a\": [1, {\"b\": null}]}')\nif s != {'a': [1, {'b': None}]}: fail('wrong value')")
	if r.err != nil {
		t.Fatal(r.err)
	}
}

func TestJSON_RoundTripWithinBudget(t *testing.T) {
	src := `
x = {"a": [1, 2, {"b": "c"}], "d": None, "e": 1.5}
s = json.encode(x)
y = json.decode(s)
if y != x: fail("round trip")
if json.indent(s) != json.encode_indent(x, prefix="", indent="\t"): fail("indent")
`
	if r := exec(t, budget1MiB, src); r.err != nil {
		t.Fatal(r.err)
	}
}
