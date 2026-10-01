package starlark

// The cost of a call with many parameters, locals or keyword arguments: charged
// by what it makes, and not the product of the number of arguments and of
// parameters.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

func paramList(n int) string {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, fmt.Sprintf("p%d=0", i))
	}
	return strings.Join(ps, ", ")
}

func countScans(t *testing.T, src string) (scans map[string]int, err error) {
	t.Helper()
	scans = map[string]int{}
	scanHook = func(kind string, n int) { scans[kind] += n }
	defer func() { scanHook = nil }()
	_, err = ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, &Thread{}, "t.star", src, nil)
	return scans, err
}

// f(**d) with many keys onto a function with many parameters looks each name up
// in a table: no linear search of the parameters.
func TestCalls_KeywordArgumentsAreLookedUpInATable(t *testing.T) {
	src := "def f(" + paramList(2000) + "): return p0\nd = {'p%d' % i: i for i in range(2000)}\nr = f(**d)\n"
	scans, err := countScans(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if scans["param"] != 0 {
		t.Errorf("%d parameters were looked at for 2000 keyword arguments of a function of 2000", scans["param"])
	}
	// A few keyword arguments onto many parameters: a search of the parameters
	// for each, bounded by the number of arguments (eight or fewer) times the
	// number of parameters.
	src = "def f(" + paramList(2000) + "): return p0\nr = f(p1999=1, p3=2, p1000=3)\n"
	scans, err = countScans(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if scans["param"] == 0 || scans["param"] > 8*2000 {
		t.Errorf("%d parameters looked at for 3 keyword arguments", scans["param"])
	}
	// The result is the same: the error of a name that is not a parameter, and of one given twice.
	for _, c := range []struct{ src, want string }{
		{"def f(" + paramList(20) + "): pass\nd = {'p%d' % i: i for i in range(20)}\nd['nope'] = 1\nf(**d)\n", "unexpected keyword argument"},
		{"def f(" + paramList(20) + "): pass\nd = {'p%d' % i: i for i in range(20)}\nf(1, **d)\n", "multiple values for parameter"},
	} {
		_, err := countScans(t, c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

// str.format(**d) with many keys: a table for the fields, not a search of the
// keys for each of them.
func TestCalls_FormatFieldsAreLookedUpInATable(t *testing.T) {
	src := "d = {'k%d' % i: 'v' for i in range(5000)}\nr = ('{k4999}{k0}{k77}' * 100).format(**d)\n"
	scans, err := countScans(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if scans["format"] != 0 {
		t.Errorf("%d keyword arguments were looked at by the fields", scans["format"])
	}
	// the first of two equal names wins, as before
	g, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true}, &Thread{}, "t.star",
		"r = '{a}'.format(a=1, b=2, c=3, d=4, e=5, f=6, g=7, h=8, i=9, j=10)\n", nil)
	if err != nil || g["r"] != String("1") {
		t.Errorf("%v %v", g["r"], err)
	}
}

// A call of a function with a large frame is charged, memory (the frame is
// garbage soon, but made at every call) and work, before the frame is made; a
// function with an ordinary frame is not.
func TestCalls_LargeFramesAreCharged(t *testing.T) {
	const params = 400
	src := "def f(" + paramList(params) + "): pass\nmark()\nf()\nmark()\n"
	r := runProg(t, 0, src)
	if r.err != nil {
		t.Fatal(r.err)
	}
	nspace := uint64(params + 1) // the parameters and the operand stack
	if got := r.marks[1] - r.marks[0]; got < nspace*allocBytesPerValue {
		t.Errorf("a call of a function of %d parameters was charged %d bytes, want at least %d", params, got, nspace*allocBytesPerValue)
	}
	// An ordinary function: nothing.
	r = runProg(t, 0, "def f(a, b, c, d=1, e=2): return a\nmark()\nf(1, 2, 3)\nmark()\n")
	if got := r.marks[1] - r.marks[0]; got != 0 {
		t.Errorf("a call of an ordinary function was charged %d bytes", got)
	}
	// 20000 parameters, called in a loop: refused by the budget after a few
	// calls (each makes 320 KB), not after 6 GB of garbage.
	src = "def f(" + paramList(20000) + "): pass\nfor i in range(20000):\n    trace(i)\n    f()\n"
	r = runProg(t, 64*budgetMiB, src)
	var be *AllocBudgetError
	if !errors.As(r.err, &be) {
		t.Fatalf("err = %v", r.err)
	}
	if n := len(r.traced); n < 150 || n > 250 {
		t.Errorf("%d calls of 20000 parameters before the refusal of 64 MiB, want about 200", n)
	}
	// And the work of a call grows with the frame.
	w := func(n int) uint64 {
		th := &Thread{}
		src := "def f(" + paramList(n) + "): pass\nf()\n"
		if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", src, nil); err != nil {
			t.Fatal(err)
		}
		return th.Work() - th.Steps
	}
	if a, b := w(1000), w(10000); b < 8*a/2 {
		t.Errorf("work of a call: %d for 1000 parameters, %d for 10000", a, b)
	}
}
