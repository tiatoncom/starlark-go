package starlark_test

// Tests of the static access to the branch table (Program.Funcodes): the table
// that a host reads without running anything is the one whose Branches the
// hooks report, for the toplevel and for nested def / lambda, and it survives
// Program.Write / CompiledProgram.

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// execTracedProgram compiles src, runs its toplevel on a new thread (no Trace
// yet) and returns the thread, the globals and the program.
func execTracedProgram(t *testing.T, src string) (*starlark.Thread, starlark.StringDict, *starlark.Program, error) {
	t.Helper()
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", src, starlark.StringDict(nil).Has)
	if err != nil {
		return nil, nil, nil, err
	}
	thread := &starlark.Thread{}
	globals, err := prog.Init(thread, nil)
	return thread, globals, prog, err
}

type staticKey struct {
	file      string
	line, col int32
}

func keyOf(p syntax.Position) staticKey { return staticKey{p.Filename(), p.Line, p.Col} }

// staticIndex is the static table of a program, found by the position of a
// function. A nil *staticIndex checks nothing.
type staticIndex struct {
	funcs map[staticKey]starlark.FuncodeInfo
	seen  map[string]bool // events found, by "pos pc"
}

func newStaticIndex(prog *starlark.Program) *staticIndex {
	s := &staticIndex{funcs: map[staticKey]starlark.FuncodeInfo{}, seen: map[string]bool{}}
	for _, f := range prog.Funcodes() {
		s.funcs[keyOf(f.Pos)] = f
	}
	return s
}

// check says that the event of fn with b is in the static table of fn, found
// by (Pos of the function, ChainID, Operand, Cond), and equal to it in every
// field.
func (s *staticIndex) check(t *testing.T, fn *starlark.Function, b starlark.Branch) {
	t.Helper()
	if s == nil {
		return
	}
	f, ok := s.funcs[keyOf(fn.Position())]
	if !ok {
		t.Errorf("function %s at %s is not in Funcodes()", fn.Name(), fn.Position())
		return
	}
	if f.Name != fn.Name() || f.IsToplevel != fn.IsToplevel() {
		t.Errorf("Funcodes() entry at %s: name %q toplevel %v, function: %q %v", fn.Position(), f.Name, f.IsToplevel, fn.Name(), fn.IsToplevel())
	}
	var found []starlark.Branch
	for _, sb := range f.Branches {
		if sb.ChainID == b.ChainID && sb.Operand == b.Operand && sb.Cond == b.Cond {
			found = append(found, sb)
		}
	}
	if len(found) != 1 {
		t.Errorf("event %+v of %s at %s: %d matches in the static table, want 1", b, fn.Name(), fn.Position(), len(found))
		return
	}
	if found[0] != b {
		t.Errorf("event of %s at %s differs from the static table:\n event  %+v\n static %+v", fn.Name(), fn.Position(), b, found[0])
	}
	s.seen[fmt.Sprintf("%s %d", fn.Position(), b.PC)] = true
}

const staticSrc = `x = 1
if x > 0 and x < 5:
    y = 1
else:
    y = 2

def outer(a, b):
    def inner(c):
        if c or not (a and b):
            return 1
        return 0
    f = lambda z: 1 if z > 2 else 0
    r = [i for i in range(3) if i != a]
    n = 0
    while n < 2 and b:
        n += 1
    if a:
        pass
    elif b:
        pass
    return inner(b) + f(b) + len(r) + n

def other(q):
    if q in [1, 2] or q == 5:
        return 1
    return 0

def plain(q):
    return q

outer(1, 0)
outer(0, 1)
outer(2, 3)
outer(0, 0)
other(1)
other(5)
other(7)
`

// TestFuncodesMatchTrace: every event of a run is in the static table of its
// function, equal in all fields; the toplevel, a def, a nested def, a lambda
// and a def with no condition are all covered; and every Branch of the table
// is reached by the inputs of the run (none is dead weight).
func TestFuncodesMatchTrace(t *testing.T) {
	var events int
	var static *staticIndex
	tr := &starlark.Tracer{
		OnBranch: func(_ *starlark.Thread, fn *starlark.Function, b starlark.Branch, _ starlark.Value, _ bool) {
			events++
			static.check(t, fn, b)
		},
		OnCompare: func(_ *starlark.Thread, fn *starlark.Function, b starlark.Branch, _ syntax.Token, _, _ starlark.Value, _ bool) {
			static.check(t, fn, b)
		},
	}
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", staticSrc, starlark.StringDict(nil).Has)
	if err != nil {
		t.Fatal(err)
	}
	static = newStaticIndex(prog)
	thread := &starlark.Thread{Trace: tr}
	if _, err := prog.Init(thread, nil); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Fatal("no events")
	}
	for _, f := range prog.Funcodes() {
		for _, b := range f.Branches {
			if !static.seen[fmt.Sprintf("%s %d", f.Pos, b.PC)] {
				t.Errorf("%s %s: the Branch %s#%d at %s was never reported by a run", f.Name, f.Pos, b.Kind, b.Operand, b.Cond)
			}
		}
	}
}

// TestFuncodesLayout pins what Funcodes lists, in which order, and the tables.
func TestFuncodesLayout(t *testing.T) {
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", staticSrc, starlark.StringDict(nil).Has)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range prog.Funcodes() {
		var bs []string
		for _, b := range f.Branches {
			bs = append(bs, fmt.Sprintf("%s#%d", b.Kind, b.Operand))
		}
		got = append(got, fmt.Sprintf("%s top=%v pos=%s %v", f.Name, f.IsToplevel, lc(f.Pos), bs))
	}
	want := []string{
		"<toplevel> top=true pos=1:1 [and#0 and#1]",
		"inner top=false pos=8:5 [or#0 and#0 and#1]",
		"lambda top=false pos=12:9 [cond_expr#-1]",
		"outer top=false pos=7:1 [comp_if#-1 and#0 if#-1 elif#-1 and#1]",
		"other top=false pos=23:1 [or#0 or#1]",
		"plain top=false pos=28:1 []",
	}
	diffLines(t, "Funcodes", got, want)
}

// TestFuncodesOrderStable: the order is the same on every compilation of the
// same source (it depends on the source alone).
func TestFuncodesOrderStable(t *testing.T) {
	names := func() (ns []string) {
		_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", staticSrc, starlark.StringDict(nil).Has)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range prog.Funcodes() {
			ns = append(ns, f.Name+"@"+lc(f.Pos))
		}
		return
	}
	first := names()
	for i := 0; i < 20; i++ {
		if got := names(); !reflect.DeepEqual(got, first) {
			t.Fatalf("order changed: %v then %v", first, got)
		}
	}
}

// TestFuncodesRoundTrip: the table of a program read back by CompiledProgram
// is the table of the program that was written, and a run of the decoded
// program reports events that are in it.
func TestFuncodesRoundTrip(t *testing.T) {
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", staticSrc, starlark.StringDict(nil).Has)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := prog.Write(&buf); err != nil {
		t.Fatal(err)
	}
	back, err := starlark.CompiledProgram(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want, got := prog.Funcodes(), back.Funcodes()
	nb := 0
	for _, f := range want {
		nb += len(f.Branches)
	}
	if nb == 0 {
		t.Fatal("the original program has no Branches: the test proves nothing")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Funcodes after the round trip differ:\n got  %+v\n want %+v", got, want)
	}

	static := newStaticIndex(back)
	events := 0
	thread := &starlark.Thread{Trace: &starlark.Tracer{
		OnBranch: func(_ *starlark.Thread, fn *starlark.Function, b starlark.Branch, _ starlark.Value, _ bool) {
			events++
			static.check(t, fn, b)
		},
	}}
	if _, err := back.Init(thread, nil); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Error("the decoded program reported no events")
	}
}

// TestFuncodesCopies: the slices of the result are the caller's.
func TestFuncodesCopies(t *testing.T) {
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", staticSrc, starlark.StringDict(nil).Has)
	if err != nil {
		t.Fatal(err)
	}
	a := prog.Funcodes()
	a[0].Branches[0].Negated = !a[0].Branches[0].Negated
	a[0].Branches[0].ChainID = 99
	a[0].Name = "changed"
	b := prog.Funcodes()
	if b[0].Name == "changed" || b[0].Branches[0].ChainID == 99 {
		t.Errorf("a change of the result reached the program: %+v", b[0])
	}
}

// TestFuncodeInfoPublicFields: the description of a function holds these
// fields and nothing of the interpreter.
func TestFuncodeInfoPublicFields(t *testing.T) {
	want := []string{"Name", "Pos", "IsToplevel", "Branches"}
	var have []string
	typ := reflect.TypeOf(starlark.FuncodeInfo{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			t.Errorf("unexported field %s in starlark.FuncodeInfo", f.Name)
			continue
		}
		have = append(have, f.Name)
	}
	sort.Strings(have)
	sort.Strings(want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("fields of starlark.FuncodeInfo = %v, want %v", have, want)
	}
	if bt := reflect.TypeOf(starlark.FuncodeInfo{}.Branches).Elem(); bt != reflect.TypeOf(starlark.Branch{}) {
		t.Errorf("Branches holds %v, want starlark.Branch", bt)
	}
}
