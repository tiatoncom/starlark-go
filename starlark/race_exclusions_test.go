package starlark_test

// The tests that are not built under -race, and why.
//
// The race detector's runtime instruments the program and changes what it
// allocates (shadow memory, the allocations of sync and of maps), so a test
// that MEASURES the allocations of the Go runtime (testing.AllocsPerRun,
// runtime.MemStats) against a number fixed without the detector is not
// meaningful under it. Such a test is held by the run without -race, and a file
// that holds it says so with the constraint `//go:build !race`.
//
// What is not measured by the runtime is never excluded: the counters of the
// fork itself (Thread.AllocatedBytes, Steps, the charges) are deterministic and
// are tested under -race like any other code.
//
// A test that is excluded from the run under -race is a test that a race in the
// code it runs would not find, so the exclusion is a decision, written here with
// its reason: a file with a constraint that removes it from a build with the
// race detector, whose test functions are not exactly those declared below, is
// red. The tests of the whole module are read.

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Tests that read the runtime's allocations and stay in the run under -race,
// because they are green under it and their bounds do not rest on the detector
// not being there: the upper bounds of the bytes a refused operation allocated
// (alloc_budget_test.go, alloc_review_test.go, bigint_alloc_test.go,
// lib/json/alloc_test.go; their other assertions are the fork's counters), the
// heap after a collection against the charge (alloc_calibration_test.go), the
// zero-allocation checks of the comparison (compare_test.go) and the equality
// of two counts of one run (TestTraceEnterArgsReused). The tools that are run by
// hand (calibrate_test.go, stackdepth_measure_test.go) are skipped without
// their environment variable.
//
// raceExcluded lists "<directory of the module>/<file>: <function>" of every
// test, benchmark, fuzz target and example of a file that is not built under
// -race, with the reason.
var raceExcluded = map[string]string{
	"starlark/allocs_small_test.go: TestSmallAllocs_NoMoreThanV020":       "compares testing.AllocsPerRun and runtime TotalAlloc of short paths with the numbers of v0.2.0, taken without the detector (sorted(l) makes 272 B under -race, 257 B without it, the bound is 269 B)",
	"starlark/peak_test.go: TestPeak_AnOperationAllocatesWhatItCharged":   "compares the runtime's TotalAlloc with the bytes charged, within 1.25x (json.indent is 2.00x and json.encode_indent 1.68x under -race, 1.00x and 1.01x without it)",
	"lib/json/norace_test.go: (no test function)":                         "the constant raceOn = false of the json tests (race_test.go has raceOn = true): the tests that compare the runtime's TotalAlloc with the bytes charged by the unquote of json.decode (TestG2_DecodeUnquotePeak, TestG4b_DecodeUnquoteGrowthIsChargedForEveryLength) skip themselves under -race by it",
	"starlark/peak_test.go: TestCharged_NotMoreThanTwiceWhatWasAllocated": "the other side of the same comparison (the bytes charged against the runtime's TotalAlloc), by the same helper and the same tables as the test above; it passes under -race only because the detector's extra allocations loosen its one-sided bound",
}

// A file is excluded from a build with the race detector if its constraint
// holds without the tag "race" and does not hold with it. (Every other tag is
// taken as set: the file is judged by the race alone.)
func excludedUnderRace(t *testing.T, path string, f *ast.File) bool {
	t.Helper()
	var exprs []constraint.Expr
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			x, err := constraint.Parse(c.Text)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			exprs = append(exprs, x)
		}
	}
	holds := func(race bool) bool {
		for _, x := range exprs {
			if !x.Eval(func(tag string) bool { return tag != "race" || race }) {
				return false
			}
		}
		return true
	}
	return holds(false) && !holds(true)
}

func TestRaceExclusionsAreDeclared(t *testing.T) {
	root := ".."
	got := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if !excludedUnderRace(t, path, f) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		found := false
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
				if strings.HasPrefix(fn.Name.Name, prefix) {
					got[rel+": "+fn.Name.Name] = true
					found = true
				}
			}
		}
		if !found {
			// A file without a test that is excluded from -race holds helpers of
			// the tests that are: say so, so that it is not forgotten.
			got[rel+": (no test function)"] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for k := range got {
		if _, ok := raceExcluded[k]; !ok {
			problems = append(problems, "not declared in raceExcluded (not built under -race): "+k)
		}
	}
	for k, reason := range raceExcluded {
		if !got[k] {
			problems = append(problems, "declared in raceExcluded, but it is built under -race (or is gone): "+k)
		}
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, "no reason: "+k)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
