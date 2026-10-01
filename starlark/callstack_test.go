package starlark

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

func chainProgram(n int) string {
	var b strings.Builder
	for i := 0; i < n-1; i++ {
		fmt.Fprintf(&b, "def f%d(): return f%d()\n", i, i+1)
	}
	fmt.Fprintf(&b, "def f%d(): return 1\nresult = f0()\n", n-1)
	return b.String()
}

func execDeep(t *testing.T, src string, depth int) (*Thread, StringDict, error) {
	th := &Thread{Name: "deep"}
	th.SetMaxCallStackDepth(depth)
	g, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", src, nil)
	return th, g, err
}

// A chain of different functions deeper than the limit of the thread is an
// error of Starlark, with or without recursion; and one under it is not.
func TestCallStack_DepthIsBoundedWithAndWithoutRecursion(t *testing.T) {
	th, g, err := execDeep(t, chainProgram(900), 1000)
	if err != nil || g["result"] != MakeInt(1) {
		t.Fatalf("a chain of 900 functions with a limit of 1000: %v", err)
	}
	_ = th
	_, _, err = execDeep(t, chainProgram(1100), 1000)
	if err == nil || !strings.Contains(err.Error(), "Starlark stack overflow") {
		t.Fatalf("a chain of 1100 functions with a limit of 1000: %v", err)
	}
	// With recursion allowed.
	th = &Thread{Name: "rec"}
	th.SetMaxCallStackDepth(500)
	_, err = ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, Recursion: true}, th, "t.star", "def f(n):\n    return f(n + 1) if n < 100000 else 0\nf(0)\n", nil)
	if err == nil || !strings.Contains(err.Error(), "Starlark stack overflow") {
		t.Fatalf("recursion with a limit of 500: %v", err)
	}
	// The default is the old limit of recursion.
	if got := (&Thread{}).maxCallDepth(); got != 100_000 {
		t.Errorf("default depth %d", got)
	}
	th = &Thread{}
	th.SetMaxCallStackDepth(7)
	th.SetMaxCallStackDepth(0)
	if th.maxCallDepth() != DefaultMaxCallStackDepth {
		t.Errorf("0 does not restore the default")
	}
}

// The limit holds through a built-in that calls back (sorted with a key), where
// the frames of the built-ins are in the stack as well.
func TestCallStack_DepthThroughBuiltins(t *testing.T) {
	src := "def f(n):\n    return sorted([1, 2], key=lambda x: g(n))\ndef g(n):\n    return 0\nresult = f(1)\n"
	_, g, err := execDeep(t, src, 100)
	if err != nil || g["result"].String() != "[1, 2]" {
		t.Fatal(err)
	}
	_, _, err = execDeep(t, src, 3)
	if err == nil || !strings.Contains(err.Error(), "Starlark stack overflow") {
		t.Fatalf("err = %v", err)
	}
}

// A call looks at no more than a bounded number of frames to see if its
// function is active, however deep the stack is (not the depth: the product).
func TestCallStack_RecursionCheckIsNotLinearInTheDepth(t *testing.T) {
	const n = 5000
	scans, err := countScans(t, strings.Replace(chainProgram(n), "result = f0()", "result = f0()", 1))
	_ = scans
	if err != nil {
		t.Fatal(err)
	}
	if bound := n * (recursionScanDepth + 2); scans["recursion"] > bound {
		t.Errorf("%d frames were looked at for %d calls, at most %d", scans["recursion"], n, bound)
	}
	// And the check still finds a recursion, deep or not.
	deep := chainProgram(100) + "def loop(n):\n    return f0() if n == 0 else loop(n - 1)\n"
	_, _, err = execDeep(t, strings.Replace(deep, "def f99(): return 1", "def f99(): return f98()", 1), 1000)
	if err == nil || !strings.Contains(err.Error(), "called recursively") {
		t.Fatalf("a recursion at depth 99 was not found: %v", err)
	}
	// The map of a deep stack is gone when the stack is shallow again.
	th2, _, err := execDeep(t, chainProgram(200)+"result2 = f0()\n", 1000)
	if err != nil || th2.active != nil {
		t.Errorf("err %v, active map %v after the calls", err, th2.active)
	}
}
