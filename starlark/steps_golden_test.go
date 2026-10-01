package starlark

// A table of the steps that operations on large operands cost, in
// testdata/steps.golden. The steps are deterministic, so the table is exact:
// a price that is changed, or dropped, changes a line, and the change is made
// on purpose (STARLARK_WRITE_STEPS=1 go test -run TestStepsGolden writes it).
// Every operation of the table is linear in its operand, except those that say
// they stop at once or are a lookup, and must cost steps in proportion to it,
// so a price that is free is also found without the file.

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

const stepsGoldenFile = "testdata/steps.golden"

// stepsSetup is the data the operations work on: N elements, 1 MiB of text.
const stepsSetup = `
N = 100000
l = list(range(N))
m = list(range(N))
t = tuple(l)
st = set(l)
d = {}
for i in range(N // 10):
    d[i] = i
e = dict(d)
s = 'ab' * (1 << 19)
w = 'a' * (1 << 20)
sp = 'ab ' * (1 << 18)
nl = 'ab\n' * (1 << 18)
big = 1 << 511
for i in range(9):
    big = big * big
big2 = big - 1
bn = 1 << 511
for i in range(4):
    bn = bn * bn
bm = 1 << 511
for i in range(7):
    bm = bm * bm
bigl = [bn] * 10
ll = [l[:1000] for i in range(100)]
mm = [l[:1000] for i in range(100)]
strs = ['x' * 1000 for i in range(100)]
bk = [bytes(x) for x in strs]
tl = [t] * 5
bigs = [big] * 20
`

var stepsOps = []struct{ name, op string }{
	{"d.items()", "r = d.items()"},
	{"d.keys()", "r = d.keys()"},
	{"d.values()", "r = d.values()"},
	{"d.clear()", "d.clear()"},
	{"l.clear()", "l.clear()"},
	{"st.clear()", "st.clear()"},
	{"d == e", "r = d == e"},
	{"d | e", "r = d | e"},
	{"dict(d)", "r = dict(d)"},
	{"l.insert(0, 1)", "l.insert(0, 1)"},
	{"l.pop(0)", "l.pop(0)"},
	{"l.remove(l[-1])", "l.remove(l[-1])"},
	{"l.extend(m)", "l.extend(m)"},
	{"l.index(l[-1])", "r = l.index(l[-1])"},
	{"list(l)", "r = list(l)"},
	{"tuple(l)", "r = tuple(l)"},
	{"reversed(l)", "r = reversed(l)"},
	{"enumerate(l)", "r = enumerate(l)"},
	{"zip(l, m)", "r = zip(l, m)"},
	{"sorted(l)", "r = sorted(l)"},
	{"sorted(reversed(l))", "r = sorted(reversed(l))"},
	{"max(l)", "r = max(l)"},
	{"min(l)", "r = min(l)"},
	{"any(l[:1]) (stops at once)", "r = any(l[:1])"},
	{"all(l[1:])", "r = all(l[1:])"},
	{"set(l)", "r = set(l)"},
	{"st | st", "r = st | st"},
	{"st & st", "r = st & st"},
	{"st == st2", "r = st == set(m)"},
	{"st.issubset(m)", "r = st.issubset(m)"},
	{"st.union(m)", "r = st.union(m)"},
	{"-1 in l", "r = -1 in l"},
	{"-1 in t", "r = -1 in t"},
	{"-1 in st (a lookup)", "r = -1 in st"},
	{"l == m", "r = l == m"},
	{"l < m", "r = l < m"},
	{"t == tuple(m)", "r = t == tuple(m)"},
	{"ll == mm (nested)", "r = ll == mm"},
	{"strs == strs2 (equal long strings)", "r = strs == [x for x in strs]"},
	{"l + m", "r = l + m"},
	{"l * 3", "r = l * 3"},
	{"l[::-1]", "r = l[::-1]"},
	{"str(l)", "r = str(l)"},
	{"repr(bigl)", "r = repr(bigl)"},
	{"str(bn)", "r = str(bn)"},
	{"big + 1", "r = big + 1"},
	{"big - big2", "r = big - big2"},
	{"big & big2", "r = big & big2"},
	{"bm * bm", "r = bm * bm"},
	{"abs(-big)", "r = abs(-big)"},
	{"hash(s)", "r = hash(s)"},
	{"float(digits)", "r = float('0.' + '1' * 100000)"},
	{"bytes(s)", "r = bytes(s)"},
	{"bytes(l)", "r = bytes([x % 256 for x in l[:1000]])"},
	{"s.count('a')", "r = s.count('a')"},
	{"s.find('c')", "r = s.find('c')"},
	{"s.rfind('c')", "r = s.rfind('c')"},
	{"s.index('ab') (a match at once)", "r = s.index('ab')"},
	{"s.partition('c')", "r = s.partition('c')"},
	{"s.rpartition('c')", "r = s.rpartition('c')"},
	{"'c' in s", "r = 'c' in s"},
	{"s.startswith", "r = s.startswith(w[:100000])"},
	{"s.endswith", "r = s.endswith(w[:100000])"},
	{"s.upper()", "r = s.upper()"},
	{"s.lower()", "r = s.lower()"},
	{"s.title()", "r = s.title()"},
	{"s.capitalize()", "r = s.capitalize()"},
	{"s.isalpha()", "r = s.isalpha()"},
	{"s.isalnum()", "r = s.isalnum()"},
	{"s.isdigit()", "r = s.isdigit()"},
	{"s.islower()", "r = s.islower()"},
	{"s.isupper()", "r = s.isupper()"},
	{"s.isspace()", "r = s.isspace()"},
	{"s.istitle()", "r = s.istitle()"},
	{"(' ' * N + 'x').strip()", "r = (' ' * 500000 + 'x').strip()"},
	{"lstrip", "r = (' ' * 500000 + 'x').lstrip()"},
	{"rstrip", "r = ('x' + ' ' * 500000).rstrip()"},
	{"s.removeprefix", "r = s.removeprefix(s[:100000])"},
	{"s.removesuffix", "r = s.removesuffix(s[-100000:])"},
	{"s.replace('a', 'cc')", "r = s.replace('a', 'cc')"},
	{"s.replace('c', 'd')", "r = s.replace('c', 'd')"},
	{"sp.split()", "r = sp.split()"},
	{"sp.split(' ')", "r = sp.split(' ')"},
	{"sp.rsplit(' ')", "r = sp.rsplit(' ')"},
	{"nl.splitlines()", "r = nl.splitlines()"},
	{"' '.join(strs)", "r = ' '.join(strs)"},
	{"'{}'.format(s)", "r = '{}'.format(s)"},
	{"'%s' % s", "r = '%s' % s"},
	{"s + s", "r = s + s"},
	{"s * 3", "r = s * 3"},
	{"s == w", "r = s == s[:-1] + 'b'"},
	{"s < w", "r = s < w"},
	{"set(strs) (hash of long strings)", "r = set(strs)"},
	{"set(bk) (hash of long bytes)", "r = set(bk)"},
	{"set(tl) (hash of long tuples)", "r = set(tl)"},
	{"set(bigs) (hash of big integers)", "r = set(bigs)"},
	{"print(s)", "print(s)"},
	{"fail(s)", "fail(s)"},
}

func runSteps(t *testing.T, op string) (uint64, error) {
	t.Helper()
	th := &Thread{Name: "steps"}
	th.Print = func(*Thread, string) {}
	var base uint64
	extra := StringDict{"reset": NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		base = th.Steps
		return None, nil
	})}
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", stepsSetup+"\nreset()\n"+op+"\n", extra)
	return th.Steps - base, err
}

func TestStepsGolden(t *testing.T) {
	var got strings.Builder
	steps := map[string]uint64{}
	for _, o := range stepsOps {
		n, err := runSteps(t, o.op)
		if err != nil && o.name != "fail(s)" {
			t.Errorf("%s: %v", o.name, err)
			continue
		}
		steps[o.name] = n
		fmt.Fprintf(&got, "%s\t%d\n", o.name, n)
		if exempt := strings.Contains(o.name, "at once") || strings.Contains(o.name, "a lookup"); n < 50 && !exempt {
			t.Errorf("%s: %d steps for a linear operation on a large operand", o.name, n)
		}
	}
	if os.Getenv("STARLARK_WRITE_STEPS") != "" {
		if err := os.WriteFile(stepsGoldenFile, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(stepsGoldenFile)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := strings.Split(strings.TrimSpace(string(want)), "\n")
	gotLines := strings.Split(strings.TrimSpace(got.String()), "\n")
	if len(wantLines) != len(gotLines) {
		t.Errorf("%d operations, the file has %d", len(gotLines), len(wantLines))
	}
	for i := 0; i < len(wantLines) && i < len(gotLines); i++ {
		if wantLines[i] != gotLines[i] {
			t.Errorf("steps changed:\n  was %s\n  now %s", wantLines[i], gotLines[i])
		}
	}
}
