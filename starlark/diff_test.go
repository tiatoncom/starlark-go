package starlark_test

// A differential test against v0.2.0: the same programs, run by v0.2.0 and by
// this version, must give the same result, the same error text and the same
// number of steps (Thread.Steps), exactly, whatever the operands: the steps are
// the interpreter's alone, and the time of an operation is in the work.
//
// The expectations of v0.2.0 are in testdata/diff_v020.golden (small operands)
// and testdata/diff_big_v020.golden (large operands). They were produced by
// running this very file, with STARLARK_WRITE_GOLDEN set, in a tree of v0.2.0
// (the commit d34a821): the file uses only the exported API, which is the same
// in both. The programs are made by a seeded generator (deterministic) and by
// tables: every built-in function and method of every type on the operands of
// every type, every binary operator on pairs of operands, and a few thousand
// random statements.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type diffOutcome struct {
	Src    string `json:"src"`
	Status string `json:"status"` // ok, error
	Result string `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
	Steps  uint64 `json:"steps"`
	Prints string `json:"prints,omitempty"`
}

func runDiffProgram(src string) (out diffOutcome) {
	out.Src = src
	var prints []string
	th := &starlark.Thread{Name: "diff", Print: func(_ *starlark.Thread, msg string) { prints = append(prints, msg) }}
	opts := &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}
	defer func() {
		if p := recover(); p != nil {
			out.Status = "panic"
			out.Err = fmt.Sprint(p)
		}
		out.Steps = th.Steps
		out.Prints = strings.Join(prints, "\n")
	}()
	globals, err := starlark.ExecFileOptions(opts, th, "p.star", src, nil)
	if err != nil {
		out.Status = "error"
		out.Err = err.Error()
		return
	}
	out.Status = "ok"
	if r, ok := globals["result"]; ok {
		out.Result = r.Type() + ":" + r.String()
	}
	return
}

// ---- the programs ----

var (
	pInts   = []string{"0", "1", "-1", "7", "255", "99", "1 << 40", "-(1 << 40)"}
	pFloats = []string{"0.0", "1.5", "-2.25"}
	pStrs   = []string{"''", "'a'", "'abc'", "'a,b,c'", "' x y '", "'Hello World'", "'\\n'", "'\\u00e9t\\u00e9'", "'aXbXc'"}
	pBytes  = []string{"b''", "b'abc'"}
	pLists  = []string{"[]", "[1]", "[3, 1, 2]", "[1, 2, 3, 4, 5, 6, 7]", "['b', 'a']", "[[1, 2], [3]]", "[1, 'a', None]"}
	pTuples = []string{"()", "(1,)", "(1, 2, 3)", "('a', 'b')", "((1, 2), (3,))"}
	pDicts  = []string{"{}", "{'a': 1}", "{'a': 1, 'b': 2, 'c': 3}", "{1: 'x', 2: 'y'}"}
	pSets   = []string{"set()", "set([1, 2, 3])", "set(['a'])", "set([3, 4])"}
	pOthers = []string{"None", "True", "False", "range(5)", "range(0)", "range(2, 10, 3)"}
)

func allOperands() []string {
	var a []string
	for _, p := range [][]string{pInts, pFloats, pStrs, pBytes, pLists, pTuples, pDicts, pSets, pOthers} {
		a = append(a, p...)
	}
	return a
}

func progs1(fn string, args []string) string {
	return "result = " + fn + "(" + strings.Join(args, ", ") + ")\n"
}

func deterministicCorpus() []string {
	var out []string
	ops := allOperands()
	// every function of the universe on every operand
	for _, fn := range []string{"abs", "any", "all", "bool", "bytes", "chr", "dict", "dir", "enumerate", "float", "hash", "int", "len", "list", "max", "min", "ord", "range", "repr", "reversed", "set", "sorted", "str", "tuple", "type", "zip"} {
		for _, a := range ops {
			out = append(out, progs1(fn, []string{a}))
		}
	}
	// functions of two arguments on selected pairs
	pairs := [][2]string{{"[1, 2, 3]", "[4, 5, 6]"}, {"'abc'", "'b'"}, {"{'a': 1}", "'a'"}, {"[3, 1, 2]", "True"}, {"5", "2"}, {"'12'", "10"}, {"[1, 2]", "1"}, {"(1, 2)", "(3, 4)"}, {"set([1])", "[2, 3]"}}
	for _, fn := range []string{"zip", "enumerate", "max", "min", "int", "getattr", "hasattr", "dict", "list", "sorted", "range", "bytes", "float"} {
		for _, p := range pairs {
			out = append(out, progs1(fn, []string{p[0], p[1]}))
		}
	}
	out = append(out,
		"result = sorted([3, 1, 2], reverse=True)\n",
		"result = sorted(['bb', 'a', 'ccc'], key=len)\n",
		"result = max([1, 5, 3], key=lambda x: -x)\n",
		"result = min('abc', key=ord)\n",
		"result = dict([('a', 1), ('b', 2)], c=3)\n",
		"result = dict(a=1, b=2)\n",
		"result = list(range(10))\n",
		"result = tuple([1, 2, 3])\n",
		"result = enumerate(['a', 'b'], 5)\n",
		"print('x', 1, [2], sep='-')\nresult = 1\n",
		"result = zip([1, 2], 'ab'.elems())\n",
		"result = int('ff', 16)\n",
		"result = int('0x1f', 0)\n",
		"result = float('1e3')\n",
		"result = str(1.5) + repr('x')\n",
		"result = hash('abc')\n",
		"result = hash(b'abc')\n",
	)
	// methods
	type meth struct {
		recvs []string
		calls []string
	}
	for _, m := range []meth{
		{pStrs, []string{".capitalize()", ".count('a')", ".count('')", ".endswith('c')", ".endswith(('a', 'c'))", ".find('b')", ".find('z')", ".index('b')", ".format()", ".format(1)", ".isalnum()", ".isalpha()", ".isdigit()", ".islower()", ".isspace()", ".istitle()", ".isupper()", ".join(['1', '2'])", ".join(('x', 'y', 'z'))", ".lower()", ".lstrip()", ".lstrip('a')", ".partition('b')", ".removeprefix('a')", ".removesuffix('c')", ".replace('a', 'x')", ".replace('', '-')", ".replace('a', 'x', 1)", ".rfind('b')", ".rindex('b')", ".rpartition('b')", ".rsplit()", ".rsplit('b', 1)", ".rstrip()", ".rstrip('c')", ".split()", ".split('b')", ".split('b', 1)", ".splitlines()", ".splitlines(True)", ".startswith('a')", ".strip()", ".strip('a')", ".title()", ".upper()", ".elems()", ".codepoints()", ".elem_ords()", ".codepoint_ords()"}},
		{pLists, []string{".append(9)", ".clear()", ".extend([7, 8])", ".extend((1,))", ".index(1)", ".index(9)", ".insert(0, 9)", ".insert(-1, 9)", ".insert(100, 9)", ".pop()", ".pop(0)", ".pop(-1)", ".remove(1)", ".remove(99)"}},
		{pDicts, []string{".clear()", ".get('a')", ".get('z', 0)", ".get(1)", ".items()", ".keys()", ".pop('a')", ".pop('z', 5)", ".pop('z')", ".popitem()", ".setdefault('a', 5)", ".setdefault('z', 5)", ".update({'z': 26})", ".update([('q', 1)])", ".update(k=1)", ".values()"}},
		{pSets, []string{".add(1)", ".add(9)", ".clear()", ".difference([1])", ".intersection([1, 2])", ".issubset([1, 2, 3, 4])", ".issuperset([1])", ".pop()", ".remove(1)", ".remove(99)", ".discard(1)", ".discard(99)", ".symmetric_difference([2, 9])", ".union([7, 8])", ".union()", ".update([5, 6])", ".update()"}},
		{pBytes, []string{".elems()"}},
	} {
		for _, r := range m.recvs {
			for _, c := range m.calls {
				out = append(out, "x = "+r+"\nresult = x"+c+"\n")
			}
		}
	}
	// binary operators and comparisons on pairs
	rng := rand.New(rand.NewSource(20260101))
	pool := allOperands()
	for _, op := range []string{"+", "-", "*", "/", "//", "%", "==", "!=", "<", "<=", ">", ">=", "in", "not in", "|", "&", "^", "<<", ">>"} {
		for k := 0; k < 40; k++ {
			a, b := pool[rng.Intn(len(pool))], pool[rng.Intn(len(pool))]
			out = append(out, "result = ("+a+") "+op+" ("+b+")\n")
		}
	}
	// indexing, slicing, unary, comprehensions, statements
	for _, a := range []string{"[1, 2, 3, 4, 5]", "(1, 2, 3, 4, 5)", "'abcde'", "b'abcde'", "range(10)"} {
		for _, i := range []string{"0", "-1", "2", "9", "1:3", ":2", "::2", "::-1", "1:", ":-1", "4:1", "4:1:-1", "::3"} {
			out = append(out, "x = "+a+"\nresult = x["+i+"]\n")
		}
	}
	for _, a := range []string{"{'a': 1, 'b': 2}"} {
		for _, k := range []string{"'a'", "'z'", "1"} {
			out = append(out, "d = "+a+"\nresult = d["+k+"]\n", "d = "+a+"\nd["+k+"] = 5\nresult = d\n")
		}
	}
	out = append(out,
		"x = [1, 2, 3]\nx[1] = 9\nresult = x\n",
		"x = [1, 2, 3]\nx += [4]\nx *= 2\nresult = x\n",
		"d = {'a': 1}\nd |= {'b': 2}\nresult = d\n",
		"s = set([1])\ns |= set([2])\ns &= set([2, 3])\ns -= set([9])\ns ^= set([2, 5])\nresult = s\n",
		"a, b = [1, 2]\nresult = a + b\n",
		"result = [x * x for x in range(10) if x % 2 == 0]\n",
		"result = {k: v for k, v in [('a', 1), ('b', 2)]}\n",
		"result = [(x, y) for x in range(3) for y in range(2)]\n",
		"def f(a, b=2, *args, **kw): return (a, b, args, kw)\nresult = f(1, 2, 3, 4, k=5)\n",
		"def f(*a, **k): return (a, k)\nresult = f(*[1, 2], **{'x': 1})\n",
		"result = (lambda x: x + 1)(2)\n",
		"x = 0\nfor i in range(10):\n  x += i\nresult = x\n",
		"x = 0\nwhile x < 5:\n  x += 1\nresult = x\n",
		"result = 'a%sb%dc%r' % ('x', 5, 'y')\n",
		"result = '%(a)s-%(b)d' % {'a': 'x', 'b': 3}\n",
		"result = '{}-{x}'.format(1, x=2)\n",
		"result = 'abc' * 3\n",
		"result = [1] * 3 + [2] * 2\n",
		"result = -5 // 2 + -5 % 3\n",
		"result = 1 << 100 >> 98\n",
		"result = (1 << 100) * (1 << 100)\n",
		"result = 10 ** 3 if False else 1000\n",
		"result = 'abc'.upper().lower().capitalize().title()\n",
		"result = ','.join([str(i) for i in range(20)])\n",
		"result = sorted([str(i) for i in range(14)])\n",
		"result = len(set([i % 5 for i in range(14)]))\n",
		"result = dict([(i, i * i) for i in range(12)])\n",
		"result = any([0, 0, 3]) and all([1, 2])\n",
		"fail('boom')\n",
		"result = 1 // 0\n",
		"result = [][0]\n",
		"result = {}['a']\n",
		"result = 'abc'.nope\n",
		"result = int('zz')\n",
		"result = float('x')\n",
		"result = [1, 2] < [1, 'a']\n",
		"result = (1,) < (1, 2)\n",
		"result = {'a': 1} == {'a': 1}\n",
		"result = set([1, 2]) <= set([1, 2, 3])\n",
		"result = [[1, 2], [3]] == [[1, 2], [3]]\n",
		"result = 'a' < 'b' and 1 < 2.5\n",
		"result = None == None\n",
		"result = [1, 2, 3].index(2) + [1, 2, 3].count(2) if hasattr([], 'count') else [1, 2, 3].index(2)\n",
		"result = (1, [2]) == (1, [2])\n",
		"result = 'x' in {'x': 1} and 3 in [1, 2, 3] and 'b' in 'abc' and 2 in (1, 2) and 3 in range(5)\n",
	)
	return out
}

func randomCorpus(n int, seed int64) []string {
	rng := rand.New(rand.NewSource(seed))
	pick := func(p []string) string { return p[rng.Intn(len(p))] }
	lit := func() string {
		switch rng.Intn(9) {
		case 0:
			return pick(pInts)
		case 1:
			return pick(pStrs)
		case 2:
			return pick(pLists)
		case 3:
			return pick(pTuples)
		case 4:
			return pick(pDicts)
		case 5:
			return pick(pSets)
		case 6:
			return pick(pFloats)
		case 7:
			return pick(pBytes)
		default:
			return pick(pOthers)
		}
	}
	fns := []string{"len", "str", "repr", "list", "tuple", "sorted", "reversed", "set", "dict", "bool", "type", "max", "min", "any", "all", "enumerate", "zip", "abs", "int", "float", "hash", "dir"}
	methods := []string{".append(1)", ".extend([2])", ".pop()", ".index(1)", ".count('a')", ".find('a')", ".split(',')", ".join(['a', 'b'])", ".strip()", ".upper()", ".replace('a', 'b')", ".items()", ".keys()", ".values()", ".get('a')", ".add(1)", ".union([1])", ".update({'a': 1})", ".startswith('a')", ".format(1)", ".insert(0, 5)", ".remove(1)", ".clear()", ".setdefault('k', 1)", ".popitem()"}
	binops := []string{"+", "-", "*", "==", "!=", "<", ">=", "in", "|", "&", "%", "//"}
	var out []string
	for i := 0; i < n; i++ {
		var b strings.Builder
		nst := 1 + rng.Intn(4)
		vars := []string{}
		for s := 0; s < nst; s++ {
			v := fmt.Sprintf("v%d", s)
			switch rng.Intn(6) {
			case 0:
				fmt.Fprintf(&b, "%s = %s\n", v, lit())
			case 1:
				fmt.Fprintf(&b, "%s = %s(%s)\n", v, pick(fns), lit())
			case 2:
				fmt.Fprintf(&b, "%s = (%s) %s (%s)\n", v, lit(), pick(binops), lit())
			case 3:
				fmt.Fprintf(&b, "%s = %s%s\n", v, lit(), pick(methods))
			case 4:
				fmt.Fprintf(&b, "%s = [x for x in %s]\n", v, lit())
			default:
				fmt.Fprintf(&b, "%s = 0\nfor i in range(%d):\n  %s += i\n", v, rng.Intn(8), v)
			}
			vars = append(vars, v)
		}
		fmt.Fprintf(&b, "result = %s\n", pick(vars))
		out = append(out, b.String())
	}
	return out
}

// bigCorpus are programs with operands over the free window: the results must
// be those of v0.2.0, the steps at least as many.
func bigCorpus() []string {
	var out []string
	big := []string{"[i for i in range(2000)]", "list(range(500))", "'ab' * 3000", "'a,' * 1500", "{i: i for i in range(800)}", "set(range(800))", "tuple(range(700))"}
	for _, b := range big {
		for _, tail := range []string{"len(x)", "(1999 in x)", "(-1 in x)", "x == x", "list(x)", "sorted(x)", "str(x)", "repr(x)", "x[::2]", "x + x", "set(x)", "min(x)", "max(x)", "any(x)", "all(x)", "reversed(x)", "list(enumerate(x))", "list(zip(x, x))", "len(dict(zip(x, x)))"} {
			out = append(out, "x = "+b+"\nresult = "+tail+"\n")
		}
	}
	for _, c := range []string{
		"x = 'ab' * 3000\nresult = len(x.upper()) + len(x.lower()) + len(x.replace('a', 'zz')) + len(x.split('b')) + x.count('a') + x.find('c') + len(x.title())\n",
		"x = 'a,' * 1500\nresult = (len(x.split(',')), len(x.split(',', 5)), len(x.rsplit(',', 3)), len(x.splitlines()), len(x.partition(',')[2]), len(x.strip(',')))\n",
		"x = ['a'] * 3000\nresult = len(','.join(x))\n",
		"x = list(range(3000))\nx.insert(0, 1)\nx.pop(0)\nx.remove(2999)\nresult = (x.index(2000), len(x))\n",
		"x = list(range(3000))\ny = list(x)\nresult = (x == y, x < y, x != y)\n",
		"x = [[i, i] for i in range(300)]\ny = [[i, i] for i in range(300)]\nresult = x == y\n",
		"d = {}\nfor i in range(300):\n  d[i] = i\nresult = (len(d), 299 in d, d.get(5), len(d.items()), len(d.keys()), len(d.values()))\n",
		"d = {str(i): i for i in range(300)}\ne = dict(d)\nresult = (d == e, len(d | e))\n",
		"s = set(range(500))\nt = set(range(250, 750))\nresult = (len(s | t), len(s & t), len(s - t), len(s ^ t), s <= t, len(s.union(t)))\n",
		"x = 1\nfor i in range(40):\n  x = x * 123456789\nresult = (x % 1000003, x // 1000003 > 0, str(x)[:5], len(str(x)))\n",
		"result = int('9' * 2000) % 1000003\n",
		"x = 3 ** 1 if False else 1\nfor i in range(60):\n  x = x * 1000003\nresult = (x < x * 3, x == x + 0, (x << 5) >> 5 == x)\n",
		"t = (1,)\nfor i in range(10):\n  t = (t, t)\nresult = len({t: 1})\n",
		"result = '%s' % ('x' * 5000) == 'x' * 5000\n",
		"result = len('{}{}'.format('a' * 3000, 'b' * 3000))\n",
		"result = sorted([(i * 7919) % 2003 for i in range(2000)])[:5]\n",
		"result = len(sorted([str(i) for i in range(1500)], key=len))\n",
		"x = [i for i in range(1000)]\nresult = sum_(x)\n",
	} {
		out = append(out, c)
	}
	return out
}

func uniq(a []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

const (
	goldenSmall = "testdata/diff_v020.golden"
	goldenBig   = "testdata/diff_big_v020.golden"
)

func smallCorpus() []string {
	return uniq(append(deterministicCorpus(), randomCorpus(3000, 7)...))
}

func writeGolden(t *testing.T, path string, progs []string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	for _, p := range progs {
		o := runDiffProgram(p)
		b, _ := json.Marshal(o)
		w.Write(b)
		w.WriteByte('\n')
	}
}

func readGolden(t *testing.T, path string) []diffOutcome {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []diffOutcome
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var o diffOutcome
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

// TestWriteDiffGolden writes the golden files. It is run in a tree of v0.2.0,
// not here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark package>.
func TestWriteDiffGolden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden files (in a tree of v0.2.0)")
	}
	writeGolden(t, dir+"/"+goldenSmall, smallCorpus())
	writeGolden(t, dir+"/"+goldenBig, uniq(bigCorpus()))
}

// Programs whose operands are small behave exactly as in v0.2.0: result, error
// text, prints and steps.
func TestDifferentialAgainstV020(t *testing.T) {
	golden := readGolden(t, goldenSmall)
	if len(golden) < 3583 {
		t.Fatalf("the golden file has %d programs, want at least 3583", len(golden))
	}
	// A program of v0.2.0 that panicked, or whose behavior is changed on
	// purpose, is not compared.
	changed := func(g diffOutcome) bool {
		return g.Status == "panic"
	}
	var bad []string
	compared := 0
	for _, g := range golden {
		if changed(g) {
			continue
		}
		compared++
		got := runDiffProgram(g.Src)
		if got.Status != g.Status || got.Result != g.Result || got.Err != g.Err || got.Prints != g.Prints || got.Steps != g.Steps {
			cut := func(s string) string {
				if len(s) > 120 {
					return s[:120] + "..."
				}
				return s
			}
			bad = append(bad, fmt.Sprintf("%q\n   v0.2.0: %s %q %q steps=%d\n   now:    %s %q %q steps=%d", cut(g.Src), g.Status, cut(g.Result), cut(g.Err), g.Steps, got.Status, cut(got.Result), cut(got.Err), got.Steps))
			if len(bad) >= 15 {
				break
			}
		}
	}
	if compared < 3500 {
		t.Errorf("only %d programs compared", compared)
	}
	if len(bad) > 0 {
		t.Fatalf("%d programs differ from v0.2.0 (showing %d):\n%s", len(bad), len(bad), strings.Join(bad, "\n"))
	}
	t.Logf("%d programs: results, errors, prints and steps identical to v0.2.0", compared)
}

// Programs with large operands: the same result, error, and steps.
func TestDifferentialLargeOperands(t *testing.T) {
	golden := readGolden(t, goldenBig)
	type row struct {
		src        string
		was, steps uint64
	}
	var rows []row
	for _, g := range golden {
		got := runDiffProgram(g.Src)
		if g.Status == "panic" {
			continue
		}
		if got.Status != g.Status || got.Result != g.Result || got.Err != g.Err {
			// (the sum_ program is an error in both)
			t.Errorf("%.200q\n   v0.2.0: %s %.100q %.100q\n   now:    %s %.100q %.100q", g.Src, g.Status, g.Result, g.Err, got.Status, got.Result, got.Err)
			continue
		}
		if got.Steps != g.Steps {
			t.Errorf("%q: %d steps, v0.2.0 had %d", g.Src, got.Steps, g.Steps)
			rows = append(rows, row{g.Src, g.Steps, got.Steps})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].steps-rows[i].was > rows[j].steps-rows[j].was })
	t.Logf("%d of %d programs take other steps than in v0.2.0", len(rows), len(golden))
	for i, r := range rows {
		if i >= 40 {
			break
		}
		src := strings.ReplaceAll(strings.TrimSpace(r.src), "\n", "; ")
		if len(src) > 90 {
			src = src[:90] + "..."
		}
		t.Logf("%-95s v0.2.0 %7d  now %9d", src, r.was, r.steps)
	}
}
