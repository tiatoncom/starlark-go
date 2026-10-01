package starlark

// The main prices, one test each, in units of work counted: the work an
// operation was charged beyond its steps is at least a function of the size of
// its operand, and grows with it (a price that is dropped, or divided, is a red
// test), and, where the elementary operations can be counted (a value that
// counts the comparisons, hashes and truth tests done on it), the work is at
// least the count of them: an operation cannot do more than it was charged.

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

// extraWork runs src (with the counted values) and returns the work charged
// between reset() and the end, beyond the steps of the opcodes in it.
func extraWork(t *testing.T, src string) (extra uint64, cmps, hashes uint64) {
	t.Helper()
	th := &Thread{}
	var w0, s0 uint64
	extra2 := countedBuiltins()
	extra2["miss"] = NewBuiltin("miss", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) { return counted{-1}, nil })
	extra2["reset"] = NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		resetCounts()
		w0, s0 = th.Work(), th.Steps
		return None, nil
	})
	if _, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "t.star", src, extra2); err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return (th.Work() - w0) - (th.Steps - s0), cmpCount.Load(), hashCount.Load()
}

type pointCase struct {
	name string
	prog string // with N, and reset() before the operation
	// the work beyond the steps is at least num/den of N (or of the count)
	num, den uint64
}

func TestPricePoints_WorkIsAtLeastAFunctionOfTheOperand(t *testing.T) {
	const n = 20000
	cases := []pointCase{
		{"x in list: a unit an element", "l = mk(N)\nreset()\nr = miss() in l", 1, 1},
		{"l.index: a unit an element", "l = mk(N)\nx = leaf()\nreset()\nr = l.index(mk(1)[0]) if False else 0\nr = (l + [0]).index(0)", 1, 1},
		{"l == m: a unit a pair", "l = mk(N)\nm = mk(N)\nreset()\nr = l == m", 1, 1},
		{"l < m: a unit a pair", "l = mk(N)\nm = mk(N)\nreset()\nr = l < m", 1, 1},
		{"max(l): two units an element", "l = mk(N)\nreset()\nr = max(l)", 2, 1},
		{"d == e: eight units an entry", "d = dict(zip(range(N), range(N)))\ne = dict(zip(range(N), range(N)))\nreset()\nr = d == e", 8, 1},
		{"set(l): eight units an element", "l = list(range(N))\nreset()\nr = set(l)", 8, 1},
		{"dict(d): eight units an entry", "d = dict(zip(range(N), range(N)))\nreset()\nr = dict(d)", 8, 1},
		{"l.insert(0, x): a slot is an eighth of a unit", "l = list(range(N))\nreset()\nl.insert(0, 1)", 1, 8},
		{"l.pop(0): a slot is an eighth of a unit", "l = list(range(N))\nreset()\nl.pop(0)", 1, 8},
		{"[0] * N: a unit a slot (16 bytes)", "reset()\nr = [0] * N", 1, 1},
		{"l + l: a unit a slot", "l = list(range(N))\nreset()\nr = l + l", 2, 1},
		{"list(l): a unit a slot", "l = list(range(N))\nreset()\nr = list(l)", 1, 1},
		{"s[::2]: an eighth of a unit a byte of the result", "s = 'a' * (2 * N)\nreset()\nr = s[::2]", 1, 8},
		{"s.find, a needle of 100 bytes: an eighth a byte", "s = 'a' * N\nreset()\nr = s.find('a' * 100 + 'b')", 1, 8},
		{"s.count, a needle of 100 bytes", "s = 'a' * N\nreset()\nr = s.count('a' * 100 + 'b')", 1, 8},
		{"s.replace, a needle of 100 bytes", "s = 'a' * N\nreset()\nr = s.replace('a' * 100 + 'b', 'x')", 1, 8},
		{"x in s, a needle of 100 bytes", "s = 'a' * N\nreset()\nr = ('a' * 100 + 'b') in s", 1, 8},
		{"s.split, a needle of 100 bytes", "s = 'a' * N\nreset()\nr = s.split('a' * 100 + 'b')", 1, 4},
		{"s.upper(): a fourth of a unit a byte", "s = 'a' * N\nreset()\nr = s.upper()", 1, 4},
		{"s.title()", "s = 'a' * N\nreset()\nr = s.title()", 1, 4},
		{"repr(s): a fourth a byte", "s = 'a' * N\nreset()\nr = repr(s)", 1, 8},
		{"str(l): four units an element", "l = list(range(N))\nreset()\nr = str(l)", 4, 1},
		{"join: a unit an element", "l = ['ab'] * N\nreset()\nr = ','.join(l)", 1, 1},
		{"s.split(','): four units a field", "s = 'a,' * N\nreset()\nr = s.split(',')", 4, 1},
		{"list(s.codepoints()): six units an element", "s = 'a' * N\nreset()\nr = list(s.codepoints())", 6, 1},
		{"enumerate(l): six units an element", "l = list(range(N))\nreset()\nr = enumerate(l)", 5, 1},
		{"zip(l, l)", "l = list(range(N))\nreset()\nr = zip(l, l)", 5, 1},
		{"lstrip with a cutset: a unit a character of it, and one for two trimmed", "a = 'a' * N\nc = 'é' * (N // 10) + 'a'\nreset()\nr = a.lstrip(c)", 1, 10},
		{"strip(), whitespace: a unit for two characters", "a = ' ' * N + 'x'\nreset()\nr = a.strip()", 1, 2},
		{"x < 1.5 on a big integer: constant", "x = 1 << 500\nreset()\nr = x < 1.5", 0, 1},
	}
	for _, c := range cases {
		prog := strings.ReplaceAll(c.prog, "N", fmt.Sprint(n))
		extra, _, _ := extraWork(t, prog)
		want := uint64(n) * c.num / c.den
		if extra < want {
			t.Errorf("%s: %d units of work beyond the steps, want at least %d for N = %d", c.name, extra, want, n)
		}
		// and it grows with the operand: 4 times the operand is not less than
		// 3 times the work (the constant overhead of the statements is small)
		if c.num > 0 {
			prog4 := strings.ReplaceAll(c.prog, "N", fmt.Sprint(4*n))
			extra4, _, _ := extraWork(t, prog4)
			if extra4 < 3*extra {
				t.Errorf("%s: %d units for 4N, %d for N: the price does not grow with the operand", c.name, extra4, extra)
			}
		}
	}
}

// A comparison cannot be done without being charged: the work is at least the
// count of comparisons, hashes and truth tests that were made on the values.
func TestPricePoints_WorkIsAtLeastTheCountedOperations(t *testing.T) {
	for _, c := range []struct{ name, prog string }{
		{"sorted of random counted values", "l = mk(5000)\nl = sorted(l, reverse=True)\nreset()\nr = sorted(l)"},
		{"sorted: descending", "l = reversed(mk(5000))\nreset()\nr = sorted(l)"},
		{"x in l", "l = mk(20000)\nreset()\nr = miss() in l"},
		{"l == m", "l = mk(20000)\nm = mk(20000)\nreset()\nr = l == m"},
		{"max", "l = mk(20000)\nreset()\nr = max(l)"},
		{"set(l)", "l = mk(5000)\nreset()\nr = set(l)"},
		{"dict(zip)", "l = mk(5000)\nreset()\nr = dict(zip(l, l))"},
		{"s | t", "s = set(mk(5000))\nt = set(mk(5000, 5000))\nreset()\nr = s | t"},
		{"d == e", "d = dict(zip(mk(3000), mk(3000)))\ne = dict(zip(mk(3000), mk(3000)))\nreset()\nr = d == e"},
		{"nested ==", "l = [mk(100)] * 100\nm = [mk(100)] * 100\nreset()\nr = l == m"},
	} {
		extra, cmps, hashes := extraWork(t, c.prog)
		ops := cmps + hashes
		if extra < ops {
			t.Errorf("%s: %d comparisons and hashes were done, %d units of work were charged for them", c.name, ops, extra)
		}
	}
}
