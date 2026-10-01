package starlark

// "Idiom, and the size at which it is refused at 1 GiB": the programs that a
// handler writes, run until the allocation budget of 1 GiB (the engine's) says
// no, and the iteration it said it at. The counter is monotonic (it counts what
// the thread allocated, garbage too), so an idiom that builds a value by copying
// it is refused when the copies add up to the budget, not when the value does.
//
//	STARLARK_IDIOMS=1 go test -run TestBudgetIdioms -v ./starlark/

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"go.starlark.net/syntax"
)

type idiom struct {
	name  string
	setup string
	body  string // the loop body, with i
	n     int    // the iterations to try at most
}

var idioms = []idiom{
	{"s += chunk of 50 bytes", "s = ''\nc = 'x' * 50", "s += c", 1000000},
	{"s += chunk of 1 KB", "s = ''\nc = 'x' * 1000", "s += c", 1000000},
	{"s = s + chunk of 50 bytes", "s = ''\nc = 'x' * 50", "s = s + c", 1000000},
	{"l = l + [i]", "l = []", "l = l + [i]", 1000000},
	{"l += [i]", "l = []", "l += [i]", 1000000},
	{"l.append(i)", "l = []", "l.append(i)", 1000000},
	{"l.extend([i, i])", "l = []", "l.extend([i, i])", 1000000},
	{"d = dict(d); d[i] = i", "d = {}", "d = dict(d)\nd[i] = i", 1000000},
	{"d = d | {i: i}", "d = {}", "d = d | {i: i}", 1000000},
	{"d[i] = i", "d = {}", "d[i] = i", 1000000},
	{"d.update({i: i})", "d = {}", "d.update({i: i})", 1000000},
	{"s = set(s); s.add(i)", "s = set()", "s = set(s)\ns.add(i)", 1000000},
	{"json.encode(state of 100 keys)", "st = {'k%d' % i: i for i in range(100)}", "r = json.encode(st)", 1000000},
	{"json.decode(state of 100 keys)", "st = json.encode({'k%d' % i: i for i in range(100)})", "r = json.decode(st)", 1000000},
	{"','.join(1000 parts of 50 bytes)", "parts = ['x' * 50] * 1000", "r = ','.join(parts)", 1000000},
	{"'%s' % a string of 1 KB", "c = 'x' * 1000", "r = '%s!' % c", 1000000},
	{"str(list of 100 ints)", "l = list(range(100))", "r = str(l)", 1000000},
	{"[x for x in 1000 elements]", "l = list(range(1000))", "r = [x for x in l]", 1000000},
	{"l[:] of 1000 elements", "l = list(range(1000))", "r = l[:]", 1000000},
	{"sorted(1000 elements)", "l = list(range(1000))", "r = sorted(l)", 1000000},
	{"list(d.items()) of 100", "d = {i: i for i in range(100)}", "r = list(d.items())", 1000000},
	{"s.split(',') of 1000 fields", "s = 'ab,' * 1000", "r = s.split(',')", 1000000},
	{"s.replace on 1 KB", "s = 'ab' * 500", "r = s.replace('a', 'xy')", 1000000},
	{"x = x + 1 (integer of 4000 bits)", "x = 1 << 500\nx = x * x * x * x", "x = x + 1", 4000000},
	{"x = x + 1 (integer of 40 bits)", "x = 1 << 40", "x = x + 1", 4000000},
	{"{} literal", "", "r = {}", 1000000},
	{"[] literal", "", "r = []", 1000000},
	{"def call with 5 params", "def f(a, b, c, d, e): return a", "f(i, 2, 3, 4, 5)", 1000000},
	{"call with *args, **kwargs", "def f(*a, **k): return 1", "f(1, 2, x=3)", 1000000},
}

// idiomRefusedAt runs the idiom with a budget of 1 GiB and returns the
// iteration it was refused at (-1: it was not in n iterations), and the bytes
// it was charged for each iteration.
func idiomRefusedAt(t *testing.T, id idiom, budget uint64, extra StringDict) (at int, perIter float64) {
	th := &Thread{Name: "idiom"}
	th.SetMaxAllocBytes(budget)
	n := 0
	extra2 := StringDict{"trace": NewBuiltin("trace", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		n++
		return None, nil
	})}
	for k, v := range extra {
		extra2[k] = v
	}
	src := id.setup + fmt.Sprintf("\nfor i in range(%d):\n    trace()\n", id.n)
	for _, line := range splitLines(id.body) {
		src += "    " + line + "\n"
	}
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, extra2)
	var be *AllocBudgetError
	if err != nil && !errors.As(err, &be) {
		t.Fatalf("%s: %v", id.name, err)
	}
	if err == nil {
		return -1, float64(th.AllocatedBytes()) / float64(id.n)
	}
	return n, float64(th.AllocatedBytes()) / float64(max(n, 1))
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestBudgetIdioms(t *testing.T) {
	if os.Getenv("STARLARK_IDIOMS") == "" {
		t.Skip("set STARLARK_IDIOMS=1 to print the table")
	}
	for _, id := range idioms {
		// (json is in another package: the idioms that use it are left to its test)
		if len(id.setup) >= 4 && (id.name[:4] == "json") {
			continue
		}
		at, per := idiomRefusedAt(t, id, 1<<30, nil)
		if at < 0 {
			fmt.Printf("IDIOM %-36s not refused in %d iterations (%.0f B an iteration)\n", id.name, id.n, per)
		} else {
			fmt.Printf("IDIOM %-36s refused at iteration %8d (%.0f B an iteration)\n", id.name, at, per)
		}
	}
}
