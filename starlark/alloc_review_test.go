package starlark

// Tests of the findings of the review of v0.3.0: crashes (stack overflow,
// panics), memory that was not charged, false charges and false refusals.

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// ---- A1, A3: deep nesting must not overflow the Go stack ----

// A stack overflow is a fatal error, not a panic: it kills the process and
// every other test with it. So the deep values are built and walked in a child
// process, with a small stack limit, and the parent checks that the child
// finished. The values are 60000 levels deep, far past MaxValueDepth, and
// would need ~40 MiB of stack (a walk of MaxValueDepth levels needs ~8 MiB) to recurse; the child's limit is 24 MiB.
func TestDeepNesting_ChildProcess(t *testing.T) {
	if os.Getenv("STARLARK_DEEP_CHILD") == "1" {
		debug.SetMaxStack(24 << 20)
		runDeepNestingBattery(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeepNesting_ChildProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "STARLARK_DEEP_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[:1000] + "\n...\n" + tail[len(tail)-1000:]
		}
		t.Fatalf("the child process failed: %v\n%s", err, tail)
	}
	if !strings.Contains(string(out), "DEEP-BATTERY-OK") {
		t.Fatalf("the child did not finish the battery:\n%s", out)
	}
}

func runDeepNestingBattery(t *testing.T) {
	const depth = 60000
	deepTuple := func() Value {
		var v Value = Tuple{}
		for i := 0; i < depth; i++ {
			v = Tuple{v}
		}
		return v
	}
	deepList := func() Value {
		v := NewList(nil)
		for i := 0; i < depth; i++ {
			v = NewList([]Value{v})
		}
		return v
	}
	deepDict := func() Value {
		var v Value = new(Dict)
		for i := 0; i < depth; i++ {
			d := new(Dict)
			d.SetKey(String("k"), v)
			v = d
		}
		return v
	}
	th := &Thread{}
	for _, c := range []struct {
		name string
		v    Value
	}{{"tuple", deepTuple()}, {"list", deepList()}, {"dict", deepDict()}} {
		for _, fn := range []string{"str", "repr"} {
			_, err := Call(th, Universe[fn], Tuple{c.v}, nil)
			if err == nil || !strings.Contains(err.Error(), "nested more than") {
				t.Errorf("%s(%s) = %v, want a nesting error", fn, c.name, err)
			}
		}
		var printed string
		th.Print = func(_ *Thread, msg string) { printed = msg }
		if _, err := Call(th, Universe["print"], Tuple{c.v}, nil); err == nil || !strings.Contains(err.Error(), "nested more than") {
			t.Errorf("print(%s) = %v (printed %d bytes)", c.name, err, len(printed))
		}
		if _, err := Call(th, Universe["fail"], Tuple{c.v}, nil); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
			t.Errorf("fail(%s) = %.80v, want a message cut at the nesting limit", c.name, err)
		}
		// The string form of the Go API (Value.String) is bounded and marked too.
		if s := c.v.String(); !strings.Contains(s, "nested too deeply") {
			t.Errorf("%s.String() has no nesting mark", c.name)
		}
		c.v.Freeze() // iterative: must not overflow the stack
	}
	// Hash of a deep tuple is an error, not a stack overflow.
	if _, err := deepTuple().Hash(); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Errorf("Hash of a deep tuple: %v", err)
	}
	d := new(Dict)
	if err := d.SetKey(deepTuple(), None); err == nil {
		t.Errorf("a deep tuple was accepted as a dict key")
	}
	// Equality and comparison already stop at CompareLimit.
	if _, err := Equal(deepTuple(), deepTuple()); err == nil {
		t.Errorf("Equal of deep tuples: want a depth error")
	}
	t.Log("DEEP-BATTERY-OK")
}

// The limit itself: a value nested MaxValueDepth levels is written, one
// level deeper is refused, and the constant is what the engine can rely on.
func TestDeepNesting_Limit(t *testing.T) {
	if MaxValueDepth != 10000 {
		t.Fatalf("MaxValueDepth = %d: the justification in value.go is for 10000 (7 MiB of stack)", MaxValueDepth)
	}
	nest := func(n int) Value {
		var v Value = MakeInt(1)
		for i := 0; i < n; i++ {
			v = NewList([]Value{v})
		}
		return v
	}
	th := &Thread{}
	if _, err := Call(th, Universe["str"], Tuple{nest(MaxValueDepth)}, nil); err != nil {
		t.Errorf("str at the limit: %v", err)
	}
	if _, err := Call(th, Universe["str"], Tuple{nest(MaxValueDepth + 1)}, nil); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Errorf("str one level past the limit: %v", err)
	}
	tup := func(n int) Tuple {
		v := Tuple{}
		for i := 0; i < n; i++ {
			v = Tuple{v}
		}
		return v
	}
	if _, err := tup(MaxValueDepth).Hash(); err != nil {
		t.Errorf("Hash at the limit: %v", err)
	}
	if _, err := tup(MaxValueDepth + 2).Hash(); err == nil {
		t.Errorf("Hash past the limit succeeded")
	}
	// The same through the script: the interpreter reports an error.
	r := runProg(t, 0, "t = ()\nfor i in range(10002): t = (t,)\ns = str(t)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "nested more than") {
		t.Errorf("script: %v", r.err)
	}
	for _, src := range []string{"repr(t)", "'%s' % (t,)", "'%r' % (t,)", "'{}'.format(t)", "'{!r}'.format(t)", "print(t)", "{t: 1}"} {
		r := runProg(t, 0, "t = ()\nfor i in range(10002): t = (t,)\nx = "+src+"\n")
		if r.err == nil || !strings.Contains(r.err.Error(), "nested more than") {
			t.Errorf("%s: err = %v", src, r.err)
		}
	}
}

// ---- A4: range slices overflow ----

func TestRangeSlice_OverflowIsAnError(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		// the product of the steps wraps to 0
		{"x = range(10)[::1 << 30][::1 << 30][::1 << 30]", "range slice is out of range"},
		// the product of the steps wraps to a non-zero number
		{"x = range(10)[::2147483647][::2147483647][::2147483647]", "range slice is out of range"},
		// the stop (start + step * end) overflows
		{"x = range(9223372036854775000, 9223372036854775807, 1000)[0:1]", "range slice is out of range"},
		// the length does not fit an int
		{"x = range(-(1 << 62), 1 << 62)", "range is too long"},
	} {
		r := runProg(t, 0, c.src)
		if r.err == nil || !strings.Contains(r.err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.src, r.err, c.want)
		}
	}
	// Legitimate slices of ranges are unchanged.
	r := runProg(t, 0, `
r = range(10)
if list(r[2:8:2]) != [2, 4, 6]: fail('forward')
if list(r[::-3]) != [9, 6, 3, 0]: fail('backward')
if list(range(10, 0, -2)[1:3]) != [8, 6]: fail('stepped')
if len(range(1 << 40)) != 1 << 40: fail('large')
if len(range(-(1 << 40), 1 << 40)) != 1 << 41: fail('wide')
`)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if n, ok := rangeLenChecked(-(1 << 62), 1<<62, 1); !ok || n != 1<<63-1+1-1 && n != math.MaxInt {
		// 2^63 elements do not fit an int: not ok
		if ok {
			t.Errorf("rangeLenChecked(-2^62, 2^62, 1) = %d, true", n)
		}
	}
}

// ---- A5: set methods need their argument ----

func TestSetMethods_RequireAnArgument(t *testing.T) {
	for _, m := range []string{"difference", "intersection", "issubset", "issuperset", "symmetric_difference"} {
		r := runProg(t, 0, "s = set([1])\nr = s."+m+"()\n")
		if r.err == nil || !strings.Contains(r.err.Error(), m) {
			t.Errorf("set.%s(): err = %v, want an argument error naming the method", m, r.err)
		}
	}
	// union and update take any number of iterables, including none.
	r := runProg(t, 0, "s = set([1])\nu = s.union()\nif u != s: fail('union()')\ns.update()\n")
	if r.err != nil {
		t.Error(r.err)
	}
}

// ---- A6: zip is bounded by columns x rows ----

func TestZip_ColumnsTimesRowsIsBounded(t *testing.T) {
	smallLimit(t)
	// 2000 columns of 1000 rows: 2 million slots, though no column is long.
	r := runProg(t, 0, "cols = [range(1000)] * 2000\nr = zip(*cols)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
		t.Errorf("zip of many columns: %v", r.err)
	}
	// With a budget: a budget error, and nothing charged by the zip.
	r = runProg(t, budgetMiB, "cols = [range(100)] * 1000\nmark()\nr = zip(*cols)\n")
	wantBudgetErr(t, r, budgetMiB)
}

func TestZip_ArithmeticOverflowIsRefused(t *testing.T) {
	// The size is rows * (columns of 16 + a tuple) and overflows uint64 when
	// rows = 2^62 and there are many columns; it is refused, not wrapped.
	r := runProg(t, budgetMiB, "cols = [range(1 << 62)] * 100\nr = zip(*cols)\n")
	wantBudgetErr(t, r, budgetMiB)
	r = runProg(t, 0, "cols = [range(1 << 62)] * 100\nr = zip(*cols)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
		t.Errorf("without a budget: %v", r.err)
	}
}

// ---- A7: maxsplit near MaxInt ----

func TestSplit_MaxsplitNearMaxIntIsStillChecked(t *testing.T) {
	const max64 = "9223372036854775807"
	for _, op := range []string{
		"s.split(',', " + max64 + ")",
		"s.split(',', " + max64 + " - 1)",
		"s.split(None, " + max64 + ")",
		"s.split(None, " + max64 + " - 1)",
		"s.rsplit(',', " + max64 + ")",
		"s.rsplit(',', " + max64 + " - 1)",
		"s.rsplit(None, " + max64 + ")",
		"s.rsplit(None, " + max64 + " - 1)",
	} {
		sep := "'a,' * 200000"
		if strings.Contains(op, "None") {
			sep = "'a ' * 200000"
		}
		r := runProg(t, budgetMiB, "s = "+sep+"\nmark()\nr = "+op+"\n")
		if r.err == nil {
			t.Errorf("%s succeeded", op)
			continue
		}
		wantBudgetErr(t, r, budgetMiB)
		if spent := r.memEnd - r.memMark; spent > 64<<10 {
			t.Errorf("%s allocated %d bytes before it was refused", op, spent)
		}
	}
}

// ---- B1: containers and functions are charged where they are made ----

func TestAllocBudget_EmptyContainersAreNotFree(t *testing.T) {
	// 512 bytes for each {} of a million would be 512 MB at no charge: the
	// loop is stopped by the budget at the iteration the formula gives.
	r := runProg(t, budgetMiB, "keep = []\nfor i in range(100000):\n  trace(i)\n  keep.append({})\n")
	wantBudgetErr(t, r, budgetMiB)
	// 48 for the list, then 512 for each dict: 48 + 512*k <= 1048576.
	want := (budgetMiB - 48) / 512
	if got := r.traced[len(r.traced)-1]; got != want {
		t.Errorf("refused at iteration %d, want %d", got, want)
	}
	for _, c := range []struct{ name, op string }{
		{"set()", "keep.append(set())"},
		{"[]", "keep.append([])"},
		{"{1: 2}", "keep.append({1: 2})"},
		{"lambda", "keep.append(lambda: i)"},
		{"dict()", "keep.append(dict())"},
		{"tuple", "keep.append((i, i))"},
		{"dict comprehension", "keep.append({j: j for j in range(1)})"},
	} {
		r := runProg(t, budgetMiB, "keep = []\nfor i in range(100000):\n  trace(i)\n  "+c.op+"\n")
		wantBudgetErr(t, r, budgetMiB)
		if len(r.traced) > 30000 {
			t.Errorf("%s: %d iterations before the refusal", c.name, len(r.traced))
		}
	}
}

// ---- B2: every in-place form is charged ----

func TestInPlaceForms_AreCharged(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		min             uint64 // at least this many bytes
	}{
		{"d |= e", "d = {1: 1}\ne = {}\nfor i in range(100): e[i + 10] = i", "d |= e", 94 * 128}, // (100 new keys, past the inline bucket)
		{"s |= t", "s = set([1])\nt = set(range(100))", "s |= t", 90 * 128},
		{"s &= t", "s = set(range(100))\nt = set(range(100))", "s &= t", 90 * 128},
		{"s -= t", "s = set(range(100))\nt = set([1])", "s -= t", 90 * 128},
		{"s ^= t", "s = set(range(100))\nt = set(range(50, 150))", "s ^= t", 100 * 128},
		{"l += l", "l = [1] * 100", "l += l", 100 * 16},
		{"l += t", "l = [1]\nt = (1,) * 100", "l += t", 100 * 16},
		{"l *= n", "l = [1] * 100", "l *= 3", 300 * 16},
		{"t += t", "t = (1,) * 100", "t += t", 200 * 16},
		{"t *= n", "t = (1,) * 100", "t *= 3", 300 * 16},
		{"s += s", "s = 'a' * 100", "s += s", 200},
		{"s *= n", "s = 'a' * 100", "s *= 3", 300},
		{"b += b", "b = b'a' * 100", "b += b", 0}, // bytes + bytes is not an operation
	} {
		r := runProg(t, 0, c.setup+"\nmark()\n"+c.op+"\nmark()\n")
		if c.name == "b += b" {
			if r.err == nil {
				t.Errorf("%s: bytes + bytes should fail", c.name)
			}
			continue
		}
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.marks[1] - r.marks[0]; got < c.min {
			t.Errorf("%s: charged %d bytes, want at least %d", c.name, got, c.min)
		}
	}
}

// ---- B3: iterables of unknown length ----

func TestUnknownLengthIterables_AreChargedPerElement(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"set.update(codepoints)", "s = set()", "s.update('abc'.codepoints())", 0}, // codepoints have a Len: charged up front
		{"set.update(lazy)", "s = set()", "s.update(lazy(3))", 3 * 128},
		{"set.union(lazy)", "s = set([1, 2, 3])", "r = s.union(lazy(3))", db(3) + 3*128},
		{"set.symmetric_difference(lazy)", "s = set([1, 2, 3])", "r = s.symmetric_difference(lazy(3))", db(3) + 3*128},
		{"join(lazy_str)", "", "r = ','.join(lazy_str(3))", 3 * 16},
	} {
		r := runProgWith(t, 0, c.setup+"\nmark()\n"+c.op+"\nmark()\n", lazyBuiltins())
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		if got := r.marks[1] - r.marks[0]; c.want != 0 && got != c.want {
			t.Errorf("%s: charged %d, want %d", c.name, got, c.want)
		}
	}
}

// ---- B4: no peak after the allocation; values in error messages are cut ----

func TestPeakBeforeAllocation_StringFamily(t *testing.T) {
	const budget = 24 << 20
	nul := "s = '\\x00' * (16 << 20)\n" // 16 MiB; its quoted form is 64 MiB
	invalid := "b = b'\\xff' * (8 << 20)\n"
	for _, c := range []struct{ name, setup, op string }{
		{"repr(s)", nul, "r = repr(s)"},
		{"str([s])", nul, "x = [s]\nmark()\nr = str(x)"},
		{"%r", nul, "r = '%r' % s"},
		{"{!r}", nul, "r = '{!r}'.format(s)"},
		{"print([s])", nul, "x = [s]\nmark()\nprint(x)"},
		{"str(bytes)", invalid, "r = str(b)"},
		{"repr(bytes)", invalid, "r = repr(b)"},
	} {
		r := runProg(t, budget, c.setup+"mark()\n"+c.op+"\n")
		if !strings.Contains(c.op, "mark()") && len(r.marks) == 0 {
			t.Fatalf("%s: no mark", c.name)
		}
		wantBudgetErr(t, r, budget)
		if spent := r.memEnd - r.memMark; spent > 1<<20 {
			t.Errorf("%s: the refused operation allocated %d bytes (want under 1 MiB)", c.name, spent)
		}
	}
}

func TestValuesInErrorMessagesAreCut(t *testing.T) {
	const big = "s = 'x' * (4 << 20)\n"
	for _, c := range []struct{ name, op string }{
		{"d[s] KeyError", "d = {}\nr = d[s]"},
		{"int(s)", "r = int(s)"},
		{"float(s)", "r = float(s)"},
		{"getattr", "r = getattr([], s)"},
		{"x.attr", "x = []\nr = x." + "nope"}, // short name: control
		{"function keyword", "def f(): return 1\nr = f(**{s: 1})"},
		{"format keyword", "r = ('{' + s + '}').format()"},
		{"%(key)s", "r = ('%(' + s + ')s') % {}"},
		{"duplicate key literal", "r = {s: 1, s: 2}"},
		{"int range", "r = [1][s]"},
	} {
		r := runProg(t, 0, big+c.op+"\nmark()\n")
		if r.err == nil {
			if c.name != "x.attr" {
				t.Errorf("%s: succeeded", c.name)
			}
			continue
		}
		if len(r.err.Error()) > 1024 {
			t.Errorf("%s: the error message is %d bytes: %.120s...", c.name, len(r.err.Error()), r.err.Error())
		}
		if spent := r.memEnd - r.memMark; len(r.marks) > 0 && spent > 1<<20 {
			t.Errorf("%s: allocated %d bytes after the mark", c.name, spent)
		}
	}
	// errValue and errStr cut at the declared length, and mark the cut.
	if s := errStr(strings.Repeat("a", 1000)); len(s) > errValueLimit+20 || !strings.Contains(s, "...<1000 bytes>") {
		t.Errorf("errStr = %q", s)
	}
	if s := errValue(String(strings.Repeat("a", 1000))); len(s) > errValueLimit+64 {
		t.Errorf("errValue of a string: %d bytes", len(s))
	}
	if s := errValue(NewList([]Value{String(strings.Repeat("a", 1000)), MakeInt(1)})); len(s) > errValueLimit+64 {
		t.Errorf("errValue of a list: %d bytes", len(s))
	}
	if s := errValue(MakeInt(1)); s != "1" {
		t.Errorf("errValue(1) = %q", s)
	}
}

// ---- B6: *args and **kwargs of the callee are copies ----

func TestParameterCopies_AreCharged(t *testing.T) {
	r := runProg(t, budgetMiB, "def f(*a): return None\nl = [1] * 100\nfor i in range(100000):\n  trace(i)\n  f(*l)\n")
	wantBudgetErr(t, r, budgetMiB)
	if len(r.traced) > 3000 {
		t.Errorf("%d calls before the refusal", len(r.traced))
	}
	r = runProg(t, budgetMiB, "def f(**k): return None\nd = {'a': 1, 'b': 2}\nfor i in range(100000):\n  trace(i)\n  f(**d)\n")
	wantBudgetErr(t, r, budgetMiB)
	if len(r.traced) > 3000 {
		t.Errorf("%d calls before the refusal", len(r.traced))
	}
}

// ---- C1: built-ins that return an existing value are not charged ----

func TestExistingValues_AreNotChargedByCall(t *testing.T) {
	// 10000 d.get of a 10 KB value allocate nothing: before, they charged 100 MB.
	r := runProg(t, budgetMiB, "d = {'k': 'x' * 10000}\nmark()\nfor i in range(10000): v = d.get('k')\nmark()\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.marks[1] - r.marks[0]; got != 0 {
		t.Errorf("10000 x d.get charged %d bytes", got)
	}
	// A bound method keeps the mark.
	r = runProg(t, budgetMiB, "d = {'k': [0] * 5000}\nf = d.get\nmark()\nfor i in range(1000): v = f('k')\nmark()\n")
	if r.err != nil || r.marks[1]-r.marks[0] != 0 {
		t.Errorf("bound d.get: err %v, charged %d", r.err, r.marks[1]-r.marks[0])
	}
	// The values a host built-in returns are charged, whatever they are.
	th := &Thread{}
	existing := NewList(make([]Value, 1000))
	host := NewBuiltin("host_get", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) { return existing, nil })
	if _, err := Call(th, host, nil, nil); err != nil || th.AllocatedBytes() != lb(1000) {
		t.Errorf("host built-in: err %v, charged %d, want %d", err, th.AllocatedBytes(), lb(1000))
	}
	// A host cannot mark its built-in: the field is not exported, and a bound
	// copy of a host built-in is not marked either.
	bound := host.BindReceiver(None)
	if bound.price != nil {
		t.Errorf("a bound host built-in is marked")
	}
}

// The set of built-ins that Call does not charge is closed and declared: a new
// built-in is charged by Call unless it is added here and to the tables.
func TestAccountedBuiltins_AreTheDeclaredSet(t *testing.T) {
	want := map[string]bool{
		"bytes": true, "getattr": true, "max": true, "min": true, "str": true, "type": true, // universe
		"dict.get": true, "dict.pop": true, "dict.setdefault": true,
		"list.pop": true, "set.pop": true,
		"string.strip": true, "string.lstrip": true, "string.rstrip": true,
		"string.removeprefix": true, "string.removesuffix": true,
		"string.lower": true, "string.upper": true, "string.replace": true,
	}
	got := map[string]bool{}
	for name, v := range Universe {
		if b, ok := v.(*Builtin); ok && b.price != nil && b.price.accounts {
			got[name] = true
		}
	}
	for prefix, tbl := range map[string]map[string]*Builtin{"dict.": dictMethods, "list.": listMethods, "set.": setMethods, "string.": stringMethods, "bytes.": bytesMethods} {
		for name, b := range tbl {
			if b.price != nil && b.price.accounts {
				got[prefix+name] = true
			}
		}
	}
	for n := range want {
		if !got[n] {
			t.Errorf("%s should be accounted", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("%s is accounted but not declared", n)
		}
	}
}

// ---- C2: replace(old, new, 0) replaces nothing ----

func TestReplace_CountZeroIsNotRefused(t *testing.T) {
	// A full replacement of this would be 4 MB; with count 0 nothing happens.
	r := runProg(t, budgetMiB, "s = 'a' * 2000\nt = 'b' * 2000\nmark()\nr = s.replace('a', t, 0)\nmark()\nif r != s: fail('changed')\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.marks[1] - r.marks[0]; got != 0 {
		t.Errorf("charged %d", got)
	}
	// count 1: one replacement, 2000 + 1999.
	r = runProg(t, budgetMiB, "s = 'a' * 2000\nt = 'b' * 2000\nmark()\nr = s.replace('a', t, 1)\nmark()\n")
	if r.err != nil || r.marks[1]-r.marks[0] != 3999 {
		t.Errorf("count 1: err %v, charged %d", r.err, r.marks[1]-r.marks[0])
	}
}

// ---- D1: the ceiling, in bytes, without allocating ----

func TestAllocCeiling_IsOneGiBInBytes(t *testing.T) {
	if maxAlloc != 1<<30 {
		t.Fatalf("maxAlloc = %d, want 1<<30", maxAlloc)
	}
}

func TestAllocCeiling_IsInBytesForEveryContainer(t *testing.T) {
	smallLimit(t) // 64 KiB
	// 5000 elements are 80 KB as a list or tuple, far below 65536 elements.
	for _, src := range []string{
		"r = list(range(5000))", "r = tuple(range(5000))", "r = [0] * 5000", "r = (0,) * 5000",
		"r = sorted(range(5000))", "r = reversed(range(5000))", "r = set(range(1000))",
		"r = dict(zip(range(1000), range(1000)))", "r = enumerate(range(1000))",
	} {
		r := runProg(t, 0, src)
		if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
			t.Errorf("%s: %v", src, r.err)
		}
	}
	// Just under: 4000 x 16 + 48 = 64048 < 65536.
	if r := runProg(t, 0, "r = list(range(4000))"); r.err != nil {
		t.Errorf("under the ceiling: %v", r.err)
	}
}

// What a regression of the ceiling does to the huge sizes is a makeslice panic
// (recoverable, so a FAIL), not an allocation: these ask Go for more than any
// platform can give.
func TestAllocCeiling_HugeSizesAreRefusedNotPanics(t *testing.T) {
	for _, src := range []string{
		"r = list(range(1 << 60))", "r = tuple(range(1 << 60))", "r = sorted(range(1 << 60))",
		"r = reversed(range(1 << 60))", "r = enumerate(range(1 << 60))", "r = bytes(range(1 << 60))",
		"r = zip(range(1 << 60), range(1 << 60))", "r = list(range(1 << 62))",
	} {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("%s: panic %v", src, p)
				}
			}()
			r := runProg(t, 0, src)
			if r.err == nil || !strings.Contains(r.err.Error(), "excessive") {
				t.Errorf("%s: err = %v", src, r.err)
			}
		}()
	}
}

// ---- D2: boundaries ----

func TestChargeAlloc_RequestForExactlyTheWholeBudget(t *testing.T) {
	th := &Thread{}
	th.SetMaxAllocBytes(100)
	if err := th.ChargeAlloc(100); err != nil {
		t.Fatalf("a request for the whole budget: %v", err)
	}
	th2 := &Thread{}
	th2.SetMaxAllocBytes(100)
	if err := th2.ChargeAlloc(101); err == nil {
		t.Fatal("a request past the whole budget was accepted")
	}
}

func TestStringLimit_ExactlyTheRemainingBudget(t *testing.T) {
	// str([s]) is len(s)+4 bytes; the setup charges len(s) and 64 for the
	// list: with budget 2068 and len(s) = 1000 the form is exactly what remains.
	for _, c := range []struct {
		n  int
		ok bool
	}{{1000, true}, {1001, false}} {
		src := "s = 'a' * " + itoa(c.n) + "\nx = [s]\nr = str(x)\n"
		r := runProg(t, 2068, src)
		if c.ok && r.err != nil {
			t.Errorf("n=%d: %v", c.n, r.err)
		}
		if !c.ok {
			wantBudgetErr(t, r, 2068)
		}
		if c.ok && r.th.AllocatedBytes() != 2068 {
			t.Errorf("n=%d: charged %d, want exactly the budget", c.n, r.th.AllocatedBytes())
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A refused list += iterator keeps the elements that were charged, no more.
func TestListExtend_StateAfterARefusal(t *testing.T) {
	var kept *List
	extra := StringDict{"keep": NewBuiltin("keep", func(_ *Thread, _ *Builtin, args Tuple, _ []Tuple) (Value, error) {
		kept = args[0].(*List)
		return None, nil
	})}
	// The budget holds x = [1] (48 + 16) and exactly 5 more elements of 32 bytes.
	const budget = 64 + 5*32
	r := runProgWith(t, budget, "x = [1]\nkeep(x)\nx += lazy(100)\n", mergeStringDicts(lazyBuiltins(), extra))
	wantBudgetErr(t, r, budget)
	if kept == nil || kept.Len() != 1+5 {
		t.Fatalf("the list holds %v elements after the refusal, want 6", kept)
	}
	if r.th.AllocatedBytes() > budget {
		t.Errorf("charged %d over the budget", r.th.AllocatedBytes())
	}
}

func mergeStringDicts(a, b StringDict) StringDict {
	out := StringDict{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ---- D3 ----

func TestChargeAlloc_ZeroOnAThreadWhoseBudgetWasLowered(t *testing.T) {
	th := &Thread{}
	if err := th.ChargeAlloc(100); err != nil {
		t.Fatal(err)
	}
	th.SetMaxAllocBytes(10) // below what is charged
	if err := th.ChargeAlloc(0); err != nil {
		t.Errorf("ChargeAlloc(0) = %v: n = 0 always succeeds", err)
	}
	var be *AllocBudgetError
	if err := th.ChargeAlloc(1); !errors.As(err, &be) {
		t.Errorf("ChargeAlloc(1) = %v, want a budget error", err)
	}
}
