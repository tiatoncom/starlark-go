package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Verdicts. A mutant is KILLED only by a failed test or a failed build; a
// timeout, a memory kill and a crash are classes of their own and decide
// nothing. A crash is a test binary that failed without a failed test: a Go
// fatal error, a panic outside a test, a signal (SIGKILL of the system). It is
// not evidence that a test sees the mutant.
const (
	Survived    = "survived"
	KilledTest  = "killed-test"
	KilledBuild = "killed-build"
	Timeout     = "timeout"
	Memory      = "memory"
	Crashed     = "crashed"
)

// Verdicts is the declared order of the verdicts, for the counts.
var Verdicts = []string{KilledTest, KilledBuild, Timeout, Memory, Crashed, Survived}

// countsLine is "total N, killed-test N, ..." of a map of counts by verdict.
func countsLine(c map[string]int) string {
	s := fmt.Sprintf("total %d", c["total"])
	for _, v := range Verdicts {
		s += fmt.Sprintf(", %s %d", v, c[v])
	}
	return s
}

// Stage is one run of one test binary. A mutant survives if every stage passes.
type Stage struct {
	Pkg     string // package directory relative to the root, e.g. "starlark"
	Run     string // -test.run
	Skip    string // -test.skip of this stage, added to Config.Skip
	Timeout time.Duration
}

// Config is everything that decides killed or survived. It is written to the
// header of the results so that a run can be repeated.
type Config struct {
	Stages      []Stage
	Skip        string // -test.skip, shared by the stages
	GoMaxProcs  string
	GoMemLimit  string
	RSSLimitMB  int
	WallSlack   time.Duration
	BinDir      string
	BuildOutCut int
}

// Result is the outcome of one mutant (one line of the results file).
type Result struct {
	ID           string   `json:"id"`
	Group        string   `json:"group"`
	Index        int      `json:"index"`
	File         string   `json:"file"`
	Line         int      `json:"line"`
	Note         string   `json:"note"`
	Before       string   `json:"before"`
	After        string   `json:"after"`
	Verdict      string   `json:"verdict"`
	Detail       string   `json:"detail,omitempty"`
	Failed       []string `json:"failed,omitempty"`
	StageIndex   int      `json:"stage,omitempty"`
	Retry        string   `json:"retry,omitempty"`
	Seconds      float64  `json:"seconds"`
	BuildSeconds float64  `json:"build_seconds"`
	TestSeconds  float64  `json:"test_seconds"`
}

// Runner decides mutants in a target.
type Runner struct {
	T   *Target
	Cfg Config
	// Exec lets a test stand in for the go tool; nil means the real one.
	Exec func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)

	// The children (the go tool, the test binaries) run in process groups of
	// their own, so that the terminal does not interrupt them with the tool:
	// the tool has to kill them itself (KillAll).
	mu      sync.Mutex
	groups  map[int]bool // process groups that are running
	stopped bool         // KillAll was called: no child starts any more
}

var errStopped = errors.New("the tool is stopping")

// startError is a child that could not be started (the binary is missing, no
// permission): not a verdict about the mutant, so it is crashed, not a kill.
type startError struct{ err error }

func (e *startError) Error() string { return "start: " + e.err.Error() }
func (e *startError) Unwrap() error { return e.err }

// begin starts cmd (already in a process group of its own) and registers its
// group, unless the runner is stopped.
func (r *Runner) begin(cmd *exec.Cmd) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return errStopped
	}
	if err := cmd.Start(); err != nil {
		return &startError{err}
	}
	if r.groups == nil {
		r.groups = map[int]bool{}
	}
	r.groups[cmd.Process.Pid] = true
	return nil
}

func (r *Runner) end(pid int) {
	r.mu.Lock()
	delete(r.groups, pid)
	r.mu.Unlock()
}

// KillAll kills the whole process group of every child that runs, and refuses
// to start another.
func (r *Runner) KillAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for pid := range r.groups {
		killGroup(pid)
	}
}

// Stopped reports whether KillAll was called.
func (r *Runner) Stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// parkIfStopped waits for the process to be ended by the interrupt handler: a
// result of a child that was killed by it must not be written or reported.
func (r *Runner) parkIfStopped() {
	if r.Stopped() {
		select {}
	}
}

// Interrupt is what the handler of a signal does: the children are killed (no
// other starts), and then every file is put back to its pristine text and the
// target refuses any later write.
func (r *Runner) Interrupt(pristine map[string]string) error {
	r.KillAll()
	return r.T.Freeze(pristine)
}

// HandleInterrupts calls Interrupt on SIGINT, SIGTERM or SIGHUP, says whether the files
// were restored, and calls exit(130). The returned func stops the handler.
func (r *Runner) HandleInterrupts(pristine map[string]string, exit func(int)) (stop func()) {
	sig := make(chan os.Signal, 1)
	quit := make(chan struct{})
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case <-sig:
			err := r.Interrupt(pristine)
			fmt.Fprintln(os.Stderr, "mutate: interrupted; children killed; files restored:", err == nil)
			exit(130)
		case <-quit:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(quit)
	}
}

// Baseline decides the pristine tree by all the stages. The suite must be green
// on the tree that is not mutated, or no verdict on a mutant means anything.
func (r *Runner) Baseline() error {
	res := r.decide()
	r.parkIfStopped()
	if res.Verdict == Survived {
		return nil
	}
	where := ""
	if res.StageIndex >= 0 && res.StageIndex < len(r.Cfg.Stages) {
		st := r.Cfg.Stages[res.StageIndex]
		where = fmt.Sprintf(" at stage %d (%s, -test.run %s)", res.StageIndex+1, st.Pkg, st.Run)
	}
	msg := fmt.Sprintf("baseline: the tree without a mutant is not green: %s%s", res.Verdict, where)
	if len(res.Failed) > 0 {
		msg += "; failed: " + strings.Join(res.Failed, ", ")
	}
	if res.Detail != "" {
		msg += "\n" + res.Detail
	}
	return errors.New(msg)
}

var (
	reUnused   = regexp.MustCompile(`declared and not used: (\w+)`)
	reNotUsed  = regexp.MustCompile(`is not used`)
	reFailLine = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
	reTimedOut = regexp.MustCompile(`panic: test timed out after`)
)

// RunMutant makes the mutant, decides it, and restores the file. src is the
// pristine content of m.File.
func (r *Runner) RunMutant(m Mutant, src string) (res Result, err error) {
	res = Result{ID: m.ID, Group: m.Group, Index: m.Index, File: m.File, Line: m.Line, Note: m.Note, Before: m.Before, After: m.After}
	start := time.Now()
	defer func() {
		res.Seconds = time.Since(start).Seconds()
		if rerr := r.T.Write(m.File, src); rerr != nil && err == nil {
			err = fmt.Errorf("restore %s: %w", m.File, rerr)
		}
	}()
	if err = r.T.Write(m.File, m.Apply(src)); err != nil {
		return res, err
	}
	d := r.decide()
	if d.Verdict == KilledBuild && (m.After == "error(nil)" || m.After == "0") {
		// The span was an expression the code uses (a call, a price): give the
		// replacement a form that builds, once.
		alt := ""
		x := reUnused.FindStringSubmatch(d.Detail)
		switch {
		case x != nil && m.After == "error(nil)":
			alt = "func() error { _ = " + x[1] + "; return nil }()"
		case x != nil:
			alt = "func() uint64 { _ = " + x[1] + "; return 0 }()"
		case reNotUsed.MatchString(d.Detail) && m.After == "error(nil)":
			alt = "_ = error(nil)"
		}
		if alt != "" {
			if err = r.T.Write(m.File, src[:m.Start]+alt+src[m.End:]); err != nil {
				return res, err
			}
			d = r.decide()
			d.Retry = alt
		}
	}
	d.ID, d.Group, d.Index, d.File, d.Line, d.Note, d.Before, d.After = res.ID, res.Group, res.Index, res.File, res.Line, res.Note, res.Before, res.After
	return d, nil
}

func (r *Runner) env(test bool) []string {
	env := append(os.Environ(), "GOPROXY=off")
	if test {
		env = append(env, "GOMAXPROCS="+r.Cfg.GoMaxProcs, "GOMEMLIMIT="+r.Cfg.GoMemLimit)
	}
	return env
}

func (r *Runner) decide() Result {
	var res Result
	built := map[string]string{}
	for i, st := range r.Cfg.Stages {
		bin, ok := built[st.Pkg]
		if !ok {
			t0 := time.Now()
			bin = filepath.Join(r.Cfg.BinDir, strings.ReplaceAll(st.Pkg, "/", "_")+".test")
			out, err := r.exec(context.Background(), r.T.Root, r.env(false), "go", "test", "-c", "-o", bin, "./"+st.Pkg+"/")
			res.BuildSeconds += time.Since(t0).Seconds()
			var se *startError
			if errors.As(err, &se) {
				res.Verdict = Crashed
				res.Detail = cut(se.Error(), r.buildCut())
				res.StageIndex = i
				return res
			}
			if err != nil {
				res.Verdict = KilledBuild
				res.Detail = cut(strings.TrimSpace(out), r.buildCut())
				res.StageIndex = i
				return res
			}
			built[st.Pkg] = bin
		}
		t0 := time.Now()
		v, detail, failed := r.runStage(bin, st)
		res.TestSeconds += time.Since(t0).Seconds()
		if v != Survived {
			res.Verdict, res.Detail, res.Failed, res.StageIndex = v, detail, failed, i
			return res
		}
	}
	res.Verdict = Survived
	return res
}

func (r *Runner) buildCut() int {
	if r.Cfg.BuildOutCut > 0 {
		return r.Cfg.BuildOutCut
	}
	return 600
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (r *Runner) exec(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	if r.Exec != nil {
		return r.Exec(ctx, dir, env, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	setProcessGroup(cmd)
	if err := r.begin(cmd); err != nil {
		return buf.String(), err
	}
	defer r.end(cmd.Process.Pid)
	err := cmd.Wait()
	return buf.String(), err
}

// limitedBuffer keeps the first cap bytes.
type limitedBuffer struct {
	buf bytes.Buffer
	cap int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.cap - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// runStage runs a built test binary in the directory of its package.
func (r *Runner) runStage(bin string, st Stage) (verdict, detail string, failed []string) {
	args := []string{"-test.run", st.Run, "-test.failfast", "-test.count=1", "-test.timeout", fmt.Sprintf("%ds", int(st.Timeout.Seconds()))}
	var skips []string
	for _, s := range []string{r.Cfg.Skip, st.Skip} {
		if s != "" {
			skips = append(skips, s)
		}
	}
	if len(skips) > 0 {
		args = append(args, "-test.skip", strings.Join(skips, "|"))
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Join(r.T.Root, filepath.FromSlash(st.Pkg))
	cmd.Env = r.env(true)
	out := &limitedBuffer{cap: 4 << 20}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second
	setProcessGroup(cmd)
	if err := r.begin(cmd); err != nil {
		return Crashed, err.Error(), nil // errStopped too: the caller parks
	}
	defer r.end(cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t0 := time.Now()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var runErr error
	reason := ""
loop:
	for {
		select {
		case runErr = <-done:
			break loop
		case <-tick.C:
			if rss := groupRSSMB(cmd.Process.Pid); r.Cfg.RSSLimitMB > 0 && rss > r.Cfg.RSSLimitMB {
				reason = Memory
				detail = fmt.Sprintf("rss %d MB over %d", rss, r.Cfg.RSSLimitMB)
			} else if time.Since(t0) > st.Timeout+r.Cfg.WallSlack {
				reason = Timeout
				detail = fmt.Sprintf("wall time over %s", st.Timeout+r.Cfg.WallSlack)
			}
			if reason != "" {
				killGroup(cmd.Process.Pid)
				<-done
				break loop
			}
		}
	}
	text := out.buf.String()
	switch {
	case reason != "":
		return reason, detail, nil
	case runErr == nil:
		return Survived, "", nil
	case reTimedOut.MatchString(text):
		return Timeout, "test timed out (-test.timeout)", reFailNames(text)
	}
	return classifyFailure(runErr, text)
}

// crashLines and crashBytes bound the output of a crash that is kept.
const (
	crashLines = 12
	crashBytes = 1500
)

// classifyFailure decides a test binary that exited with an error (not a
// timeout, not a kill by the tool): killed-test if a test failed (a "--- FAIL"
// line), else crashed, with the error of the exit and the first lines of the
// output kept as the detail. A crash is not a kill.
func classifyFailure(runErr error, text string) (verdict, detail string, failed []string) {
	if failed = reFailNames(text); len(failed) > 0 {
		return KilledTest, "", failed
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > crashLines {
		lines = lines[:crashLines]
	}
	detail = strings.Join(lines, "\n")
	if runErr != nil {
		detail = runErr.Error() + "\n" + detail
	}
	return Crashed, cut(strings.TrimSpace(detail), crashBytes), nil
}

func reFailNames(text string) []string {
	set := map[string]bool{}
	for _, m := range reFailLine.FindAllStringSubmatch(text, -1) {
		set[m[1]] = true
	}
	var names []string
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// groupRSSMB sums the resident size of the processes of a process group.
func groupRSSMB(pgid int) int {
	out, err := exec.Command("ps", "-A", "-o", "pgid=,rss=").Output()
	if err != nil {
		return 0
	}
	kb := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		g, err1 := strconv.Atoi(f[0])
		n, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil && g == pgid {
			kb += n
		}
	}
	return kb / 1024
}
