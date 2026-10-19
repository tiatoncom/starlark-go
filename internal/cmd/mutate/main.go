// Command mutate makes mutants of the sources of the resource accounting and
// decides each by the tests: killed (a test or the build fails), survived, or
// neither (timeout, memory, crashed: a test binary that died without a failed
// test is not a kill). Before the first mutant the tree without one is decided
// by the same stages and must be green (the baseline; it cannot be skipped).
// A run needs a linked worktree as -root; Ctrl-C or SIGTERM kills the test
// binaries' process groups and puts the files back.
//
// It is an internal tool, imported by nothing. It changes the sources of the
// tree named by -root, one declared file at a time, and puts every file back
// after every mutant; run it ONLY in a separate copy of the tree (a detached
// git worktree), never in a working tree that holds work:
//
//	git worktree add --detach ../starlark-go-mutate <commit>
//	go run ./internal/cmd/mutate -root ../starlark-go-mutate -group struct -list
//	go run ./internal/cmd/mutate -root ../starlark-go-mutate -group struct -out struct.jsonl
//	go run ./internal/cmd/mutate -report struct.jsonl
//
// What decides the result is explicit: the table of the group (table_struct.go,
// or the generators of mutants.go for calls and prices), the stages (-stage
// pkg;run;seconds[;skip], repeatable, else the table below), -skip, and the limits.
// The numbering of the mutants of a group depends on the text of the declared
// files only; the hash of that text is printed and written to the results
// (-expect-tree refuses a tree other than the one a numbering was made for).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultSkip is the -test.skip of every stage. The tests that measure time or
// write a document (TestMeasure*, TestCalibrate, TestWritePricesDoc,
// TestBudgetIdioms, TestWrite*Golden) skip themselves unless an environment
// variable asks for them, so nothing needs to be named here.
const defaultSkip = ""

// slowTests are the tests of package starlark that take most of its time (about
// 95 of 110 seconds on the reference machine, GOMAXPROCS=1). They run in the
// last stage, so that a mutant killed by any other test does not pay for them.
const slowTests = "TestPrices_AtLeastTheMeasuredCost|TestDeterminism_StepsAreTheSameInEveryProcess|TestAttacks_AreRefusedAtTheLimitOfWork|TestWork_NoProbeCostsMoreThanC|TestTraceOracle"

// defaultStages is the table of the stages by group. The decider is the whole
// test suite of both packages, in three stages; a mutant survives only if all
// three pass. It is the same for every group.
func defaultStages(group string) []Stage {
	return []Stage{
		{Pkg: "starlark", Run: ".", Skip: slowTests, Timeout: 120 * time.Second},
		{Pkg: "lib/json", Run: ".", Timeout: 60 * time.Second},
		{Pkg: "starlark", Run: "^(" + slowTests + ")$", Timeout: 300 * time.Second},
	}
}

type stageFlags []Stage

func (s *stageFlags) String() string { return fmt.Sprint(*s) }
func (s *stageFlags) Set(v string) error {
	// pkg;run;seconds[;skip]: the patterns hold | so the fields are split by ;
	parts := strings.Split(v, ";")
	if len(parts) != 3 && len(parts) != 4 {
		return fmt.Errorf("want pkg;run;seconds[;skip], got %q", v)
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n <= 0 {
		return fmt.Errorf("bad seconds in %q", v)
	}
	st := Stage{Pkg: parts[0], Run: parts[1], Timeout: time.Duration(n) * time.Second}
	if len(parts) == 4 {
		st.Skip = parts[3]
	}
	*s = append(*s, st)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mutate:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	var (
		root       = fs.String("root", "", "the tree to mutate: a LINKED git worktree (.git is a file), made with `git worktree add --detach`; required, except for -report; -list only reads, so it accepts any root")
		group      = fs.String("group", "", "struct, calls or prices")
		list       = fs.Bool("list", false, "print the mutants and the tree hash, run nothing")
		report     = fs.String("report", "", "comma-separated results files: print the counts and the survivors")
		out        = fs.String("out", "", "results file (jsonl, appended)")
		resume     = fs.Bool("resume", false, "skip the mutants already in -out")
		from       = fs.Int("from", 0, "first index of the group to run")
		to         = fs.Int("to", -1, "end index (exclusive); -1 is the end of the group")
		only       = fs.String("only", "", "comma-separated indexes of the group (instead of from/to)")
		every      = fs.Int("every", 1, "run every n-th index of the range")
		filter     = fs.String("filter", "", "run only mutants whose label contains this text")
		skip       = fs.String("skip", defaultSkip, "-test.skip of every stage")
		binDir     = fs.String("bin", "", "directory of the test binaries (outside the root; default: a temporary one)")
		expectTree = fs.String("expect-tree", "", "refuse a tree whose hash differs")
		allowDirty = fs.Bool("allow-dirty", false, "do not require a clean git status of the root")
		procs      = fs.String("gomaxprocs", "1", "GOMAXPROCS of the test binaries")
		memlimit   = fs.String("gomemlimit", "1GiB", "GOMEMLIMIT of the test binaries")
		rssMB      = fs.Int("rss-mb", 1500, "kill a test binary above this resident size, in MB (0: no limit)")
		slack      = fs.Duration("wall-slack", 20*time.Second, "wall time allowed over the timeout of a stage")
		stages     stageFlags
	)
	fs.Var(&stages, "stage", "stage pkg;run;seconds[;skip] (repeatable); default: the table of the group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *report != "" {
		return printReport(stdout, strings.Split(*report, ","))
	}
	if *root == "" || *group == "" {
		return fmt.Errorf("-root and -group are required")
	}
	t, err := NewTarget(*root)
	if err != nil {
		return err
	}
	if !*list {
		// A run writes the files of the root: only in a worktree made for it.
		// (-list reads and writes nothing.)
		if err := requireLinkedWorktree(t.Root); err != nil {
			return err
		}
	}
	if !*allowDirty {
		st, err := gitStatus(t.Root)
		if err != nil {
			return fmt.Errorf("git status of the root: %v (use -allow-dirty to skip the check)", err)
		}
		if st != "" {
			return fmt.Errorf("the root is not clean (a left-over mutant, or work in it):\n%s", st)
		}
	}
	tree, err := t.ReadTree()
	if err != nil {
		return err
	}
	if *expectTree != "" && *expectTree != tree.Hash {
		return fmt.Errorf("tree hash %s, expected %s", tree.Hash, *expectTree)
	}
	set, err := Generate(tree, *group)
	if err != nil {
		return err
	}
	sel := selectMutants(set.Mutants, *from, *to, *only, *every, *filter)
	fmt.Fprintf(stdout, "# tree sha256:%s\n# group %s: %d mutants, %d selected, %d patterns missing, %d identical mutants not made\n", tree.Hash, set.Group, len(set.Mutants), len(sel), len(set.Missing), set.Identical)
	for _, s := range set.Missing {
		fmt.Fprintf(stdout, "# MISSING %s\n", s)
	}
	if *list {
		for _, m := range sel {
			fmt.Fprintf(stdout, "%s\t%s:%d\t%s\t%s => %s\n", m.ID, m.File, m.Line, oneLine(m.Note, 100), oneLine(m.Before, 60), oneLine(m.After, 60))
		}
		return nil
	}
	if *out == "" {
		return fmt.Errorf("-out is required to run")
	}
	cfg := Config{Stages: stages, Skip: *skip, GoMaxProcs: *procs, GoMemLimit: *memlimit, RSSLimitMB: *rssMB, WallSlack: *slack}
	if len(cfg.Stages) == 0 {
		cfg.Stages = defaultStages(*group)
	}
	if *binDir == "" {
		d, err := os.MkdirTemp("", "mutate-bin-")
		if err != nil {
			return err
		}
		*binDir = d
	}
	absBin, err := filepath.Abs(*binDir)
	if err != nil {
		return err
	}
	if inside(t.Root, absBin) {
		return fmt.Errorf("-bin %s is inside the root", absBin)
	}
	if err := os.MkdirAll(absBin, 0o755); err != nil {
		return err
	}
	cfg.BinDir = absBin
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if inside(t.Root, absOut) {
		return fmt.Errorf("-out %s is inside the root", absOut)
	}

	// On a signal the children (they are in process groups of their own, which
	// the terminal does not reach) are killed, the files are put back and the
	// tool stops: no further write. It is installed before the first child.
	r := &Runner{T: t, Cfg: cfg}
	defer r.HandleInterrupts(tree.Files, os.Exit)()

	// The baseline: the suite must pass on the tree that is not mutated. There
	// is no way to skip it: a verdict on a mutant of a red tree means nothing.
	t0 := time.Now()
	if err := r.Baseline(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "# baseline: survived (the suite is green on the tree without a mutant), %.1fs\n", time.Since(t0).Seconds())

	done := map[string]bool{}
	if *resume {
		done = doneIDs(absOut)
	}
	f, err := os.OpenFile(absOut, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	head := map[string]any{"header": map[string]any{
		"tree": tree.Hash, "group": set.Group, "total": len(set.Mutants), "selected": len(sel),
		"stages": stageStrings(cfg.Stages), "skip": cfg.Skip, "gomaxprocs": cfg.GoMaxProcs, "gomemlimit": cfg.GoMemLimit,
		"rss_mb": cfg.RSSLimitMB, "wall_slack": cfg.WallSlack.String(), "started": time.Now().UTC().Format(time.RFC3339),
		"missing": set.Missing,
	}}
	if err := enc.Encode(head); err != nil {
		return err
	}

	counts := map[string]int{}
	for _, m := range sel {
		if done[m.ID] {
			continue
		}
		res, err := r.RunMutant(m, tree.Files[m.File])
		r.parkIfStopped() // killed by the interrupt: nothing is written
		if err != nil {
			_ = t.Freeze(tree.Files)
			return fmt.Errorf("%s: %w", m.ID, err)
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
		counts[res.Verdict]++
		counts["total"]++
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%.1fs\t%s\t%s\n", res.ID, res.Verdict, m.Label(), res.Seconds, strings.Join(res.Failed, ","), oneLine(res.Detail, 120))
	}
	fmt.Fprintf(stdout, "# verdicts of this run: %s\n", countsLine(counts))
	after, err := t.ReadTree()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "# tree after sha256:%s restored=%v\n", after.Hash, after.Hash == tree.Hash)
	if after.Hash != tree.Hash {
		return fmt.Errorf("the tree is not restored")
	}
	return nil
}

func stageStrings(st []Stage) []string {
	var s []string
	for _, x := range st {
		s = append(s, fmt.Sprintf("%s;%s;%d;%s", x.Pkg, x.Run, int(x.Timeout.Seconds()), x.Skip))
	}
	return s
}

// inside reports whether p is root or below it.
func inside(root, p string) bool {
	if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(real, filepath.Base(p))
	}
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// requireLinkedWorktree refuses a root that is not a linked worktree: in one
// .git is a file ("gitdir: ..."), in the main worktree a directory. The tool
// writes the files of the root, so it must not be a tree that holds work.
func requireLinkedWorktree(root string) error {
	const hint = "make one with `git worktree add --detach <dir> <commit>` and give it as -root"
	p := filepath.Join(root, ".git")
	st, err := os.Lstat(p)
	switch {
	case err != nil:
		return fmt.Errorf("-root %s is not a linked git worktree (no .git): %s", root, hint)
	case st.IsDir():
		return fmt.Errorf("-root %s is a main worktree (.git is a directory): mutating in place changes a tree that holds work; %s", root, hint)
	}
	b, err := os.ReadFile(p)
	if err != nil || !strings.HasPrefix(string(b), "gitdir:") {
		return fmt.Errorf("-root %s: .git is not a worktree link (no \"gitdir:\"): %s", root, hint)
	}
	return nil
}

func gitStatus(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	return strings.TrimSpace(string(out)), err
}

func selectMutants(all []Mutant, from, to int, only string, every int, filter string) []Mutant {
	var want map[int]bool
	if only != "" {
		want = map[int]bool{}
		for _, s := range strings.Split(only, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				want[n] = true
			}
		}
	}
	if every < 1 {
		every = 1
	}
	var sel []Mutant
	for _, m := range all {
		switch {
		case want != nil && !want[m.Index]:
			continue
		case want == nil && (m.Index < from || (to >= 0 && m.Index >= to)):
			continue
		case want == nil && (m.Index-from)%every != 0:
			continue
		case filter != "" && !strings.Contains(m.Label(), filter):
			continue
		}
		sel = append(sel, m)
	}
	return sel
}

func doneIDs(path string) map[string]bool {
	done := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return done
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r Result
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			done[r.ID] = true
		}
	}
	return done
}

func printReport(w io.Writer, files []string) error {
	// The result of a mutant is the last one written for its id, in the order
	// of the files given: a later file (a rerun) replaces an earlier one.
	last := map[string]Result{}
	for _, path := range files {
		f, err := os.Open(strings.TrimSpace(path))
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var r Result
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == "" {
				continue
			}
			last[r.ID] = r
		}
		f.Close()
	}
	byGroup := map[string]map[string]int{}
	var survivors, crashes []Result
	for _, r := range last {
		if r.Verdict == Crashed {
			crashes = append(crashes, r)
		}
		if byGroup[r.Group] == nil {
			byGroup[r.Group] = map[string]int{}
		}
		byGroup[r.Group][r.Verdict]++
		byGroup[r.Group]["total"]++
		if r.Verdict == Survived {
			survivors = append(survivors, r)
		}
	}
	for _, g := range Groups {
		c := byGroup[g]
		if c == nil {
			continue
		}
		fmt.Fprintf(w, "%s: %s\n", g, countsLine(c))
	}
	byPlace := func(rs []Result) {
		sort.Slice(rs, func(i, j int) bool {
			a, b := rs[i], rs[j]
			if a.Group != b.Group {
				return a.Group < b.Group
			}
			if a.File != b.File {
				return a.File < b.File
			}
			return a.Line < b.Line
		})
	}
	byPlace(survivors)
	byPlace(crashes)
	for _, r := range survivors {
		fmt.Fprintf(w, "SURVIVED %s\t%s:%d\t%s\t%s => %s\n", r.ID, r.File, r.Line, oneLine(r.Note, 100), oneLine(r.Before, 80), oneLine(r.After, 80))
	}
	// A crash is not a kill: each is listed, with the first lines of its output.
	for _, r := range crashes {
		fmt.Fprintf(w, "CRASHED %s\t%s:%d\t%s\t%s => %s\n\t%s\n", r.ID, r.File, r.Line, oneLine(r.Note, 100), oneLine(r.Before, 80), oneLine(r.After, 80), strings.ReplaceAll(cut(r.Detail, 400), "\n", "\n\t"))
	}
	return nil
}
