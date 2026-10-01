package starlark_test

// The Go stack and the heap that a level of the stack of calls takes, for the
// worst frames: measured, for the host that chooses the limit of the depth
// (Thread.SetMaxCallStackDepth). Run by hand:
//
//	STARLARK_MEASURE_STACK=1 go test -run TestMeasureStackPerLevel -v ./starlark/
//
// The stack is found by running the chain in a child process with a limit of
// the stack of Go (debug.SetMaxStack), and bisecting the limit: the least that
// the chain does not overflow is the stack it needs.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type depthCase struct {
	name string
	body func(i int) string
	last string
}

func depthCases() []depthCase {
	var ps, ls []string
	for i := 0; i < 250; i++ {
		ps = append(ps, fmt.Sprintf("p%d=0", i))
		ls = append(ls, fmt.Sprintf("    v%d = %d", i, i))
	}
	params, locals := strings.Join(ps, ", "), strings.Join(ls, "\n")
	return []depthCase{
		{"plain: def f(): return g()", func(i int) string { return fmt.Sprintf("def f%d(): return f%d()\n", i, i+1) }, "return 1"},
		{"250 parameters", func(i int) string { return fmt.Sprintf("def f%d(%s): return f%d()\n", i, params, i+1) }, "return 1"},
		{"250 locals", func(i int) string { return fmt.Sprintf("def f%d():\n%s\n    return f%d()\n", i, locals, i+1) }, "return 1"},
		{"through sorted(key=)", func(i int) string {
			return fmt.Sprintf("def f%d(): return sorted([1], key=lambda x: f%d())\n", i, i+1)
		}, "return [1]"},
		{"through max(key=)", func(i int) string { return fmt.Sprintf("def f%d(): return max([1], key=lambda x: f%d())\n", i, i+1) }, "return 1"},
		{"through a comprehension", func(i int) string { return fmt.Sprintf("def f%d(): return [f%d() for x in [1]][0]\n", i, i+1) }, "return 1"},
		{"through str(f())", func(i int) string { return fmt.Sprintf("def f%d(): return str(f%d())\n", i, i+1) }, "return 1"},
		{"through a keyword call f(**d)", func(i int) string { return fmt.Sprintf("def f%d(**k): return f%d(**k)\n", i, i+1) }, "return 1"},
	}
}

func depthProgram(c depthCase, n int) string {
	var b strings.Builder
	for i := 0; i < n-1; i++ {
		b.WriteString(c.body(i))
	}
	kw := strings.Contains(c.name, "**")
	fmt.Fprintf(&b, "def f%d(", n-1)
	if kw {
		b.WriteString("**k")
	}
	fmt.Fprintf(&b, "):\n    %s\n", c.last)
	if kw {
		b.WriteString("r = f0(a=1)\n")
	} else {
		b.WriteString("r = f0()\n")
	}
	return b.String()
}

func runDepth(c depthCase, n int) (heap float64, frames int, err error) {
	th := &starlark.Thread{Name: "d"}
	th.SetMaxCallStackDepth(5*n + 10)
	prog := depthProgram(c, n)
	var m0 runtime.MemStats
	probe := starlark.NewBuiltin("probe", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		runtime.GC()
		runtime.ReadMemStats(&m0)
		return starlark.None, nil
	})
	var m1 runtime.MemStats
	probe2 := starlark.NewBuiltin("probe2", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		runtime.ReadMemStats(&m1)
		frames = th.CallStackDepth()
		return starlark.None, nil
	})
	prog = strings.Replace(prog, "r = f0(", "probe()\nr = f0(", 1)
	prog = strings.Replace(prog, c.last+"\n", strings.Replace(c.last, "return ", "probe2()\n    return ", 1)+"\n", 1)
	_, err = starlark.ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", prog, starlark.StringDict{"probe": probe, "probe2": probe2})
	return float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc)) / float64(n), frames, err
}

func TestMeasureStackPerLevel(t *testing.T) {
	if name := os.Getenv("STARLARK_DEPTH_CHILD"); name != "" {
		parts := strings.Split(name, "|")
		var c depthCase
		for _, d := range depthCases() {
			if d.name == parts[0] {
				c = d
			}
		}
		n, _ := strconv.Atoi(parts[1])
		limit, _ := strconv.Atoi(parts[2])
		debug.SetMaxStack(limit)
		if _, _, err := runDepth(c, n); err != nil {
			fmt.Printf("DEPTH-ERR %v\n", err)
			os.Exit(3)
		}
		fmt.Println("DEPTH-OK")
		return
	}
	if os.Getenv("STARLARK_MEASURE_STACK") == "" {
		t.Skip("set STARLARK_MEASURE_STACK=1 to measure the stack of a level")
	}
	// The least stack of Go that a chain of n levels needs is found by
	// bisecting n for a fixed limit of the stack: the deepest chain that does
	// not overflow it. (The stack of Go grows by doubling, so the limit
	// itself is what a chain of that depth needs, to the level.)
	const limit = 16 << 20
	for _, c := range depthCases() {
		heap, frames, err := runDepth(c, 1000)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		perN := float64(frames) / 1000 // frames of the stack of calls for a level of the chain
		ok := func(n int) bool {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMeasureStackPerLevel$", "-test.count=1")
			cmd.Env = append(os.Environ(), fmt.Sprintf("STARLARK_DEPTH_CHILD=%s|%d|%d", c.name, n, limit))
			out, _ := cmd.CombinedOutput()
			return strings.Contains(string(out), "DEPTH-OK")
		}
		lo, hi := 200, 12000
		for hi-lo > 100 {
			mid := (lo + hi) / 2
			if ok(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		fmt.Printf("DEPTH %-34s %5.0f B of Go stack a frame (%.1f frames a level, %d levels in %d MiB), %5.0f B of heap a level\n",
			c.name, float64(limit)/(float64(lo)*perN), perN, lo, limit>>20, heap)
	}
}
