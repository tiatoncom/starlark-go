//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// scriptExec stands in for `go test -c -o bin`: it writes a shell script as the
// test binary.
func scriptExec(script string) func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	return func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				return "", os.WriteFile(args[i+1], []byte("#!/bin/sh\n"+script+"\n"), 0o755)
			}
		}
		return "", errors.New("no -o")
	}
}

func scriptRunner(t *testing.T, script string) (*Runner, *Tree) {
	t.Helper()
	root, tree := copyTree(t)
	target, err := NewTarget(root)
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{
		T:    target,
		Cfg:  Config{Stages: []Stage{{Pkg: "starlark", Run: ".", Timeout: 30 * time.Second}}, GoMaxProcs: "1", GoMemLimit: "1GiB", BinDir: t.TempDir()},
		Exec: scriptExec(script),
	}, tree
}

// The baseline passes on a green suite and aborts, with the reason, on a red
// tree and on a crash.
func TestBaselineMustBeGreen(t *testing.T) {
	r, _ := scriptRunner(t, "exit 0")
	if err := r.Baseline(); err != nil {
		t.Fatalf("a green suite: %v", err)
	}
	for _, c := range []struct{ script, verdict, text string }{
		{"echo '--- FAIL: TestRed (0.00s)'; exit 1", KilledTest, "TestRed"},
		{"echo 'fatal error: all goroutines are asleep'; exit 2", Crashed, "fatal error: all goroutines are asleep"},
		{"exit 3", Crashed, "exit status 3"},
	} {
		r, _ := scriptRunner(t, c.script)
		err := r.Baseline()
		if err == nil || !strings.Contains(err.Error(), "not green") || !strings.Contains(err.Error(), c.verdict) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%q: %v", c.script, err)
		}
	}
	// A tree that does not build is not green either.
	r, _ = scriptRunner(t, "")
	r.Exec = func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		return "starlark/x.go:1:1: syntax error", errors.New("exit 1")
	}
	if err := r.Baseline(); err == nil || !strings.Contains(err.Error(), KilledBuild) || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("a build that fails: %v", err)
	}
}

// A test binary that dies without a failed test is crashed, not killed-test.
func TestRunStageCrashIsNotAKill(t *testing.T) {
	r, _ := scriptRunner(t, "echo 'fatal error: concurrent map writes'; echo second; exit 2")
	res := r.decide()
	if res.Verdict != Crashed || len(res.Failed) != 0 {
		t.Fatalf("verdict %s failed %v", res.Verdict, res.Failed)
	}
	if !strings.Contains(res.Detail, "fatal error: concurrent map writes") || !strings.Contains(res.Detail, "second") {
		t.Errorf("detail %q", res.Detail)
	}
	r, _ = scriptRunner(t, "kill -KILL $$")
	if res := r.decide(); res.Verdict != Crashed || !strings.Contains(res.Detail, "signal: killed") {
		t.Errorf("SIGKILL: %s %q", res.Verdict, res.Detail)
	}
	r, _ = scriptRunner(t, "echo '--- FAIL: TestX (0.00s)'; exit 1")
	if res := r.decide(); res.Verdict != KilledTest || len(res.Failed) != 1 || res.Failed[0] != "TestX" {
		t.Errorf("failed test: %s %v", res.Verdict, res.Failed)
	}
}

// sleeper is a test binary that starts a grandchild that sleeps, and writes the
// pid of it to pidfile.
func sleeper(pidfile string) string {
	return "sleep 60 &\necho $! > " + pidfile + "\nwait"
}

func waitPid(t *testing.T, pidfile string) int {
	t.Helper()
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(pidfile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the child did not start")
	return 0
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d is alive", pid)
}

func waitReturn(t *testing.T, done <-chan Result) Result {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(20 * time.Second):
		t.Fatal("the stage did not return after the kill")
		return Result{}
	}
}

// KillAll kills the whole process group of a test binary (its grandchildren
// too), and no child starts after it.
func TestKillAllKillsTheProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r, _ := scriptRunner(t, sleeper(pidfile))
	done := make(chan Result, 1)
	go func() { done <- r.decide() }()
	pid := waitPid(t, pidfile)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the grandchild is not running: %v", err)
	}
	r.KillAll()
	waitReturn(t, done)
	waitDead(t, pid)

	// No child starts after it.
	pidfile2 := filepath.Join(t.TempDir(), "pid")
	r.Exec = scriptExec(sleeper(pidfile2))
	r.decide()
	if _, err := os.Stat(pidfile2); err == nil {
		t.Error("a child started after KillAll")
	}
}

// A signal kills the children, puts the files back, and exits with 130.
func TestInterruptKillsChildrenAndRestoresFiles(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	r, tree := scriptRunner(t, sleeper(pidfile))
	if err := r.T.Write(fWork, "garbage"); err != nil { // a mutant is in place
		t.Fatal(err)
	}
	code := make(chan int, 1)
	stop := r.HandleInterrupts(tree.Files, func(c int) { code <- c })
	defer stop()
	done := make(chan Result, 1)
	go func() { done <- r.decide() }()
	pid := waitPid(t, pidfile)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-code:
		if c != 130 {
			t.Errorf("exit code %d", c)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the handler did not run")
	}
	waitReturn(t, done)
	waitDead(t, pid)
	if got, _ := r.T.ReadTree(); got.Hash != tree.Hash {
		t.Error("the files are not restored")
	}
	if err := r.T.Write(fWork, "more garbage"); err == nil {
		t.Error("the target accepted a write after the interrupt")
	}
}
