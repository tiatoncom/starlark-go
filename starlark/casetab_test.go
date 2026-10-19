package starlark

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The static tables of the case maps (casetables.go) are made by
// mkcasetables.go from package unicode. They must be what package unicode of
// the Go that runs the tests says, or a case map would charge a size that is
// not the one it makes.

func TestCaseTables_UnicodeVersionIsTheGeneratedOne(t *testing.T) {
	if caseTablesUnicodeVersion != unicode.Version {
		t.Fatalf("casetables.go was generated from Unicode %s, this Go has Unicode %s: regenerate with go generate (in the directory of package starlark)",
			caseTablesUnicodeVersion, unicode.Version)
	}
}

func TestCaseTables_AreWhatPackageUnicodeSays(t *testing.T) {
	bit := func(w *[caseWidthLimit / 64]uint64, r rune) bool { return w[r>>6]&(1<<(r&63)) != 0 }
	tab := caseWidthTables()
	bad := 0
	fail := func(format string, args ...any) {
		t.Helper()
		if bad++; bad <= 10 {
			t.Errorf(format+": regenerate with go generate (in the directory of package starlark)", args...)
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= caseWidthLimit {
			// nothing beyond the limit is mapped, or a table would have to cover it
			if unicode.ToUpper(r) != r || unicode.ToLower(r) != r || unicode.ToTitle(r) != r {
				fail("U+%04X has a case mapping beyond caseWidthLimit", r)
			}
			continue
		}
		if got, want := bit(&tab.upper, r), utf8.RuneLen(unicode.ToUpper(r)) != utf8.RuneLen(r); got != want {
			fail("upper width of U+%04X: table %v, unicode %v", r, got, want)
		}
		if got, want := bit(&tab.lower, r), utf8.RuneLen(unicode.ToLower(r)) != utf8.RuneLen(r); got != want {
			fail("lower width of U+%04X: table %v, unicode %v", r, got, want)
		}
		if got, want := bit(&tab.title, r), utf8.RuneLen(unicode.ToTitle(r)) != utf8.RuneLen(r); got != want {
			fail("title width of U+%04X: table %v, unicode %v", r, got, want)
		}
		if r >= utf8.RuneSelf {
			if got, want := bit(&generatedCasedAfter[0], r), isCasedRune(unicode.ToLower(r)); got != want {
				fail("cased after lower of U+%04X: table %v, unicode %v", r, got, want)
			}
			if got, want := bit(&generatedCasedAfter[1], r), isCasedRune(unicode.ToTitle(r)); got != want {
				fail("cased after title of U+%04X: table %v, unicode %v", r, got, want)
			}
		}
	}
}

// No table is built when the first case map is called (it was ~4 ms when the
// table of cased runes was made by the first call): the tables are static, and
// no sync.Once is left in the code of the tables. (Not a test of time: the
// structure of the code is what is checked.)
func TestCaseTables_FirstCallIsFree(t *testing.T) {
	if s := titleCase("éa ǆx", titleCaseLen("éa ǆx", false), false); s != "Éa ǅx" {
		t.Fatalf("title = %q", s)
	}

	// The tables are package-level composite literals of constants: the file
	// has no function (so no init), and their initializers call nothing.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "casetables.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			t.Errorf("casetables.go declares the function %s", d.Name.Name)
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					found[name.Name] = true
					if i >= len(vs.Values) {
						t.Errorf("%s has no initializer", name.Name)
						continue
					}
					if _, ok := vs.Values[i].(*ast.CompositeLit); !ok {
						t.Errorf("%s is not a composite literal", name.Name)
					}
					ast.Inspect(vs.Values[i], func(n ast.Node) bool {
						if _, ok := n.(*ast.CallExpr); ok {
							t.Errorf("the initializer of %s calls a function", name.Name)
						}
						return true
					})
				}
			}
		}
	}
	for _, name := range []string{"generatedCaseWidths", "generatedCasedAfter"} {
		if !found[name] {
			t.Errorf("casetables.go does not declare %s", name)
		}
	}

	// and nothing is built once, at run time, by the code that uses them
	for _, file := range []string{"casetab.go", "casetables.go", "library.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "sync.Once") || strings.Contains(string(src), "sync.OnceValue") {
			t.Errorf("%s builds something once at run time", file)
		}
	}
}
