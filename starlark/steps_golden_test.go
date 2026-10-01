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
const stepsSetupTemplate = `
N = $N
K = $K
l = list(range(N))
m = list(range(N))
t = tuple(l)
st = set(l)
d = {}
for i in range(N // 10):
    d[i] = i
e = dict(d)
lk = 'k' * 100000
dk = {lk: 1}
s = 'ab' * (K // 2)
w = 'a' * K
sp = 'ab ' * (K // 3)
nl = 'ab\n' * (K // 3)
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
bs = bytes(w)
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
	{"l.remove(l[0]) (the memmove)", "l.remove(l[0])"},
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
	{"'%d' % bn", "r = '%d' % bn"},
	{"big // bm", "r = big // bm"},
	{"big % bm", "r = big % bm"},
	{"big | big2", "r = big | big2"},
	{"big ^ big2", "r = big ^ big2"},
	{"big << 3", "r = big << 3"},
	{"big >> 3", "r = big >> 3"},
	{"b'c' in bs", "r = b'c' in bs"},
	{"big + 1", "r = big + 1"},
	{"big - big2", "r = big - big2"},
	{"big & big2", "r = big & big2"},
	{"bm * bm", "r = bm * bm"},
	{"abs(-big)", "r = abs(-big)"},
	{"hash(s)", "r = hash(s)"},
	{"float(digits)", "r = float('0.' + '1' * (K // 10))"},
	{"bytes(s)", "r = bytes(s)"},
	{"bytes(l)", "r = bytes([x % 256 for x in l[:1000]])"},
	{"s.count('a')", "r = s.count('a')"},
	{"s.find('c')", "r = s.find('c')"},
	{"s.rfind('c')", "r = s.rfind('c')"},
	{"s.index('ab') (a match at once)", "r = s.index('ab')"},
	{"s.partition('c')", "r = s.partition('c')"},
	{"s.rpartition('c')", "r = s.rpartition('c')"},
	{"'c' in s", "r = 'c' in s"},
	{"s.startswith", "r = s.startswith(w[:K // 10])"},
	{"s.endswith", "r = s.endswith(w[:K // 10])"},
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
	{"(' ' * N + 'x').strip()", "r = (' ' * (K // 2) + 'x').strip()"},
	{"lstrip", "r = (' ' * (K // 2) + 'x').lstrip()"},
	{"rstrip", "r = ('x' + ' ' * (K // 2)).rstrip()"},
	{"s.removeprefix", "r = s.removeprefix(s[:K // 10])"},
	{"s.removesuffix", "r = s.removesuffix(s[-K // 10:])"},
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
	{"l[N // 2] in l (found)", "r = l[N // 2] in l"},
	{"t[N // 2] in t (found)", "r = t[N // 2] in t"},
	{"st - st", "r = st - st"},
	{"st ^ st", "r = st ^ st"},
	{"d |= e", "d |= e"},
	{"long key in dict", "r = lk in dk"},
	{"long key in set", "r = lk in set([1])"},
	{"dk[long key]", "r = dk[lk]"},
	{"dk[long key] = 1", "dk[lk] = 2"},
	{"d.get(long key)", "r = dk.get(lk)"},
	{"'%(key)s' % dk", "r = ('%(' + lk + ')s') % dk"},
	{"set(strs) (hash of long strings)", "r = set(strs)"},
	{"set(bk) (hash of long bytes)", "r = set(bk)"},
	{"set(tl) (hash of long tuples)", "r = set(tl)"},
	{"set(bigs) (hash of big integers)", "r = set(bigs)"},
	{"print(s)", "print(s)"},
	{"fail(s)", "fail(s)"},
}

func runSteps(t *testing.T, setup, op string) (uint64, error) {
	t.Helper()
	th := &Thread{Name: "steps"}
	th.Print = func(*Thread, string) {}
	var base uint64
	extra := StringDict{"reset": NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		base = th.Steps
		return None, nil
	})}
	_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", setup+"\nreset()\n"+op+"\n", extra)
	return th.Steps - base, err
}

// The operations run at two scales: large, where every one is far past the
// free window and the chunk of a meter, and small, where the work of many is
// between the free window and one chunk and is charged by the last flush.
func TestStepsGolden(t *testing.T) {
	var got strings.Builder
	for _, scale := range []struct {
		name string
		n, k int
	}{{"", 100000, 1 << 20}, {"small: ", 2000, 8000}} {
		setup := strings.NewReplacer("$N", fmt.Sprint(scale.n), "$K", fmt.Sprint(scale.k)).Replace(stepsSetupTemplate)
		for _, o := range stepsOps {
			n, err := runSteps(t, setup, o.op)
			if err != nil && o.name != "fail(s)" {
				t.Errorf("%s%s: %v", scale.name, o.name, err)
				continue
			}
			fmt.Fprintf(&got, "%s%s\t%d\n", scale.name, o.name, n)
			if exempt := strings.Contains(o.name, "at once") || strings.Contains(o.name, "a lookup"); n < 50 && !exempt && scale.name == "" {
				t.Errorf("%s: %d steps for a linear operation on a large operand", o.name, n)
			}
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
