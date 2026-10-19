package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// copyTree copies the declared files of the repository into a new root and
// returns the root and the tree.
func copyTree(t *testing.T) (string, *Tree) {
	t.Helper()
	src, err := NewTarget("../../..")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := src.ReadTree()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "root")
	for rel, s := range tree.Files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, tree
}

func TestNumberingIsStable(t *testing.T) {
	_, tree := copyTree(t)
	again := NewTree(tree.Files)
	if again.Hash != tree.Hash {
		t.Fatalf("the hash of the same text differs: %s %s", again.Hash, tree.Hash)
	}
	for _, g := range Groups {
		a, err := Generate(tree, g)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Generate(again, g)
		if !reflect.DeepEqual(a.Mutants, b.Mutants) {
			t.Fatalf("%s: two generations differ", g)
		}
		if len(a.Mutants) == 0 {
			t.Fatalf("%s: no mutants", g)
		}
		seen := map[string]bool{}
		for i, m := range a.Mutants {
			if m.Index != i || m.ID != fmt.Sprintf("%s-%04d", g, i) {
				t.Fatalf("%s: mutant %d has id %s index %d", g, i, m.ID, m.Index)
			}
			if seen[m.ID] {
				t.Fatalf("duplicate id %s", m.ID)
			}
			seen[m.ID] = true
			if tree.Files[m.File][m.Start:m.End] != m.Before {
				t.Fatalf("%s: Before is not the text of the span", m.ID)
			}
			if m.Before == m.After {
				t.Fatalf("%s: the mutation changes nothing", m.ID)
			}
			if got := strings.Count(tree.Files[m.File][:m.Start], "\n") + 1; got != m.Line {
				t.Fatalf("%s: line %d, want %d", m.ID, m.Line, got)
			}
		}
	}
	// A different text gives a different hash.
	files := map[string]string{}
	for k, v := range tree.Files {
		files[k] = v
	}
	files[fWork] += "\n"
	if NewTree(files).Hash == tree.Hash {
		t.Fatal("the hash does not depend on the text")
	}
}

func TestTargetWritesOnlyDeclaredFilesInsideTheRoot(t *testing.T) {
	root, tree := copyTree(t)
	target, err := NewTarget(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.go")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "starlark", "other.go")
	if err := os.WriteFile(other, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../outside.go", "/etc/hosts", "starlark/other.go", "starlark/../starlark/eval.go", "starlark//eval.go", ""} {
		if err := target.Write(rel, "x"); err == nil {
			t.Errorf("Write(%q) did not refuse", rel)
		}
	}
	// A declared file that is a link out of the root.
	link := filepath.Join(root, filepath.FromSlash(fAlloc))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := target.Write(fAlloc, "x"); err == nil {
		t.Error("Write through a link out of the root did not refuse")
	}
	for p, want := range map[string]string{outside: "untouched", other: "untouched"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Errorf("%s was written: %q", p, b)
		}
	}
	// The files that were not touched are as they were.
	for rel, s := range tree.Files {
		if rel == fAlloc {
			continue
		}
		if got, _ := target.Read(rel); got != s {
			t.Errorf("%s changed", rel)
		}
	}
}

func TestFreezeRestoresAndRefusesWrites(t *testing.T) {
	root, tree := copyTree(t)
	target, _ := NewTarget(root)
	if err := target.Write(fWork, "garbage"); err != nil {
		t.Fatal(err)
	}
	if err := target.Freeze(tree.Files); err != nil {
		t.Fatal(err)
	}
	if got, _ := target.ReadTree(); got.Hash != tree.Hash {
		t.Fatal("not restored")
	}
	if err := target.Write(fWork, "garbage"); err == nil {
		t.Fatal("a frozen target accepted a write")
	}
}

// RunMutant puts the file back after every mutant, whatever the verdict, and
// the build sees the mutant.
func TestRunMutantRestores(t *testing.T) {
	root, tree := copyTree(t)
	target, _ := NewTarget(root)
	set, err := Generate(tree, GroupStruct)
	if err != nil {
		t.Fatal(err)
	}
	m := set.Mutants[3]
	var seen []string
	r := &Runner{T: target, Cfg: Config{Stages: []Stage{{Pkg: "starlark", Run: ".", Timeout: time.Second}}, BinDir: t.TempDir()},
		Exec: func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
			s, _ := target.Read(m.File)
			seen = append(seen, s)
			return "does not build", errors.New("exit 1")
		}}
	res, err := r.RunMutant(m, tree.Files[m.File])
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != KilledBuild || res.ID != m.ID {
		t.Fatalf("verdict %s id %s", res.Verdict, res.ID)
	}
	if len(seen) != 1 || seen[0] != m.Apply(tree.Files[m.File]) {
		t.Fatal("the build did not see the mutant")
	}
	if got, _ := target.ReadTree(); got.Hash != tree.Hash {
		t.Fatal("the tree is not restored")
	}
}

// A dropped call that the code uses as a statement is tried once in a form
// that builds.
func TestRunMutantRetriesAnUnusedCall(t *testing.T) {
	root, tree := copyTree(t)
	target, _ := NewTarget(root)
	m := Mutant{ID: "x-0000", Group: "x", File: fWork, Start: 0, End: 7, Before: tree.Files[fWork][:7], After: "error(nil)"}
	var seen []string
	r := &Runner{T: target, Cfg: Config{Stages: []Stage{{Pkg: "starlark", Run: ".", Timeout: time.Second}}, BinDir: t.TempDir()},
		Exec: func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
			s, _ := target.Read(fWork)
			seen = append(seen, s[:20])
			return "error(nil) (value of type error) is not used", errors.New("exit 1")
		}}
	res, err := r.RunMutant(m, tree.Files[fWork])
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !strings.HasPrefix(seen[0], "error(nil)") || !strings.HasPrefix(seen[1], "_ = error(nil)") || res.Retry != "_ = error(nil)" {
		t.Fatalf("seen %q retry %q", seen, res.Retry)
	}
	if got, _ := target.ReadTree(); got.Hash != tree.Hash {
		t.Fatal("the tree is not restored")
	}
}

func TestStageFlagAndDefaultStages(t *testing.T) {
	var sf stageFlags
	if err := sf.Set("starlark;^(A|B)$;30;C|D"); err != nil {
		t.Fatal(err)
	}
	if err := sf.Set("lib/json;.;10"); err != nil {
		t.Fatal(err)
	}
	want := stageFlags{{Pkg: "starlark", Run: "^(A|B)$", Skip: "C|D", Timeout: 30 * time.Second}, {Pkg: "lib/json", Run: ".", Timeout: 10 * time.Second}}
	if !reflect.DeepEqual(sf, want) {
		t.Fatalf("%+v", sf)
	}
	if err := sf.Set("a;b"); err == nil {
		t.Fatal("two fields accepted")
	}
	// The slow tests the stages name are tests of package starlark.
	src, err := os.ReadDir("../../../starlark")
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, e := range src {
		if strings.HasSuffix(e.Name(), "_test.go") {
			b, _ := os.ReadFile(filepath.Join("../../../starlark", e.Name()))
			text.Write(b)
		}
	}
	for _, name := range strings.Split(slowTests, "|") {
		if !strings.Contains(text.String(), "func "+name+"(") {
			t.Errorf("slowTests names %s: no such test", name)
		}
	}
	for _, g := range Groups {
		if len(defaultStages(g)) == 0 {
			t.Errorf("%s: no stages", g)
		}
	}
}

// The price mutants of one-line functions replace the expression, not the }.
func TestPriceMutantsReplaceTheExpressionOnly(t *testing.T) {
	_, tree := copyTree(t)
	set, err := Generate(tree, GroupPrices)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range set.Mutants {
		if strings.Contains(m.Before, "\n") || strings.HasSuffix(strings.TrimSpace(m.Before), "}") {
			t.Errorf("%s: Before %q", m.ID, m.Before)
		}
		if m.File == fWork && strings.HasSuffix(m.Note, "workFast") {
			found++
			if m.Before != "uint64(max(n, 0)) / 64" {
				t.Errorf("%s: Before %q", m.ID, m.Before)
			}
		}
	}
	if found != 2 {
		t.Errorf("workFast: %d mutants, want ZERO and HALF", found)
	}
}

// A price that was zeroed and left a variable unused is tried once more with
// the variable used.
func TestRunMutantRetriesAnUnusedVariableOfAPrice(t *testing.T) {
	root, tree := copyTree(t)
	target, _ := NewTarget(root)
	m := Mutant{ID: "x-0000", Group: "x", File: fWork, Start: 0, End: 7, Before: tree.Files[fWork][:7], After: "0"}
	var seen []string
	r := &Runner{T: target, Cfg: Config{Stages: []Stage{{Pkg: "starlark", Run: ".", Timeout: time.Second}}, BinDir: t.TempDir()},
		Exec: func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
			s, _ := target.Read(fWork)
			seen = append(seen, s[:40])
			return "starlark/work.go:3:6: declared and not used: s", errors.New("exit 1")
		}}
	res, err := r.RunMutant(m, tree.Files[fWork])
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !strings.HasPrefix(seen[1], "func() uint64 { _ = s; return 0 }()") || res.Retry == "" {
		t.Fatalf("seen %q retry %q", seen, res.Retry)
	}
}

func TestDropTakesTheSelectorOfAField(t *testing.T) {
	tree := NewTree(map[string]string{fAlloc: "x\nif err := c.thread.chargeOne(c.have, c.unit); err != nil {\n}\nthread.chargeWork(1)\n"})
	g := &generator{tree: tree, group: "t"}
	g.collect(fAlloc, reCallsThread, "error(nil)")
	if len(g.muts) != 2 || g.muts[0].Before != "c.thread.chargeOne(c.have, c.unit)" || g.muts[1].Before != "thread.chargeWork(1)" {
		t.Fatalf("%+v", g.muts)
	}
}

func TestSelect(t *testing.T) {
	var all []Mutant
	for i := 0; i < 20; i++ {
		all = append(all, Mutant{Index: i, File: "f", Note: "n"})
	}
	idx := func(ms []Mutant) (r []int) {
		for _, m := range ms {
			r = append(r, m.Index)
		}
		return
	}
	if got := idx(selectMutants(all, 2, 8, "", 3, "")); !reflect.DeepEqual(got, []int{2, 5}) {
		t.Errorf("range+every: %v", got)
	}
	if got := idx(selectMutants(all, 0, -1, "7, 3,19", 1, "")); !reflect.DeepEqual(got, []int{3, 7, 19}) {
		t.Errorf("only: %v", got)
	}
}

// A test binary that fails without a failed test is a crash, which is not a
// kill; one with a "--- FAIL" line is killed-test.
func TestClassifyFailure(t *testing.T) {
	exit2 := errors.New("exit status 2")
	cases := []struct {
		name    string
		err     error
		text    string
		verdict string
		failed  []string
		detail  []string // substrings of the detail
	}{
		{"failed test", exit2, "=== RUN TestA\n--- FAIL: TestA (0.10s)\n    a_test.go:3: bad\nFAIL\n", KilledTest, []string{"TestA"}, nil},
		{"panic inside a test", exit2, "--- FAIL: TestB (0.00s)\npanic: boom [recovered]\n", KilledTest, []string{"TestB"}, nil},
		{"fatal error", exit2, "fatal error: concurrent map writes\n\ngoroutine 7 [running]:\n", Crashed, nil, []string{"exit status 2", "fatal error: concurrent map writes"}},
		{"panic outside a test", exit2, "panic: runtime error: index out of range [3] with length 2\n\ngoroutine 1 [running]:\n", Crashed, nil, []string{"panic: runtime error"}},
		{"signal", errors.New("signal: killed"), "", Crashed, nil, []string{"signal: killed"}},
		{"exit without output", exit2, "", Crashed, nil, []string{"exit status 2"}},
	}
	for _, c := range cases {
		v, detail, failed := classifyFailure(c.err, c.text)
		if v != c.verdict || !reflect.DeepEqual(failed, c.failed) {
			t.Errorf("%s: verdict %s failed %v", c.name, v, failed)
		}
		for _, s := range c.detail {
			if !strings.Contains(detail, s) {
				t.Errorf("%s: detail %q lacks %q", c.name, detail, s)
			}
		}
		if v == KilledTest && detail != "" {
			t.Errorf("%s: detail %q", c.name, detail)
		}
	}
	// Only the first lines of a long output are kept, and no more than crashBytes.
	var long strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	_, detail, _ := classifyFailure(exit2, long.String())
	if !strings.Contains(detail, "line 0") || !strings.Contains(detail, fmt.Sprintf("line %d", crashLines-1)) || strings.Contains(detail, fmt.Sprintf("line %d\n", crashLines)) {
		t.Errorf("the first lines: %q", detail)
	}
	_, detail, _ = classifyFailure(exit2, strings.Repeat("x", 10*crashBytes))
	if len(detail) != crashBytes {
		t.Errorf("detail of %d bytes", len(detail))
	}
}

// -report counts a crash in a column of its own, not as killed, and lists it.
func TestReportCountsCrashesApart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	lines := []string{
		`{"header":{}}`,
		`{"id":"struct-0000","group":"struct","verdict":"killed-test","failed":["TestA"]}`,
		`{"id":"struct-0001","group":"struct","file":"starlark/eval.go","line":7,"note":"n","verdict":"crashed","detail":"signal: killed\nfatal error: x"}`,
		`{"id":"struct-0002","group":"struct","verdict":"survived"}`,
		`{"id":"struct-0003","group":"struct","verdict":"killed-build"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := printReport(&out, []string{path}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	want := "struct: total 4, killed-test 1, killed-build 1, timeout 0, memory 0, crashed 1, survived 1\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("summary:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(got, "CRASHED struct-0001") || !strings.Contains(got, "fatal error: x") {
		t.Errorf("the crash is not listed:\n%s", got)
	}
}

// gitIn runs git in dir for a test, or skips the test if there is no git.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A run is refused in a main worktree (.git is a directory) and in a directory
// that is no worktree; a linked worktree (.git is a file) is accepted.
func TestRefusesAMainWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "x")
	linked := filepath.Join(base, "linked")
	gitIn(t, main, "worktree", "add", "-q", "--detach", linked)

	err := requireLinkedWorktree(main)
	if err == nil || !strings.Contains(err.Error(), "main worktree") || !strings.Contains(err.Error(), "git worktree add --detach") {
		t.Errorf("main worktree: %v", err)
	}
	if err := requireLinkedWorktree(linked); err != nil {
		t.Errorf("linked worktree: %v", err)
	}
	plain := t.TempDir()
	if err := requireLinkedWorktree(plain); err == nil || !strings.Contains(err.Error(), "git worktree add --detach") {
		t.Errorf("a directory without .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plain, ".git"), []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := requireLinkedWorktree(plain); err == nil {
		t.Error(".git that is no link was accepted")
	}

	// run() refuses a main worktree before it reads or runs anything ...
	var out strings.Builder
	err = run([]string{"-root", main, "-group", GroupStruct, "-out", filepath.Join(base, "out.jsonl")}, &out)
	if err == nil || !strings.Contains(err.Error(), "main worktree") {
		t.Errorf("run in a main worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "out.jsonl")); err == nil {
		t.Error("a results file was made")
	}
	// ... but -list only reads: the refusal is not what stops it here (the
	// repository has no declared files).
	err = run([]string{"-root", main, "-group", GroupStruct, "-list", "-allow-dirty"}, &out)
	if err == nil || strings.Contains(err.Error(), "worktree") {
		t.Errorf("-list in a main worktree: %v", err)
	}
}
