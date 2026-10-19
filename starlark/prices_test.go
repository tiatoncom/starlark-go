package starlark

// The closed list of prices: every built-in and operator has one, and the ones
// that are declared constant are.

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

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
	for k, p := range operatorPrices {
		if p.desc == "" || (!p.o1 && !p.inside && p.work == nil) {
			t.Errorf("operator price %q declares nothing", k)
		}
	}
	// The keys are an exact set: a row missing and a row extra both fail.
	for _, problem := range operatorKeyProblems(operatorPrices) {
		t.Error(problem)
	}
	for _, tok := range []syntax.Token{syntax.PLUS, syntax.MINUS, syntax.STAR, syntax.SLASH, syntax.SLASHSLASH, syntax.PERCENT,
		syntax.EQL, syntax.NEQ, syntax.LT, syntax.LE, syntax.GT, syntax.GE, syntax.IN, syntax.PIPE, syntax.AMP,
		syntax.CIRCUMFLEX, syntax.LTLT, syntax.GTGT, syntax.TILDE} {
		if _, ok := operatorOfToken[tok]; !ok {
			t.Errorf("operator %s has no row in operatorOfToken", tok)
		}
	}

	// The check itself is not vacuous: a row deleted, a row added and a row
	// renamed in a scratch copy of the table are each a problem.
	scratch := func(edit func(m map[string]price)) map[string]price {
		m := make(map[string]price, len(operatorPrices)+1)
		for k, p := range operatorPrices {
			m[k] = p
		}
		edit(m)
		return m
	}
	for _, k := range declaredOperatorKeys {
		deleted := scratch(func(m map[string]price) { delete(m, k) })
		if len(operatorKeyProblems(deleted)) == 0 {
			t.Errorf("deleting the row %q is not detected", k)
		}
	}
	added := scratch(func(m map[string]price) { m["x ?? y"] = o1("an operator that does not exist") })
	if len(operatorKeyProblems(added)) == 0 {
		t.Error("an extra row is not detected")
	}
	renamed := scratch(func(m map[string]price) {
		k := declaredOperatorKeys[0]
		m[k+" "] = m[k]
		delete(m, k)
	})
	if len(operatorKeyProblems(renamed)) == 0 {
		t.Error("a renamed row is not detected")
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

// declaredOperatorKeys is the exact set of the rows of operatorPrices: every
// operation the interpreter has, once. A row added or removed in prices.go is
// a change here, on purpose.
var declaredOperatorKeys = []string{
	"load/store/constant/jump/call (opcodes)",
	"x + y (int, float)",
	"x + y (string, bytes)",
	"x + y (list, tuple)",
	"x * n (string, list, tuple)",
	"x * y, x // y, x % y (big int)",
	"x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	"x % y (string interpolation)",
	"x in l (list, tuple)",
	"x in s (string, bytes)",
	"x in d (dict), x in s (set)",
	"x in r (range)",
	"x == y, x != y, x < y, x <= y, x > y, x >= y (string)",
	"x == y, x != y, x < y, x <= y, x > y, x >= y (list, tuple)",
	"x == y, x != y (dict)",
	"x == y, x != y, x <= y, x < y, x >= y, x > y (set)",
	"x == y, x != y, x < y, x > y (int, float, bool, None)",
	"x[i] (list, tuple, string, range)",
	"x[k] (dict)",
	"x[i:j:k] (list, tuple, string, bytes)",
	"x[i] = v (list)",
	"x[k] = v (dict)",
	"d1 | d2, d1 |= d2",
	"s1 | s2, s1 & s2, s1 - s2, s1 ^ s2",
	"-x, +x, ~x, not x",
	"for x in l (iteration)",
	"a, b = l (unpacking)",
	"f(*l), f(**d)",
	"x.attr, x.method",
	"list comprehension, dict comprehension",
	"{k: v}, [a, b], (a, b), lambda",
	"def f(*args, **kwargs) (parameters)",
	"hash of a string",
	"hash of a tuple",
	"hash of an int",
	"freeze",
	"json.encode, json.decode, json.indent",
}

// operatorOfToken says which row prices each token of the binary and unary
// operators of the syntax.
var operatorOfToken = map[syntax.Token]string{
	syntax.PLUS:       "x + y (int, float)",
	syntax.MINUS:      "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.STAR:       "x * y, x // y, x % y (big int)",
	syntax.SLASH:      "x * y, x // y, x % y (big int)",
	syntax.SLASHSLASH: "x * y, x // y, x % y (big int)",
	syntax.PERCENT:    "x * y, x // y, x % y (big int)",
	syntax.EQL:        "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.NEQ:        "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.LT:         "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.LE:         "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.GT:         "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.GE:         "x == y, x != y, x < y, x > y (int, float, bool, None)",
	syntax.IN:         "x in l (list, tuple)",
	syntax.PIPE:       "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.AMP:        "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.CIRCUMFLEX: "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.LTLT:       "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.GTGT:       "x - y, x & y, x | y, x ^ y, x << y, x >> y (int)",
	syntax.TILDE:      "-x, +x, ~x, not x",
}

// operatorKeyProblems compares the keys of a table of operator prices with the
// declared set both ways, and checks that the row of every token is there.
func operatorKeyProblems(have map[string]price) []string {
	var problems []string
	want := map[string]bool{}
	for _, k := range declaredOperatorKeys {
		want[k] = true
		if _, ok := have[k]; !ok {
			problems = append(problems, fmt.Sprintf("the operator price %q is declared but has no row", k))
		}
	}
	for k := range have {
		if !want[k] {
			problems = append(problems, fmt.Sprintf("the row %q is not in the declared set of operators", k))
		}
	}
	for tok, k := range operatorOfToken {
		if !want[k] {
			problems = append(problems, fmt.Sprintf("operator %s: its row %q is not declared", tok, k))
		}
		if _, ok := have[k]; !ok {
			problems = append(problems, fmt.Sprintf("operator %s has no price (row %q)", tok, k))
		}
	}
	return problems
}

// A price that is declared constant is constant: the call costs the same work
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
		"dict.clear":            "d.clear()",
		"set.clear":             "st.clear()",
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
	work := func(n int, call string) uint64 {
		src := fmt.Sprintf("N = %d\nx = [0] * N\ns = 'a' * N\nd = {i: i for i in range(N)}\nst = set(range(N))\nb = b'a' * N\nmark()\nr = %s\nmark()\n", n, strings.ReplaceAll(call, "N)", "N)"))
		var marks []uint64
		th := &Thread{}
		mark := NewBuiltin("mark", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
			marks = append(marks, th.Work())
			return None, nil
		})
		if _, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, StringDict{"mark": mark}); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
		return marks[1] - marks[0]
	}
	for name, call := range calls {
		small, large := work(100, call), work(10000, call)
		if small != large {
			t.Errorf("%s (%s): %d units of work for n=100, %d for n=10000", name, call, small, large)
		}
	}
}
