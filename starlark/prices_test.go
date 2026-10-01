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
