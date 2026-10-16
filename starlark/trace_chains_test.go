package starlark_test

// Tests of FuncodeInfo.Chains: every and/or chain of the conditions of a
// function, by ChainID, also the chains that no Branch names (all operands
// are chains; dead code), with the slot of the chain in its parent.

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"go.starlark.net/starlark"
)

// chainsOf returns the chains of the function name of src, one line each:
// "id kind@col parent/slot".
func chainsOf(t *testing.T, prog *starlark.Program, name string) (out []string, f starlark.FuncodeInfo) {
	t.Helper()
	found := false
	for _, fi := range prog.Funcodes() {
		if fi.Name == name {
			f, found = fi, true
		}
	}
	if !found {
		t.Fatalf("no function %s", name)
	}
	for _, c := range f.Chains {
		out = append(out, fmt.Sprintf("%d %s@%d:%d %d/%d", c.ChainID, c.Kind, c.Start.Line, c.Start.Col, c.ParentChain, c.ParentOperand))
	}
	return out, f
}

var chainCases = []struct {
	name     string
	src      string
	function string
	want     []string
}{
	{
		"seven chains, the outer one has no Branch",
		"def f(a, b, c, d, e, g, h):\n    if ((a and b) or (c and d)) and ((e and h) or (g and h)):\n        pass\n",
		"f",
		[]string{
			"0 and@2:8 -1/-1",
			"1 or@2:8 0/0",
			"2 and@2:9 1/0",
			"3 and@2:22 1/1",
			"4 or@2:37 0/1",
			"5 and@2:38 4/0",
			"6 and@2:51 4/1",
		},
	},
	{
		"slot in the flat list of the parent",
		"def f(a, b, c, d):\n    if a and (b or c) and d:\n        pass\n",
		"f",
		[]string{"0 and@2:8 -1/-1", "1 or@2:14 0/1"},
	},
	{
		"not in front of a chain that is not the last operand",
		"def f(a, b, c):\n    if not (a and b) and c:\n        pass\n",
		"f",
		[]string{"0 and@2:8 -1/-1", "1 and@2:12 0/0"},
	},
	{
		"parenthesised chain of the same operator: two chains at one place",
		"def f(a, b, c):\n    if (a and b) and c:\n        pass\n",
		"f",
		[]string{"0 and@2:8 -1/-1", "1 and@2:8 0/0"},
	},
	{
		"a double not",
		"def f(a, b):\n    if not (not (a and b)):\n        pass\n",
		"f",
		[]string{"0 and@2:17 -1/-1"},
	},
	{
		"comp_if",
		"def f(a, b, c, d):\n    return [x for x in a if b and (c or d)]\n",
		"f",
		[]string{"0 and@2:29 -1/-1", "1 or@2:35 0/1"},
	},
	{
		"lambda",
		"def f(a, b):\n    return lambda x: 1 if (x and a) or b else 2\n",
		"lambda",
		[]string{"0 or@2:27 -1/-1", "1 and@2:27 0/0"},
	},
	{
		"dead code after return: the chains are numbered, no Branch",
		"def f(a, b, c):\n    return 1\n    if (a and b) or c:\n        pass\n    while a or (b and c):\n        pass\n",
		"f",
		[]string{"0 or@3:8 -1/-1", "1 and@3:8 0/0", "2 or@5:11 -1/-1", "3 and@5:16 2/1"},
	},
	{
		"live chains after dead ones keep their numbers",
		"def f(a, b, c):\n    for x in a:\n        if b and c:\n            break\n            if a or b:\n                pass\n        if a or c:\n            pass\n",
		"f",
		[]string{"0 and@3:12 -1/-1", "1 or@5:16 -1/-1", "2 or@7:12 -1/-1"},
	},
	{
		"conditions inside the branches of a conditional expression are numbered by the source, not by the order the compiler visits them",
		"def f(a, b, c, d):\n    return (1 if a and b else 2) if c or d else 3\n",
		"f",
		[]string{"0 and@2:18 -1/-1", "1 or@2:37 -1/-1"},
	},
	{
		"a chain that is not in a condition has no entry",
		"def f(a, b):\n    x = a and b\n    return x\n",
		"f",
		nil,
	},
}

func TestFuncodesChains(t *testing.T) {
	for _, tc := range chainCases {
		t.Run(tc.name, func(t *testing.T) {
			_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", tc.src, starlark.StringDict(nil).Has)
			if err != nil {
				t.Fatal(err)
			}
			got, f := chainsOf(t, prog, tc.function)
			diffLines(t, "Chains", got, tc.want)
			checkChainsOfBranches(t, f)
			if tc.want == nil && f.Chains != nil {
				t.Errorf("Chains = %+v, want nil", f.Chains)
			}
		})
	}
}

// checkChainsOfBranches: the numbers of Chains are exactly 0..n-1 and the
// parent of a chain is numbered before it, and the ChainID / ParentChain of
// every Branch point at an element of Chains that agrees with it.
func checkChainsOfBranches(t *testing.T, f starlark.FuncodeInfo) {
	t.Helper()
	for i, c := range f.Chains {
		if c.ChainID != i {
			t.Errorf("Chains[%d].ChainID = %d", i, c.ChainID)
		}
		if (c.ParentChain < 0) != (c.ParentOperand < 0) {
			t.Errorf("Chains[%d]: parent %d with operand %d", i, c.ParentChain, c.ParentOperand)
		}
		if c.ParentChain >= i {
			t.Errorf("Chains[%d]: parent %d is not numbered before it", i, c.ParentChain)
		}
		if c.Kind != starlark.BranchAnd && c.Kind != starlark.BranchOr {
			t.Errorf("Chains[%d].Kind = %v", i, c.Kind)
		}
	}
	for _, b := range f.Branches {
		if b.ChainID < 0 {
			if b.ParentChain != -1 {
				t.Errorf("Branch %+v: no chain but a parent", b)
			}
			continue
		}
		if b.ChainID >= len(f.Chains) || b.ParentChain >= len(f.Chains) {
			t.Errorf("Branch %+v points outside Chains (%d)", b, len(f.Chains))
			continue
		}
		c := f.Chains[b.ChainID]
		if c.ParentChain != b.ParentChain || c.Kind != b.Kind || keyOf(c.Start) != keyOf(b.Chain) {
			t.Errorf("Branch %+v disagrees with Chains[%d] = %+v", b, b.ChainID, c)
		}
	}
}

// TestFuncodesChainsRoundTrip: the chains survive Write / CompiledProgram,
// those without a Branch included (the case set above: dead code, a chain
// with chains only for operands).
func TestFuncodesChainsRoundTrip(t *testing.T) {
	src := "def a(a, b, c, d, e, g, h):\n    if ((a and b) or (c and d)) and ((e and h) or (g and h)):\n        pass\n"
	src += "def b(a, b, c):\n    return 1\n    if (a and b) or c:\n        pass\n"
	src += "def c(a, b):\n    return [x for x in a if b and (a or x)]\n"
	src += "d = lambda x, y: 1 if (x and y) or x else 2\n"
	src += "if not (True and False) and True:\n    pass\n"
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", src, starlark.StringDict(nil).Has)
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
	total := 0
	for _, f := range want {
		total += len(f.Chains)
	}
	if total < 15 {
		t.Fatalf("only %d chains in the sample", total)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Funcodes after the round trip differ:\n got  %+v\n want %+v", got, want)
	}
	// the chains of the functions that have no Branch for some of them
	for _, f := range got {
		checkChainsOfBranches(t, f)
	}
}

// TestChainInfoPublicFields: the description of a chain holds these fields.
func TestChainInfoPublicFields(t *testing.T) {
	want := []string{"ChainID", "ParentChain", "ParentOperand", "Kind", "Start"}
	var have []string
	typ := reflect.TypeOf(starlark.ChainInfo{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			t.Errorf("unexported field %s in starlark.ChainInfo", f.Name)
			continue
		}
		have = append(have, f.Name)
	}
	sort.Strings(have)
	sort.Strings(want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("fields of starlark.ChainInfo = %v, want %v", have, want)
	}
	if ct := reflect.TypeOf(starlark.FuncodeInfo{}.Chains).Elem(); ct != reflect.TypeOf(starlark.ChainInfo{}) {
		t.Errorf("Chains holds %v, want starlark.ChainInfo", ct)
	}
}

// TestFuncodesChainsCopies: the Chains of the result are the caller's.
func TestFuncodesChainsCopies(t *testing.T) {
	_, prog, err := starlark.SourceProgramOptions(traceOpts, "t.star", "def f(a, b, c):\n    if a and (b or c):\n        pass\n", starlark.StringDict(nil).Has)
	if err != nil {
		t.Fatal(err)
	}
	_, f := chainsOf(t, prog, "f")
	f.Chains[1].ParentOperand = 99
	f.Chains[0].Kind = starlark.BranchOr
	if _, g := chainsOf(t, prog, "f"); g.Chains[1].ParentOperand == 99 || g.Chains[0].Kind == starlark.BranchOr {
		t.Errorf("a change of the result reached the program: %+v", g.Chains)
	}
}
