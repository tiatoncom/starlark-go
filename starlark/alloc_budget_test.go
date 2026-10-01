package starlark

// These tests cover the allocation accounting of a Thread (alloc.go): the
// byte budget, the ceiling of one operation, and the charging of every
// "amplifier" (an operation whose result is large relative to the interpreter
// steps it costs). They use no clock, and no test allocates more than a few
// MiB: an operation that must be refused is refused by arithmetic before it
// allocates, and the tests that exercise the ceiling lower maxAlloc.

import (
	"errors"
	"math"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

const (
	budgetMiB    = 1 << 20
	budgetPrefix = "starlark: allocation budget exhausted: "
)

type progRun struct {
	th      *Thread
	err     error
	marks   []uint64 // AllocatedBytes at each call of mark()
	traced  []int    // arguments of trace()
	printed int      // number of print calls
	memMark uint64   // runtime TotalAlloc at the first mark()
	memEnd  uint64   // runtime TotalAlloc when the program ended
}

// runProg executes src on a fresh thread with the given budget (0: none).
// The program may call mark(), which records the bytes charged so far, and
// trace(i), which records an integer.
func runProg(t *testing.T, budget uint64, src string) *progRun {
	t.Helper()
	return runProgWith(t, budget, src, nil)
}

// runProgWith is runProg with more predeclared names.
func runProgWith(t *testing.T, budget uint64, src string, extra StringDict) *progRun {
	t.Helper()
	r := &progRun{th: &Thread{Name: "t"}}
	r.th.Print = func(*Thread, string) { r.printed++ }
	r.th.SetMaxAllocBytes(budget)
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
		"trace": NewBuiltin("trace", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
			var i int
			if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &i); err != nil {
				return nil, err
			}
			r.traced = append(r.traced, i)
			return None, nil
		}),
	}
	predeclared["struct_like"] = NewBuiltin("struct_like", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		return attrHolder{NewList(make([]Value, 1000))}, nil
	})
	for k, v := range extra {
		predeclared[k] = v
	}
	opts := &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}
	_, r.err = ExecFileOptions(opts, r.th, "t.star", src, predeclared)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.memEnd = ms.TotalAlloc
	return r
}

// withN substitutes the size parameter {N} of a program template.
func withN(src string, n int) string { return strings.ReplaceAll(src, "{N}", strconv.Itoa(n)) }

func wantBudgetErr(t *testing.T, r *progRun, budget uint64) *AllocBudgetError {
	t.Helper()
	if r.err == nil {
		t.Fatalf("operation succeeded; want a budget error (allocated %d of %d)", r.th.AllocatedBytes(), budget)
	}
	var be *AllocBudgetError
	if !errors.As(r.err, &be) {
		t.Fatalf("error is not an *AllocBudgetError: %v", r.err)
	}
	if !strings.HasPrefix(r.err.Error(), budgetPrefix) {
		t.Fatalf("error text does not begin with %q: %q", budgetPrefix, r.err.Error())
	}
	if be.Limit != budget {
		t.Fatalf("error limit = %d, want %d", be.Limit, budget)
	}
	if got := r.th.AllocatedBytes(); got > budget {
		t.Fatalf("AllocatedBytes() = %d exceeds the budget %d", got, budget)
	}
	return be
}

// ---- (b) within the budget: charged exactly by the formula ----

// The formulas of alloc.go, written out: a List is 48 + 16n, a Tuple 32 + 16n,
// a Dict or Set 512 + 128 per entry past the 7 the inline bucket holds, an
// item (a pair in a list) 80, a row of zip 16 + 32 + 16 per column.
func lb(n int) uint64 { return 48 + 16*uint64(n) }
func tb(n int) uint64 { return 32 + 16*uint64(n) }
func db(n int) uint64 { return 512 + 128*uint64(max(n-7, 0)) }

// Each case runs "setup", then op, and the bytes charged by op alone are
// compared with want, which is written out from the formulas of alloc.go.
// Operands are made in the setup: a literal in op is charged too.
var chargeCases = []struct {
	name  string
	setup string
	op    string
	want  uint64
}{
	// repeat: len * n bytes, 16 * len * n for lists and tuples
	{"str*n", "s = 'abc'", "r = s * 10", 30},
	{"n*str", "s = 'abc'", "r = 10 * s", 30},
	{"str*1 is the same string", "s = 'abc'", "r = s * 1", 0},
	{"bytes*n", "s = b'abc'", "r = s * 10", 30},
	{"list*n", "s = [1, 2, 3]", "r = s * 10", lb(30)},
	{"tuple*n", "s = (1, 2, 3)", "r = s * 10", tb(30)},
	// concatenation
	{"str+str", "s = 'abc'; t = 'defg'", "r = s + t", 7},
	{"list+list", "s = [1, 2, 3]; t = [4, 5]", "r = s + t", lb(5)},
	{"tuple+tuple", "s = (1, 2, 3); t = (4, 5)", "r = s + t", tb(5)},
	// list growth by copying existing elements
	{"list+=list", "s = [1, 2, 3]; t = [4, 5]", "s += t", 2 * 16},
	{"list+=tuple", "s = [1, 2, 3]; t = (4, 5, 6, 7)", "s += t", 4 * 16},
	{"list.extend(list)", "s = [1, 2, 3]; t = [4, 5]", "s.extend(t)", 2 * 16},
	{"list.extend(range)", "s = [1, 2, 3]", "s.extend(range(5))", 5 * 16},
	// string forms
	{"%", "a = ('ab', 'cd')", "r = '%s-%s' % a", 5},
	{"%r", "a = ('ab',)", "r = '%r' % a", 4},
	{"str(list)", "x = [1, 2, 3]", "r = str(x)", 9},
	{"str(bytes) of a valid string allocates nothing", "x = b'abc'", "r = str(x)", 0},
	{"str(bytes) of an invalid string", "x = b'\\xff'", "r = str(x)", 3},
	{"str(str) is the same string", "x = 'abc'", "r = str(x)", 0},
	{"repr(list)", "x = [1, 2, 3]", "r = repr(x)", 9},
	{"print", "", "print('abc', 'de')", 6},
	// strings
	{"replace", "s = 'aaa'", "r = s.replace('a', 'bb')", 6},
	{"replace/shrink", "s = 'aaaa'", "r = s.replace('aa', 'b')", 2},
	{"replace/count", "s = 'aaaa'", "r = s.replace('a', 'bb', 2)", 6},
	{"replace/empty", "s = 'abc'", "r = s.replace('', '-')", 7},
	{"replace/unchanged allocates nothing", "s = 'abc'", "r = s.replace('a', 'a')", 0},
	{"replace/no match allocates nothing", "s = 'abc'", "r = s.replace('x', 'yy')", 0},
	{"replace/count 0 allocates nothing", "s = 'abc'", "r = s.replace('a', 'yy', 0)", 0},
	{"join", "p = ['ab', 'cd', 'ef']", "r = ','.join(p)", 8},
	{"join/tuple", "p = ('ab', 'cd')", "r = '-'.join(p)", 5},
	{"join/dict collects the keys first", "d = {'ab': 1, 'cd': 2}", "r = '-'.join(d)", 2 * 16},
	{"format", "", "r = '{}-{}'.format('ab', 'cd')", 5},
	{"format/r", "", "r = '{!r}'.format('ab')", 4},
	{"upper", "s = 'abc'", "r = s.upper()", 3},
	{"upper of an uppercase string allocates nothing", "s = 'ABC'", "r = s.upper()", 0},
	{"lower", "s = 'ABC'", "r = s.lower()", 3},
	{"lower of a lowercase string allocates nothing", "s = 'abc'", "r = s.lower()", 0},
	{"title", "s = 'abc'", "r = s.title()", 3},
	{"capitalize", "s = 'abc'", "r = s.capitalize()", 3},
	{"split(sep)", "s = 'a,b,c'", "r = s.split(',')", lb(3) + 3*16},
	{"split(sep, max)", "s = 'a,b,c'", "r = s.split(',', 1)", lb(2) + 2*16},
	{"split()", "s = ' a b  c '", "r = s.split()", lb(3) + 3*16},
	{"rsplit(sep, max) splits all, then joins", "s = 'a,b,c'", "r = s.rsplit(',', 1)", lb(3) + 3*16},
	{"rsplit(None, max)", "s = 'a b c'", "r = s.rsplit(None, 1)", lb(2) + 2*16},
	{"splitlines", "s = 'a\\nb\\nc'", "r = s.splitlines()", lb(3) + 3*16},
	{"bytes(valid str) allocates nothing", "s = 'abc'", "r = bytes(s)", 0},
	{"bytes(invalid str)", "s = 'é'[:1]", "r = bytes(s)", 3},
	{"bytes(list)", "s = [1, 2, 3]", "r = bytes(s)", 3},
	// containers from iterables
	{"list(range)", "", "r = list(range(10))", lb(10)},
	{"list(list)", "x = [1, 2, 3]", "r = list(x)", lb(3)},
	{"list()", "", "r = list()", lb(0)},
	{"tuple(range)", "", "r = tuple(range(10))", tb(10)},
	{"tuple()", "", "r = tuple()", 0},
	{"set(range)", "", "r = set(range(10))", 512 + 10*128},
	{"set()", "", "r = set()", db(0)},
	{"dict()", "", "r = dict()", db(0)},
	{"dict(pairs)", "x = [(1, 2), (3, 4)]", "r = dict(x)", 512 + 2*128},
	{"dict(dict)", "x = {1: 2, 3: 4, 5: 6}", "r = dict(x)", 512 + 3*128},
	{"sorted", "x = [3, 1, 2]", "r = sorted(x)", lb(3)},
	{"sorted/key", "x = [3, 1, 2]\ndef k(v): return -v", "r = sorted(x, key=k)", lb(3) + lb(3)},
	{"reversed", "x = [1, 2, 3]", "r = reversed(x)", lb(3)},
	{"zip", "x = [1, 2, 3]; y = [4, 5, 6]", "r = zip(x, y)", lb(0) + 3*(16+32+2*16)},
	{"enumerate", "x = [1, 2, 3]", "r = enumerate(x)", lb(0) + 3*80},
	// slices
	{"list[:]", "x = [1, 2, 3, 4]", "r = x[:]", lb(4)},
	{"list[1:3]", "x = [1, 2, 3, 4]", "r = x[1:3]", lb(2)},
	{"list[::2]", "x = [1, 2, 3, 4, 5]", "r = x[::2]", lb(3)},
	{"list[::-1]", "x = [1, 2, 3, 4, 5]", "r = x[::-1]", lb(5)},
	{"tuple[::2]", "x = (1, 2, 3, 4, 5)", "r = x[::2]", tb(3)},
	{"tuple[:] shares memory", "x = (1, 2, 3, 4, 5)", "r = x[:]", 0},
	{"str[::2]", "x = 'abcdef'", "r = x[::2]", 3},
	{"str[1:4] shares memory", "x = 'abcdef'", "r = x[1:4]", 0},
	{"bytes[::-1]", "x = b'abcdef'", "r = x[::-1]", 6},
	{"range[:] is lazy", "x = range(100)", "r = x[10:20]", 0},
	// dict and set
	{"dict.items", "d = {'a': 1, 'b': 2}", "r = d.items()", lb(0) + 2*80},
	{"dict.keys", "d = {'a': 1, 'b': 2}", "r = d.keys()", lb(2)},
	{"dict.values", "d = {'a': 1, 'b': 2}", "r = d.values()", lb(0) + 2*80},
	{"dict|dict", "a = {'a': 1, 'b': 2}; b = {'c': 3}", "r = a | b", db(3)},
	{"dict|=dict", "a = {'a': 1, 'b': 2}; b = {'c': 3}", "a |= b", 1 * 128},
	{"dict.update(dict)", "a = {'a': 1}; b = {'a': 2, 'b': 3, 'c': 4}", "a.update(b)", 3 * 128},
	{"dict.update(pairs)", "a = {'a': 1}; b = [('b', 2), ('c', 3)]", "a.update(b)", 2 * 128},
	{"set|set", "a = set([1, 2, 3]); b = set([3, 4])", "r = a | b", db(5)},
	{"set&set", "a = set([1, 2, 3]); b = set([3, 4])", "r = a & b", db(2)},
	{"set-set", "a = set([1, 2, 3]); b = set([3, 4])", "r = a - b", db(3)},
	{"set^set", "a = set([1, 2, 3]); b = set([3, 4])", "r = a ^ b", db(5)},
	{"set.union", "a = set([1, 2, 3]); b = set([3, 4])", "r = a.union(b)", db(5)},
	{"set.intersection", "a = set([1, 2, 3]); b = set([3, 4])", "r = a.intersection(b)", db(2)},
	{"set.difference", "a = set([1, 2, 3]); b = set([3, 4])", "r = a.difference(b)", db(3)},
	{"set.symmetric_difference", "a = set([1, 2, 3]); b = set([3, 4])", "r = a.symmetric_difference(b)", db(5)},
	{"set.update", "a = set([1, 2, 3]); b = [3, 4]", "a.update(b)", 2 * 128},
	// literals and creation: the base of every container, and a function
	{"[]", "", "r = []", lb(0)},
	{"[1, 2, 3]", "", "r = [1, 2, 3]", lb(3)},
	{"{}", "", "r = {}", db(0)},
	{"{1: 2}", "", "r = {1: 2}", db(1)},
	{"(a, b)", "a = 1", "r = (a, a)", tb(2)},
	{"comprehension [x for ...]", "", "r = [i for i in range(3)]", lb(0)},
	{"dict comprehension", "", "r = {i: i for i in range(3)}", db(0)},
	{"lambda", "", "r = lambda: 1", 96},
	{"def", "", "def f(): return 1", 96},
	// entries added in place: a dict or set entry past the inline bucket
	{"d[k] = v, first 7 entries are free", "d = {}", "d[1] = 1", 0},
	{"d[k] = v past the inline bucket", "d = {}\nfor i in range(7): d[i] = i", "d[100] = 1", 128},
	{"d[k] = v replacing a value is free", "d = {}\nfor i in range(9): d[i] = i", "d[3] = 0", 0},
	{"setdefault past the inline bucket", "d = {}\nfor i in range(7): d[i] = i", "d.setdefault(100, 1)", 128},
	{"set.add past the inline bucket", "s = set(range(7))", "s.add(100)", 128},
	{"set.add of an element already in is free", "s = set(range(9))", "s.add(3)", 0},
	{"append is not charged", "x = []", "x.append(1)", 0},
	// parameters of a function: *args is a new tuple, **kwargs a new dict
	{"f(*x)", "x = [1, 2, 3]\ndef f(*a): return None", "f(*x)", 3*16 + tb(3)},
	{"f(a, b) into *args", "def f(*a): return None", "f(1, 2, 3)", tb(3)},
	{"f(**d)", "d = {'a': 1, 'b': 2}\ndef f(**k): return None", "f(**d)", 2*80 + 512},
	{"f(a=1) into **kwargs", "def f(**k): return None", "f(a=1)", 512},
	// big integers: the digits of both operands
	{"bigint*bigint", "x = 1 << 500", "r = x * x", 2 * 63},
	{"bigint*int", "x = 1 << 500", "r = x * 3", 63},
	{"int*int is free", "x = 1 << 20", "r = x * 3", 0},
	// built-ins that return a value that already exists are not charged
	{"len", "x = [1, 2, 3]", "r = len(x)", 0},
	{"strip returns a substring", "s = ' abc '", "r = s.strip()", 0},
	{"removeprefix returns a substring", "s = 'abcd'", "r = s.removeprefix('ab')", 0},
	{"dict.get returns an existing value", "d = {'a': [1, 2, 3]}", "r = d.get('a')", 0},
	{"dict.pop returns an existing value", "d = {'a': [1, 2, 3]}", "r = d.pop('a')", 0},
	{"list.pop returns an existing value", "x = [[1, 2, 3]]", "r = x.pop()", 0},
	{"min returns an existing value", "x = [[1], [2, 3]]", "r = min(x)", 0},
	{"max returns an existing value", "x = ['a', 'bbb']", "r = max(x)", 0},
	{"getattr returns an existing value", "s = struct_like()", "r = getattr(s, 'v')", 0},
	{"type", "x = [1, 2, 3]", "r = type(x)", 0},
	// the built-ins that make something new are charged by what they make
	{"partition", "s = 'a,b'", "r = s.partition(',')", tb(3)},
	{"dict.popitem", "d = {'a': 1}", "r = d.popitem()", tb(2)},
	{"dir", "x = [1]", "r = dir(x)", lb(len(listMethods))},
}

func TestAllocCharge_Exact(t *testing.T) {
	for _, c := range chargeCases {
		for _, budget := range []uint64{0, 1 << 30} { // counted with and without a budget
			src := c.setup + "\nmark()\n" + c.op + "\nmark()\n"
			r := runProg(t, budget, src)
			if r.err != nil {
				t.Errorf("%s (budget %d): %v", c.name, budget, r.err)
				continue
			}
			if len(r.marks) != 2 {
				t.Fatalf("%s: marks = %v", c.name, r.marks)
			}
			if got := r.marks[1] - r.marks[0]; got != c.want {
				t.Errorf("%s (budget %d): charged %d bytes, want %d", c.name, budget, got, c.want)
			}
		}
	}
}

// ---- (a) over the budget, (c) over the ceiling ----

// Each case is a program template whose size parameter {N} is chosen so that
// the operation's result exceeds the budget (nBudget) or the ceiling
// (nCeil, with maxAlloc lowered to 64 KiB); the operands themselves stay
// below both, and are made before the mark. An operation that is refused must
// allocate nothing and charge nothing.
var refuseCases = []struct {
	name    string
	src     string
	nBudget int
	ceil    string // source for the ceiling test; "" means src
	nCeil   int    // 0: the result cannot exceed the ceiling without an operand doing so
	// late: the operation builds its result up to the limit of the thread
	// before it is refused (a string form cannot be sized in advance); every
	// other operation is refused before it allocates the result.
	late bool
}{
	{name: "str*n", src: "s = 'abc'\nmark()\nr = s * {N}", nBudget: 400000, nCeil: 22000},
	{name: "bytes*n", src: "s = b'abc'\nmark()\nr = s * {N}", nBudget: 400000, nCeil: 22000},
	{name: "list*n", src: "s = [1, 2, 3]\nmark()\nr = s * {N}", nBudget: 30000, nCeil: 1500},
	{name: "tuple*n", src: "s = (1, 2, 3)\nmark()\nr = s * {N}", nBudget: 30000, nCeil: 1500},
	{name: "str+str", src: "s = 'a' * {N}\nmark()\nr = s + s", nBudget: 600000, nCeil: 40000},
	{name: "list+list", src: "s = [1] * {N}\nmark()\nr = s + s", nBudget: 40000, nCeil: 3000},
	{name: "tuple+tuple", src: "s = (1,) * {N}\nmark()\nr = s + s", nBudget: 40000, nCeil: 3000},
	{name: "list+=list", src: "s = [1] * {N}\nmark()\ns += s", nBudget: 40000, nCeil: 3000},
	{name: "list.extend", src: "s = [1] * {N}\nmark()\ns.extend(s)", nBudget: 40000, nCeil: 3000},
	{name: "%", src: "s = 'a' * {N}\na = (s, s)\nmark()\nr = '%s%s' % a", nBudget: 600000, nCeil: 40000, late: true},
	{name: "str(x)", src: "s = 'a' * {N}\nx = [s, s]\nmark()\nr = str(x)", nBudget: 600000, nCeil: 40000, late: true},
	{name: "repr(x)", src: "s = 'a' * {N}\nx = [s, s]\nmark()\nr = repr(x)", nBudget: 600000, late: true}, // repr truncates at the ceiling
	{name: "print", src: "s = 'a' * {N}\nmark()\nprint(s)", nBudget: 600000, late: true},
	{name: "replace", src: "s = 'a' * {N}\nt = 'b' * {N}\nmark()\nr = s.replace('a', t)", nBudget: 1100, nCeil: 300},
	{name: "join", src: "s = 'x' * {N}\nparts = [s, s, s]\nmark()\nr = s.join(parts)", nBudget: 300000, nCeil: 20000},
	{name: "format", src: "s = 'a' * {N}\nmark()\nr = '{}{}'.format(s, s)", nBudget: 600000, nCeil: 40000, late: true},
	{name: "format/trailing literal", src: "s = 'a' * {N}\nf = '{}' + s\nmark()\nr = f.format(s)", nBudget: 300000, nCeil: 40000, late: true},
	{name: "bytes(range)", src: "mark()\nr = bytes(range({N}))", nBudget: 2000000, nCeil: 70000},
	{name: "bytes(invalid str)", src: "s = 'é'[:1] * {N}\nmark()\nr = bytes(s)", nBudget: 300000},
	{name: "str(invalid bytes)", src: "s = b'\\xff' * {N}\nmark()\nr = str(s)", nBudget: 300000},
	{name: "list(range)", src: "mark()\nr = list(range({N}))", nBudget: 100000, nCeil: 70000},
	{name: "tuple(range)", src: "mark()\nr = tuple(range({N}))", nBudget: 100000, nCeil: 70000},
	{name: "set(range)", src: "mark()\nr = set(range({N}))", nBudget: 10000, nCeil: 70000},
	{name: "dict(pairs)", src: "x = [(1, 2)] * {N}\nmark()\nr = dict(x)", nBudget: 8000, ceil: "mark()\nr = dict(range({N}))", nCeil: 70000},
	{name: "sorted", src: "mark()\nr = sorted(range({N}))", nBudget: 100000, nCeil: 70000},
	{name: "reversed", src: "mark()\nr = reversed(range({N}))", nBudget: 100000, nCeil: 70000},
	{name: "zip", src: "mark()\nr = zip(range({N}), range({N}))", nBudget: 15000, nCeil: 70000},
	{name: "enumerate", src: "mark()\nr = enumerate(range({N}))", nBudget: 15000, nCeil: 70000},
	{name: "list[:]", src: "x = [1] * {N}\nmark()\nr = x[:]", nBudget: 40000},
	{name: "list[::-1]", src: "x = [1] * {N}\nmark()\nr = x[::-1]", nBudget: 40000},
	{name: "tuple[::-1]", src: "x = (1,) * {N}\nmark()\nr = x[::-1]", nBudget: 40000},
	{name: "str[::-1]", src: "x = 'a' * {N}\nmark()\nr = x[::-1]", nBudget: 600000},
	{name: "bytes[::-1]", src: "x = b'a' * {N}\nmark()\nr = x[::-1]", nBudget: 600000},
	{name: "upper", src: "s = 'a' * {N}\nmark()\nr = s.upper()", nBudget: 600000},
	{name: "lower", src: "s = 'A' * {N}\nmark()\nr = s.lower()", nBudget: 600000},
	{name: "title", src: "s = 'a' * {N}\nmark()\nr = s.title()", nBudget: 600000},
	{name: "capitalize", src: "s = 'a' * {N}\nmark()\nr = s.capitalize()", nBudget: 600000},
	{name: "split(sep)", src: "s = 'a,' * {N}\nmark()\nr = s.split(',')", nBudget: 200000},
	{name: "split()", src: "s = 'a ' * {N}\nmark()\nr = s.split()", nBudget: 200000},
	{name: "rsplit()", src: "s = 'a ' * {N}\nmark()\nr = s.rsplit()", nBudget: 200000},
	{name: "splitlines", src: "s = 'a\\n' * {N}\nmark()\nr = s.splitlines()", nBudget: 200000},
	{name: "dict.items", src: "d = {}\nfor i in range({N}): d[i] = i\nmark()\nr = d.items()", nBudget: 7000},
	{name: "dict.keys", src: "d = {}\nfor i in range({N}): d[i] = i\nmark()\nr = d.keys()", nBudget: 7500},
	{name: "dict.values", src: "d = {}\nfor i in range({N}): d[i] = i\nmark()\nr = d.values()", nBudget: 7000},
	{name: "dict|dict", src: "a = {}; b = {}\nfor i in range({N}): a[i] = i; b[-i] = i\nmark()\nr = a | b", nBudget: 3000, nCeil: 300},
	{name: "dict|=dict", src: "a = {}; b = {}\nfor i in range({N}): a[i] = i; b[-i] = i\nmark()\na |= b", nBudget: 4000},
	{name: "dict.update", src: "d = {}\nb = {}\nfor i in range({N}): b[i] = i\nmark()\nd.update(b)", nBudget: 5000},
	{name: "set|set", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a | b", nBudget: 3000, nCeil: 300},
	{name: "set&set", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(i)\nmark()\nr = a & b", nBudget: 3500},
	{name: "set-set", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a - b", nBudget: 3500},
	{name: "set^set", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a ^ b", nBudget: 3000, nCeil: 300},
	{name: "set.union", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a.union(b)", nBudget: 3000, nCeil: 300},
	{name: "set.intersection", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(i)\nmark()\nr = a.intersection(b)", nBudget: 3500},
	{name: "set.difference", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a.difference(b)", nBudget: 3500},
	{name: "set.symmetric_difference", src: "a = set(); b = set()\nfor i in range({N}): a.add(i); b.add(-i)\nmark()\nr = a.symmetric_difference(b)", nBudget: 3000, nCeil: 300},
	{name: "set.update", src: "a = set(); b = set()\nfor i in range({N}): b.add(i)\nmark()\na.update(b)", nBudget: 5000},
	{name: "f(*x)", src: "x = [1] * {N}\ndef f(*a): return None\nmark()\nf(*x)", nBudget: 40000},
	{name: "f(**d)", src: "d = {}\nfor i in range({N}): d['k' + str(i)] = i\ndef f(**k): return None\nmark()\nf(**d)", nBudget: 6000},
}

func TestAllocBudget_RefusesOverBudget(t *testing.T) {
	for _, c := range refuseCases {
		if c.nBudget == 0 {
			continue
		}
		budget := uint64(budgetMiB)
		r := runProg(t, budget, withN(c.src, c.nBudget))
		if r.err == nil {
			t.Errorf("%s: succeeded, AllocatedBytes() = %d of %d", c.name, r.th.AllocatedBytes(), budget)
			continue
		}
		var be *AllocBudgetError
		if !errors.As(r.err, &be) || !strings.HasPrefix(r.err.Error(), budgetPrefix) {
			t.Errorf("%s: error is not the budget error: %v", c.name, r.err)
			continue
		}
		if be.Limit != budget {
			t.Errorf("%s: error limit %d, want %d", c.name, be.Limit, budget)
		}
		if got := r.th.AllocatedBytes(); got > budget {
			t.Errorf("%s: AllocatedBytes() = %d exceeds the budget %d", c.name, got, budget)
		}
		// The refused operation charged nothing.
		if len(r.marks) != 1 || r.th.AllocatedBytes() != r.marks[0] {
			t.Errorf("%s: refused operation charged: marks %v, allocated %d", c.name, r.marks, r.th.AllocatedBytes())
		}
	}
}

// The string forms are built up to the limit of the thread and no further:
// a form of 2^40 leaves, or a thousand fields of a large argument, must not be
// built to the end before it is refused.
func TestAllocBudget_StringFormsStopAtTheBudget(t *testing.T) {
	const shared = "x = [1]\nfor i in range(40): x = [x, x]\n" // 2^40 leaves in its string form
	const many = "s = 'a' * 10000\nargs = (s,) * 5000\n"       // 50 MB in 5000 fields
	for _, c := range []struct{ name, setup, op string }{
		{"str", shared, "r = str(x)"},
		{"repr", shared, "r = repr(x)"},
		{"%s", shared, "r = '%s' % (x,)"},
		{"%r", shared, "r = '%r' % (x,)"},
		{"format", shared, "r = '{}'.format(x)"},
		{"format/r", shared, "r = '{!r}'.format(x)"},
		{"print", shared, "print(x)"},
		{"print of many", many, "print(*args)"},
		{"% with many fields", many + "f = '%s' * 5000\n", "r = f % args"},
		{"format with many fields", many + "f = '{}' * 5000\n", "r = f.format(*args)"},
		{"join with many parts", many, "r = ','.join(args)"},
	} {
		r := runProg(t, budgetMiB, c.setup+"mark()\n"+c.op+"\n")
		wantBudgetErr(t, r, budgetMiB)
		// The builder doubles: at most 2x the limit, with room to spare.
		if spent := r.memEnd - r.memMark; spent > 8*budgetMiB {
			t.Errorf("%s: allocated %d bytes before the refusal, want at most %d", c.name, spent, 8*budgetMiB)
		}
	}
	// fail builds its message up to the limit too (and fails, as it always does).
	r := runProg(t, budgetMiB, shared+"mark()\nfail(x)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "fail: [[") || len(r.err.Error()) > 2*budgetMiB {
		t.Errorf("fail: err = %.100v (%d bytes)", r.err, len(r.err.Error()))
	}
	if spent := r.memEnd - r.memMark; spent > 8*budgetMiB {
		t.Errorf("fail: allocated %d bytes, want at most %d", spent, 8*budgetMiB)
	}
}

// A refused operation must be refused before it allocates its result: the
// charge in Call, which counts a result after it is made, would give the same
// error and the same count, but only after the memory was spent. The runtime's
// count of bytes allocated since the mark bounds what the refused operation
// allocated; it is far below the size of the result.
func TestAllocBudget_RefusalPrecedesAllocation(t *testing.T) {
	const slack = 16 << 10 // the error, its backtrace
	for _, c := range refuseCases {
		if c.nBudget == 0 || c.late {
			continue
		}
		budget := uint64(budgetMiB)
		r := runProg(t, budget, withN(c.src, c.nBudget))
		wantBudgetErr(t, r, budget)
		if spent := r.memEnd - r.memMark; spent > slack {
			t.Errorf("%s: the refused operation allocated %d bytes, want at most %d", c.name, spent, slack)
		}
	}
}

func TestAllocBudget_BigIntSquaringIsBounded(t *testing.T) {
	// x = x*x doubles the digits on every step. Each multiplication is
	// charged the digit bytes of both operands, (BitLen+7)/8 each, before it
	// is made; work out the iteration that the budget refuses.
	x := new(big.Int).Lsh(big.NewInt(1), 500)
	var total uint64
	wantIter := -1
	for i := 0; i < 40; i++ {
		n := 2 * uint64((x.BitLen()+7)/8)
		if total+n > budgetMiB {
			wantIter = i
			break
		}
		total += n
		x.Mul(x, x)
	}
	if wantIter < 0 {
		t.Fatal("the model never reaches the budget")
	}
	for run := 0; run < 2; run++ {
		r := runProg(t, budgetMiB, "x = 1 << 500\nfor i in range(40):\n  trace(i)\n  x = x * x\n")
		wantBudgetErr(t, r, budgetMiB)
		if got := r.traced[len(r.traced)-1]; got != wantIter {
			t.Fatalf("refused at iteration %d, want %d", got, wantIter)
		}
		if got := r.th.AllocatedBytes(); got != total {
			t.Fatalf("charged %d, want %d", got, total)
		}
	}
}

func TestAllocCeiling_RefusesOverCeiling(t *testing.T) {
	for _, c := range refuseCases {
		if c.nCeil == 0 {
			continue
		}
		smallLimit(t) // maxAlloc = 1<<16
		src := c.src
		if c.ceil != "" {
			src = c.ceil
		}
		r := runProg(t, 0, withN(src, c.nCeil)) // no budget
		if r.err == nil {
			t.Errorf("%s: succeeded over the ceiling", c.name)
			continue
		}
		var be *AllocBudgetError
		if errors.As(r.err, &be) {
			t.Errorf("%s: budget error without a budget: %v", c.name, r.err)
		}
		if !strings.Contains(r.err.Error(), "excessive") && !strings.Contains(r.err.Error(), "size limit") {
			t.Errorf("%s: error does not report the size limit: %v", c.name, r.err)
		}
		// The refused operation charged nothing.
		if len(r.marks) == 1 && r.th.AllocatedBytes() != r.marks[0] {
			t.Errorf("%s: refused operation charged: mark %d, allocated %d", c.name, r.marks[0], r.th.AllocatedBytes())
		}
	}
}

// rsplit preallocated max+1 strings, whatever the string: rsplit(None, 1<<31-1)
// asked Go for 32 GiB.
func TestAllocCeiling_RsplitDoesNotPreallocateByMax(t *testing.T) {
	r := runProg(t, budgetMiB, "mark()\nr = 'a b c d'.rsplit(None, 2147483647)\nmark()\nif r != ['a', 'b', 'c', 'd']: fail('got ' + str(r))")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.marks[1] - r.marks[0]; got != lb(0)+4*32 {
		t.Fatalf("charged %d, want %d", got, lb(0)+4*32)
	}
}

// repr is not an error at the ceiling: it returns a bounded form.
func TestAllocCeiling_ReprReturnsBoundedForm(t *testing.T) {
	smallLimit(t)
	r := runProg(t, 0, "x = [1]\nfor i in range(40): x = [x, x]\nr = repr(x)\nif 'truncated' not in r: fail('not marked')\nif len(r) > 3 * 65536: fail('unbounded: ' + str(len(r)))")
	if r.err != nil {
		t.Fatal(r.err)
	}
}

// ---- budget semantics ----

func TestAllocBudget_AccumulationFailsAtPredictableIteration(t *testing.T) {
	// Each iteration retains a fresh copy of a 100000-byte string: s + 'y' is
	// charged its length, 100001. After the initial 100000 and the empty list
	// (48), iteration i brings the total to 148 + 100000 + 100001*(i+1)... the
	// budget 1048576 allows i <= 8, so iteration 9 is the first refused, with
	// 1000057 bytes charged.
	src := "s = 'x' * 100000\nkeep = []\nfor i in range(100):\n  trace(i)\n  keep.append(s + 'y')\n"
	var last int
	var allocated uint64
	for run := 0; run < 3; run++ {
		r := runProg(t, budgetMiB, src)
		wantBudgetErr(t, r, budgetMiB)
		if got := r.traced[len(r.traced)-1]; run == 0 {
			last, allocated = got, r.th.AllocatedBytes()
		} else if got != last || r.th.AllocatedBytes() != allocated {
			t.Fatalf("run %d: refused at iteration %d with %d bytes; first run %d with %d", run, got, r.th.AllocatedBytes(), last, allocated)
		}
	}
	if last != 9 || allocated != 1000057 {
		t.Fatalf("refused at iteration %d with %d bytes charged, want iteration 9 with 1000057", last, allocated)
	}
}

func TestAllocBudget_NoBudgetNeverRefusesButCounts(t *testing.T) {
	r := runProg(t, 0, "s = 'x' * 100000\nkeep = []\nfor i in range(100): keep.append(s + 'y')\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := r.th.AllocatedBytes(), uint64(48+100000+100*100001); got != want {
		t.Fatalf("AllocatedBytes() = %d, want %d", got, want)
	}
}

func TestAllocBudget_ErrorIsRecognizedThroughWrapping(t *testing.T) {
	// The error crosses nameErr / prefixErr wrappers (dict, update, list.extend)
	// and the EvalError of the call: it stays one value, found by errors.As,
	// and its text keeps the fixed beginning.
	for _, src := range []string{
		"x = [1] * 40000\nr = dict([(1, 2)] * 40000)",
		"d = {}\nd.update([(i, i) for i in range(40000)])",
		"x = [1] * 40000\nx.extend(x)",
		"r = set(range(40000))",
		"r = 'a'.join(['b'] * 40000) + 'c' * 300000",
	} {
		r := runProg(t, 1<<19, src)
		var ev *EvalError
		if !errors.As(r.err, &ev) {
			t.Errorf("%q: not an *EvalError: %v", src, r.err)
			continue
		}
		wantBudgetErr(t, r, 1<<19)
	}
}

func TestAllocBudget_TextDoesNotContainScriptSizes(t *testing.T) {
	a := runProg(t, budgetMiB, "mark()\nr = list(range(100000))")
	b := runProg(t, budgetMiB, "mark()\nr = list(range(200000))")
	if a.err == nil || b.err == nil || a.err.Error() != b.err.Error() {
		t.Fatalf("texts differ: %v / %v", a.err, b.err)
	}
}

func TestChargeAlloc(t *testing.T) {
	t.Run("zero always passes", func(t *testing.T) {
		th := &Thread{}
		th.SetMaxAllocBytes(1)
		if err := th.ChargeAlloc(0); err != nil || th.AllocatedBytes() != 0 {
			t.Fatalf("err %v, allocated %d", err, th.AllocatedBytes())
		}
	})
	t.Run("exactly at the limit passes, one more is refused", func(t *testing.T) {
		th := &Thread{}
		th.SetMaxAllocBytes(100)
		if err := th.ChargeAlloc(60); err != nil {
			t.Fatal(err)
		}
		if err := th.ChargeAlloc(40); err != nil {
			t.Fatalf("charging exactly up to the limit: %v", err)
		}
		if th.AllocatedBytes() != 100 {
			t.Fatalf("allocated %d", th.AllocatedBytes())
		}
		err := th.ChargeAlloc(1)
		var be *AllocBudgetError
		if !errors.As(err, &be) || be.Limit != 100 || be.Allocated != 100 || be.Requested != 1 {
			t.Fatalf("err = %#v", err)
		}
		if th.AllocatedBytes() != 100 {
			t.Fatalf("a refused charge changed the count: %d", th.AllocatedBytes())
		}
		if err.Error() != budgetPrefix+"a thread may allocate at most 100 bytes" {
			t.Fatalf("text = %q", err.Error())
		}
	})
	t.Run("one request over the limit is refused whole", func(t *testing.T) {
		th := &Thread{}
		th.SetMaxAllocBytes(100)
		if err := th.ChargeAlloc(101); err == nil {
			t.Fatal("charged 101 against 100")
		}
		if th.AllocatedBytes() != 0 {
			t.Fatalf("allocated %d", th.AllocatedBytes())
		}
	})
	t.Run("uint64 overflow", func(t *testing.T) {
		th := &Thread{}
		th.SetMaxAllocBytes(math.MaxUint64)
		th.allocated = math.MaxUint64 - 5
		if err := th.ChargeAlloc(6); err == nil {
			t.Fatal("allocated+n wrapped around uint64 and was accepted")
		}
		if err := th.ChargeAlloc(math.MaxUint64); err == nil {
			t.Fatal("MaxUint64 accepted")
		}
		if err := th.ChargeAlloc(5); err != nil {
			t.Fatal(err)
		}
		if th.AllocatedBytes() != math.MaxUint64 {
			t.Fatalf("allocated %d", th.AllocatedBytes())
		}
		// The count saturates, it does not wrap.
		unbudgeted := &Thread{}
		unbudgeted.allocated = math.MaxUint64 - 5
		if err := unbudgeted.chargeBudget(10); err != nil {
			t.Fatal(err)
		}
		if unbudgeted.AllocatedBytes() != math.MaxUint64 {
			t.Fatalf("allocated %d, want saturation at MaxUint64", unbudgeted.AllocatedBytes())
		}
	})
	t.Run("a request past the ceiling is refused without a budget", func(t *testing.T) {
		th := &Thread{}
		if err := th.ChargeAlloc(uint64(maxAlloc - 1)); err != nil {
			t.Fatal(err)
		}
		err := th.ChargeAlloc(uint64(maxAlloc))
		var be *AllocBudgetError
		if err == nil || errors.As(err, &be) || !strings.Contains(err.Error(), "excessive") {
			t.Fatalf("err = %v", err)
		}
		if th.AllocatedBytes() != uint64(maxAlloc-1) {
			t.Fatalf("allocated %d", th.AllocatedBytes())
		}
		if err := th.ChargeAlloc(math.MaxUint64); err == nil {
			t.Fatal("MaxUint64 accepted")
		}
	})
	t.Run("no budget: unlimited total", func(t *testing.T) {
		th := &Thread{}
		for i := 0; i < 5; i++ {
			if err := th.ChargeAlloc(uint64(maxAlloc - 1)); err != nil {
				t.Fatal(err)
			}
		}
		if th.AllocatedBytes() != 5*uint64(maxAlloc-1) {
			t.Fatalf("allocated %d", th.AllocatedBytes())
		}
	})
	t.Run("headroom", func(t *testing.T) {
		th := &Thread{}
		if got := th.AllocHeadroom(); got != uint64(maxAlloc-1) {
			t.Fatalf("no budget: %d", got)
		}
		th.SetMaxAllocBytes(1000)
		th.ChargeAlloc(300)
		if got := th.AllocHeadroom(); got != 700 {
			t.Fatalf("headroom %d, want 700", got)
		}
		th.ChargeAlloc(700)
		if got := th.AllocHeadroom(); got != 0 {
			t.Fatalf("headroom %d, want 0", got)
		}
		th.SetMaxAllocBytes(uint64(maxAlloc) * 4)
		if got := th.AllocHeadroom(); got != uint64(maxAlloc-1) {
			t.Fatalf("a large budget is capped by the ceiling: %d", got)
		}
	})
}

// ---- Call: charging what a built-in did not charge itself ----

func TestCall_ChargesBuiltinResults(t *testing.T) {
	str := func(n int) Value { return String(strings.Repeat("x", n)) }
	for _, c := range []struct {
		name string
		fn   func(th *Thread) (Value, error) // the built-in's body
		want uint64
	}{
		{"string", func(*Thread) (Value, error) { return str(1000), nil }, 1000},
		{"bytes", func(*Thread) (Value, error) { return Bytes(strings.Repeat("x", 1000)), nil }, 1000},
		{"list", func(*Thread) (Value, error) { return NewList(make([]Value, 100)), nil }, lb(100)},
		{"tuple", func(*Thread) (Value, error) { return make(Tuple, 100), nil }, tb(100)},
		{"dict", func(*Thread) (Value, error) {
			d := new(Dict)
			for i := 0; i < 10; i++ {
				d.SetKey(MakeInt(i), None)
			}
			return d, nil
		}, db(10)},
		{"set", func(*Thread) (Value, error) {
			s := new(Set)
			for i := 0; i < 10; i++ {
				s.Insert(MakeInt(i))
			}
			return s, nil
		}, db(10)},
		{"int is not charged", func(*Thread) (Value, error) { return MakeInt(1 << 40), nil }, 0},
		{"None is not charged", func(*Thread) (Value, error) { return None, nil }, 0},
		{"a charge made by the built-in is not charged again", func(th *Thread) (Value, error) {
			if err := th.ChargeAlloc(1000); err != nil {
				return nil, err
			}
			return str(1000), nil
		}, 1000},
		{"a partial charge is completed", func(th *Thread) (Value, error) {
			if err := th.ChargeAlloc(400); err != nil {
				return nil, err
			}
			return str(1000), nil
		}, 1000},
		{"an excess charge stays", func(th *Thread) (Value, error) {
			if err := th.ChargeAlloc(3000); err != nil {
				return nil, err
			}
			return str(1000), nil
		}, 3000},
		{"charged by a built-in the built-in calls", func(th *Thread) (Value, error) {
			// ('x'*1000) inside the built-in is charged 1000 by the
			// operation itself; its own result (1000) is not charged again.
			s, err := Binary(syntax.STAR, String("x"), MakeInt(1000)) // no thread: not charged
			if err != nil {
				return nil, err
			}
			if err := th.chargeBytes(1000); err != nil {
				return nil, err
			}
			return s, nil
		}, 1000},
	} {
		th := &Thread{}
		th.SetMaxAllocBytes(1 << 20)
		b := NewBuiltin("host", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) { return c.fn(th) })
		if _, err := Call(th, b, nil, nil); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := th.AllocatedBytes(); got != c.want {
			t.Errorf("%s: charged %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCall_RefusesResultOverBudget(t *testing.T) {
	th := &Thread{}
	th.SetMaxAllocBytes(1000)
	b := NewBuiltin("host", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		return String(strings.Repeat("x", 1001)), nil
	})
	res, err := Call(th, b, nil, nil)
	var be *AllocBudgetError
	if !errors.As(err, &be) || res != nil {
		t.Fatalf("res %v, err %v", res, err)
	}
	if th.AllocatedBytes() != 0 {
		t.Fatalf("a refused result was charged: %d", th.AllocatedBytes())
	}
	// A call that fails with its own error is not charged for the result.
	th2 := &Thread{}
	th2.SetMaxAllocBytes(1000)
	failing := NewBuiltin("fail", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		return String("xxxxxxxxxx"), errors.New("boom")
	})
	if _, err := Call(th2, failing, nil, nil); err == nil || th2.AllocatedBytes() != 0 {
		t.Fatalf("err %v, allocated %d", err, th2.AllocatedBytes())
	}
}

// Only built-ins are charged by Call: a Starlark function returns values that
// were already charged where they were made.
func TestCall_DoesNotChargeStarlarkFunctions(t *testing.T) {
	r := runProg(t, 0, "s = 'x' * 1000\ndef f(): return s\nmark()\nr = f()\nmark()")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.marks[1]-r.marks[0] != 0 {
		t.Fatalf("charged %d for a Starlark function", r.marks[1]-r.marks[0])
	}
}

func TestAllocBudget_DeterministicAcrossRuns(t *testing.T) {
	src := `
d = {}
for i in range(300):
  d[str(i)] = [i] * 10
s = ','.join([k for k in d])
parts = s.split(',')
t = sorted(parts)
u = '%s|%s' % (t[0], t[-1])
e = enumerate(parts)
z = zip(parts, parts)
j = repr(d.items())
`
	var first uint64
	for i := 0; i < 5; i++ {
		r := runProg(t, 0, src)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if i == 0 {
			first = r.th.AllocatedBytes()
			if first == 0 {
				t.Fatal("nothing charged")
			}
		} else if got := r.th.AllocatedBytes(); got != first {
			t.Fatalf("run %d: %d, run 0: %d", i, got, first)
		}
	}
}

// Binary has no thread: it applies the ceiling and charges nothing.
func TestBinary_NoThreadAppliesCeiling(t *testing.T) {
	smallLimit(t)
	x := String(strings.Repeat("a", 40000))
	if _, err := Binary(syntax.PLUS, x, x); err == nil || !strings.Contains(err.Error(), "excessive") {
		t.Fatalf("err = %v", err)
	}
	if v, err := Binary(syntax.PLUS, String("a"), String("b")); err != nil || v != String("ab") {
		t.Fatalf("v, err = %v, %v", v, err)
	}
	l := NewList(make([]Value, 40000))
	if _, err := Binary(syntax.PLUS, l, l); err == nil || !strings.Contains(err.Error(), "excessive") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Binary(syntax.STAR, x, MakeInt(2)); err == nil || !strings.Contains(err.Error(), "excessive") {
		t.Fatalf("err = %v", err)
	}
}

// The interpreter's charging of a Starlark call does not depend on whether
// the budget is set: a program that fits behaves the same.
func TestAllocBudget_ProgramWithinBudgetRunsAsWithout(t *testing.T) {
	src := "r = [str(i) + 'x' for i in range(100)]\nif len(','.join(r)) != 389: fail('wrong')\n"
	for _, budget := range []uint64{0, budgetMiB} {
		if r := runProg(t, budget, src); r.err != nil {
			t.Fatalf("budget %d: %v", budget, r.err)
		}
	}
}

// ---- iterables of unknown length ----

// A lazyIter is an Iterable without a Len: it yields n values (n < 0: without
// end). The operations that preallocate from Len cannot know the size of their
// result and are charged as the elements are produced.
type lazyIter struct {
	n     int
	pairs bool // yield (i, i) instead of i
	bytes bool // yield i % 256: values fit in a byte
	strs  bool // yield the string "x"
}

func (l lazyIter) String() string        { return "lazy" }
func (l lazyIter) Type() string          { return "lazy" }
func (l lazyIter) Freeze()               {}
func (l lazyIter) Truth() Bool           { return True }
func (l lazyIter) Hash() (uint32, error) { return 0, errors.New("unhashable") }
func (l lazyIter) Iterate() Iterator     { return &lazyIterator{l, 0} }

type lazyIterator struct {
	l lazyIter
	i int
}

func (it *lazyIterator) Next(p *Value) bool {
	if it.l.n >= 0 && it.i >= it.l.n {
		return false
	}
	if it.l.pairs {
		*p = Tuple{MakeInt(it.i), MakeInt(it.i)}
	} else {
		if it.l.strs {
			*p = String("x")
		} else if it.l.bytes {
			*p = MakeInt(it.i % 256)
		} else {
			*p = MakeInt(it.i)
		}
	}
	it.i++
	return true
}
func (*lazyIterator) Done() {}

func lazyBuiltins() StringDict {
	mk := func(pairs, bytes bool) *Builtin {
		return NewBuiltin("lazy", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
			var n int
			if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n); err != nil {
				return nil, err
			}
			return lazyIter{n: n, pairs: pairs, bytes: bytes}, nil
		})
	}
	lazyStr := NewBuiltin("lazy_str", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n); err != nil {
			return nil, err
		}
		return lazyIter{n: n, strs: true}, nil
	})
	return StringDict{"lazy": mk(false, false), "lazy_pairs": mk(true, false), "lazy_bytes": mk(false, true), "lazy_str": lazyStr}
}

func TestAllocCharge_UnknownLengthExact(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"list", "", "r = list(lazy(3))", lb(0) + 3*32},
		{"tuple", "", "r = tuple(lazy(3))", 32 + 3*32},
		{"set", "", "r = set(lazy(3))", db(0) + 3*128},
		{"sorted", "", "r = sorted(lazy(3))", lb(0) + 3*32},
		{"reversed", "", "r = reversed(lazy(3))", lb(0) + 3*32},
		{"enumerate", "", "r = enumerate(lazy(3))", lb(0) + 3*80},
		{"zip", "", "r = zip(lazy(3), lazy(3))", lb(0) + 3*80},
		{"bytes", "", "r = bytes(lazy(3))", 3},
		{"dict", "", "r = dict(lazy_pairs(3))", db(0) + 3*128},
		{"list.extend", "x = [1]", "x.extend(lazy(3))", 3 * 32},
		{"list+=", "x = [1]", "x += lazy(3)", 3 * 32},
		{"f(*x)", "def f(*a): return None", "f(*lazy(3))", 3*32 + tb(3)},
		{"string.elems", "s = 'abc'", "r = list(s.elems())", lb(0) + 3*32},
	} {
		r := runProgWith(t, 0, c.setup+"\nmark()\n"+c.op+"\nmark()\n", lazyBuiltins())
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.marks[1] - r.marks[0]; got != c.want {
			t.Errorf("%s: charged %d, want %d", c.name, got, c.want)
		}
	}
}

// An iterable of unknown length that is longer than the budget (or the
// ceiling) is stopped by it. The iterables are finite, a few times longer than
// the limit: a regression makes the operation succeed, which is a failure of
// the test, not an unbounded allocation.
func TestAllocBudget_LongIterableIsStopped(t *testing.T) {
	for _, c := range []struct {
		op string
		n  int
	}{
		{"list(lazy({N}))", 100000}, {"tuple(lazy({N}))", 100000}, {"set(lazy({N}))", 20000},
		{"sorted(lazy({N}))", 100000}, {"reversed(lazy({N}))", 100000},
		{"enumerate(lazy({N}))", 20000}, {"zip(lazy({N}), lazy({N}))", 20000},
		{"bytes(lazy_bytes({N}))", 2200000}, {"dict(lazy_pairs({N}))", 20000},
		{"[1].extend(lazy({N}))", 100000}, {"[1] + list(lazy({N}))", 100000},
		{"(lambda *a: None)(*lazy({N}))", 100000},
		{"set().update(lazy({N}))", 20000}, {"set().union(lazy({N}))", 20000},
		{"set().symmetric_difference(lazy({N}))", 20000}, {"','.join(lazy_str({N}))", 100000},
		{"dict().update(lazy_pairs({N}))", 20000},
	} {
		t.Run(c.op, func(t *testing.T) {
			src := withN("mark()\nr = "+c.op+"\n", c.n)
			r := runProgWith(t, budgetMiB, src, lazyBuiltins())
			wantBudgetErr(t, r, budgetMiB)
			smallLimit(t) // restored at the end of the subtest
			r = runProgWith(t, 0, src, lazyBuiltins())
			var be *AllocBudgetError
			if r.err == nil || errors.As(r.err, &be) || !strings.Contains(r.err.Error(), "excessive") {
				t.Errorf("without a budget: err = %v", r.err)
			}
		})
	}
}

// ---- mappings of unknown length ----

// A lazyMap is an IterableMapping without a Len: its items are materialized
// by Items, which cannot be charged before it is called.
type lazyMap struct{ n int }

func (m lazyMap) String() string                 { return "lazy_map" }
func (m lazyMap) Type() string                   { return "lazy_map" }
func (m lazyMap) Freeze()                        {}
func (m lazyMap) Truth() Bool                    { return True }
func (m lazyMap) Hash() (uint32, error)          { return 0, errors.New("unhashable") }
func (m lazyMap) Get(Value) (Value, bool, error) { return nil, false, nil }
func (m lazyMap) Iterate() Iterator              { return lazyIter{n: m.n}.Iterate() }
func (m lazyMap) Items() []Tuple {
	items := make([]Tuple, m.n)
	for i := range items {
		items[i] = Tuple{String("k" + strconv.Itoa(i)), MakeInt(i)}
	}
	return items
}

func lazyMapBuiltins() StringDict {
	return StringDict{"lazy_map": NewBuiltin("lazy_map", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n); err != nil {
			return nil, err
		}
		return lazyMap{n}, nil
	})}
}

func TestAllocCharge_MappingOfUnknownLength(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"dict(m)", "", "r = dict(lazy_map(3))", db(0) + 3*128},
		{"dict.update(m)", "d = {}", "d.update(lazy_map(3))", 3 * 128},
		{"f(**m)", "def f(**k): return None", "f(**lazy_map(3))", 3*80 + 512},
	} {
		r := runProgWith(t, 0, c.setup+"\nmark()\n"+c.op+"\nmark()\n", lazyMapBuiltins())
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.marks[1] - r.marks[0]; got != c.want {
			t.Errorf("%s: charged %d, want %d", c.name, got, c.want)
		}
	}
	// Over the budget: refused once the items exist and their number is known.
	for _, op := range []string{"dict(lazy_map(100000))", "d.update(lazy_map(100000))", "f(**lazy_map(100000))"} {
		r := runProgWith(t, budgetMiB, "d = {}\ndef f(**k): return None\nmark()\nr = "+strings.Replace(op, "d.update", "d.update", 1)+"\n", lazyMapBuiltins())
		if strings.HasPrefix(op, "d.update") {
			r = runProgWith(t, budgetMiB, "d = {}\nmark()\n"+op+"\n", lazyMapBuiltins())
		} else if strings.HasPrefix(op, "f(") {
			r = runProgWith(t, budgetMiB, "def f(**k): return None\nmark()\n"+op+"\n", lazyMapBuiltins())
		}
		wantBudgetErr(t, r, budgetMiB)
	}
}

// A size that overflows uint64 is a refusal by the budget, not a wrapped-around
// small number that passes it.
func TestAllocBudget_OverflowingSizeIsRefusedByTheBudget(t *testing.T) {
	for _, src := range []string{
		"r = list(range(1 << 62))", // 16 * 2^62 wraps to 0
		"r = set(range(1 << 62))",  // 96 * 2^62
		"r = enumerate(range(1 << 62))",
		"r = zip(range(1 << 62), range(1 << 62))",
	} {
		r := runProg(t, budgetMiB, src)
		wantBudgetErr(t, r, budgetMiB)
	}
	if got := satMul(1<<62, 16); got != math.MaxUint64 {
		t.Errorf("satMul(2^62, 16) = %d, want saturation", got)
	}
	if got := satMul(3, 5); got != 15 {
		t.Errorf("satMul(3, 5) = %d", got)
	}
	if got := satAdd(math.MaxUint64-1, 5); got != math.MaxUint64 {
		t.Errorf("satAdd = %d, want saturation", got)
	}
}

// An attrHolder is a host value with attributes, as a struct of the host:
// the value of getattr is a list that exists already.
type attrHolder struct{ v Value }

func (a attrHolder) String() string        { return "holder" }
func (a attrHolder) Type() string          { return "holder" }
func (a attrHolder) Freeze()               {}
func (a attrHolder) Truth() Bool           { return True }
func (a attrHolder) Hash() (uint32, error) { return 0, errors.New("unhashable") }
func (a attrHolder) AttrNames() []string   { return []string{"v"} }
func (a attrHolder) Attr(name string) (Value, error) {
	if name == "v" {
		return a.v, nil
	}
	return nil, nil
}
