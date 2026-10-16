package starlark_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// TestBranchPublicFields: the Branch that a host gets holds the description of
// the condition and nothing of the interpreter's mechanics (the flags that make
// echoes and flipped tests invisible live in the internal Funcode), and the
// package does not export the type of those flags.
func TestBranchPublicFields(t *testing.T) {
	want := []string{"PC", "Kind", "Cond", "Chain", "Keyword", "Operand", "Negated", "ChainID", "ParentChain"}
	var have []string
	typ := reflect.TypeOf(starlark.Branch{})
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.IsExported() {
			have = append(have, f.Name)
		}
	}
	sort.Strings(have)
	sort.Strings(want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("exported fields of starlark.Branch = %v, want %v", have, want)
	}

	// No exported ShortCircuit, nor its constants, in the package.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs["starlark"]
	if pkg == nil {
		t.Fatal("package starlark not found")
	}
	for name, file := range pkg.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if strings.HasPrefix(sp.Name.Name, "Short") {
						t.Errorf("%s exports type %s", name, sp.Name.Name)
					}
				case *ast.ValueSpec:
					for _, id := range sp.Names {
						if strings.HasPrefix(id.Name, "Short") {
							t.Errorf("%s exports %s", name, id.Name)
						}
					}
				}
			}
		}
	}
}

// TestTraceTestedUnderComputedNot: under a "not" that the compiler evaluates
// as a value, the last operand of the chain is reported with tested =
// Bool(taken) (its value is gone), the operands before it with their value
// (doc/trace.md, "taken, Negated and the side that ran").
func TestTraceTestedUnderComputedNot(t *testing.T) {
	src := `def f(a, b, c):
    if not (a and b) and c:
        return 1
    return 0
`
	var got []string
	tr := &starlark.Tracer{OnBranch: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, tested starlark.Value, taken bool) {
		got = append(got, fmt.Sprintf("%s#%d tested=%v taken=%v", b.Kind, b.Operand, tested, taken))
	}}
	thread, globals, err := execTraced(t, src, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := starlark.Call(thread, globals["f"], starlark.Tuple{starlark.MakeInt(7), starlark.MakeInt(0), starlark.MakeInt(5)}, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"and#0 tested=7 taken=true",      // before the NOT: the value itself
		"and#1 tested=False taken=false", // after it: Bool(taken), not 0
		"and#1 tested=5 taken=true",      // c, outside the not
	}
	diffLines(t, "events", got, want)
}
