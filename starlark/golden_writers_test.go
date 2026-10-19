package starlark_test

// Every writer of the goldens of v0.2.0 (TestWrite*Golden) refuses to run in a
// tree of this version (refuseInV030 in diff_test.go): a rewrite here would
// make the differential compare v0.3.0 with itself.
//
// This file is NOT copied to the tree of v0.2.0 and need not compile there.
//
// TestWritePricesDoc is not in the list: it is not a writer of v0.2.0 goldens
// (it writes doc/prices.md under STARLARK_WRITE_PRICES), and TestPricesGolden*
// writes testdata/prices.golden of this tree, which is right.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var goldenWriters = []string{
	"TestWriteDiffGolden",
	"TestWriteCompatGolden",
	"TestWriteErrTextGolden",
	"TestWriteLimitsGolden",
	"TestWriteB5Golden",
}

func TestGoldenWritersListIsComplete(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			n := fd.Name.Name
			if strings.HasPrefix(n, "TestWrite") && strings.HasSuffix(n, "Golden") {
				found[n] = true
			}
		}
	}
	var want []string
	for n := range found {
		want = append(want, n)
	}
	sort.Strings(want)
	have := append([]string(nil), goldenWriters...)
	sort.Strings(have)
	if strings.Join(want, ",") != strings.Join(have, ",") {
		t.Errorf("writers of goldens in the package: %v; in the list of this test: %v", want, have)
	}
}

func TestGoldenWritersRefuse(t *testing.T) {
	for _, name := range goldenWriters {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1")
			cmd.Env = append(os.Environ(), "STARLARK_WRITE_GOLDEN="+dir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("%s did not fail; output:\n%s", name, out)
			}
			if !strings.Contains(string(out), "this is a tree of v0.3.0") || !strings.Contains(string(out), "d34a821") {
				t.Errorf("%s: no refusal text in the output:\n%s", name, out)
			}
			ents, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(ents) != 0 {
				t.Errorf("%s wrote %d files into %s", name, len(ents), dir)
			}
		})
	}
}
