package starlark_test

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// pairTracer records enter/exit events of the functions and checks that they
// nest: every OnExit closes the function entered last.
type pairTracer struct {
	t      *testing.T
	open   []string
	events []string
}

func (p *pairTracer) tracer() *starlark.Tracer {
	return &starlark.Tracer{
		OnEnter: func(_ *starlark.Thread, fn *starlark.Function, _ starlark.CallSite, _ []starlark.Arg) {
			p.open = append(p.open, fn.Name())
			p.events = append(p.events, "enter "+fn.Name())
		},
		OnExit: func(_ *starlark.Thread, fn *starlark.Function, result starlark.Value, err error) {
			if n := len(p.open); n == 0 || p.open[n-1] != fn.Name() {
				p.t.Errorf("exit %s with open functions %v", fn.Name(), p.open)
			} else {
				p.open = p.open[:n-1]
			}
			if err != nil {
				p.events = append(p.events, "exit "+fn.Name()+" err="+err.Error())
			} else {
				p.events = append(p.events, "exit "+fn.Name()+" ok")
			}
		},
	}
}

// TestTraceExitEveryPath: OnExit is called for every OnEnter on every way a
// function ends: it returns, its body fails, the step limit stops it, the
// thread is cancelled. A call refused before the body runs has neither.
func TestTraceExitEveryPath(t *testing.T) {
	const src = `def inner(n):
    i = 0
    while i < n:
        i += 1
    return i

def middle(n):
    return inner(n) + 1

def outer(n):
    return middle(n) + 1

def fails(n):
    fail("boom")

def stops(n):
    stop()
    return inner(n)

def recur(n):
    return recur(n)
`
	stop := starlark.NewBuiltin("stop", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		th.Cancel("stopped by the test")
		return starlark.None, nil
	})
	load := func(t *testing.T, p *pairTracer) (*starlark.Thread, starlark.StringDict) {
		thread := new(starlark.Thread)
		globals, err := starlark.ExecFileOptions(traceOpts, thread, "t.star", src, starlark.StringDict{"stop": stop})
		if err != nil {
			t.Fatal(err)
		}
		thread.Trace = p.tracer()
		return thread, globals
	}
	call := func(thread *starlark.Thread, fn starlark.Value, args ...starlark.Value) (starlark.Value, error) {
		return starlark.Call(thread, fn, starlark.Tuple(args), nil)
	}
	three := starlark.MakeInt(3)

	t.Run("return", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		if _, err := call(thread, g["outer"], three); err != nil {
			t.Fatal(err)
		}
		diffLines(t, "events", p.events, []string{"enter outer", "enter middle", "enter inner", "exit inner ok", "exit middle ok", "exit outer ok"})
	})
	t.Run("error in the body", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		if _, err := call(thread, g["fails"], three); err == nil {
			t.Fatal("no error")
		}
		diffLines(t, "events", p.events, []string{"enter fails", "exit fails err=fail: boom"})
	})
	t.Run("step limit", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		thread.SetMaxExecutionSteps(thread.Steps + 100)
		_, err := call(thread, g["outer"], starlark.MakeInt(1000000))
		if err == nil || !strings.Contains(err.Error(), "too many steps") {
			t.Fatalf("err = %v, want the step limit", err)
		}
		want := []string{"enter outer", "enter middle", "enter inner"}
		for _, name := range []string{"inner", "middle", "outer"} {
			want = append(want, "exit "+name+" err=Starlark computation cancelled: too many steps")
		}
		diffLines(t, "events", p.events, want)
		if len(p.open) != 0 {
			t.Errorf("open after the limit: %v", p.open)
		}
	})
	t.Run("step limit with OnMaxSteps", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		thread.SetMaxExecutionSteps(thread.Steps + 100)
		thread.OnMaxSteps = func(th *starlark.Thread) { th.Cancel("custom") }
		_, err := call(thread, g["middle"], starlark.MakeInt(1000000))
		if err == nil || !strings.Contains(err.Error(), "custom") {
			t.Fatalf("err = %v", err)
		}
		diffLines(t, "events", p.events, []string{
			"enter middle", "enter inner",
			"exit inner err=Starlark computation cancelled: custom",
			"exit middle err=Starlark computation cancelled: custom"})
	})
	t.Run("cancel", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		_, err := call(thread, g["stops"], starlark.MakeInt(5))
		if err == nil || !strings.Contains(err.Error(), "stopped by the test") {
			t.Fatalf("err = %v, want the cancellation", err)
		}
		diffLines(t, "events", p.events, []string{"enter stops", "exit stops err=Starlark computation cancelled: stopped by the test"})
	})
	t.Run("cancelled before the call", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		thread.Cancel("early")
		if _, err := call(thread, g["outer"], three); err == nil {
			t.Fatal("no error")
		}
		diffLines(t, "events", p.events, []string{"enter outer", "exit outer err=Starlark computation cancelled: early"})
	})
	t.Run("recursion refused", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		if _, err := call(thread, g["recur"], three); err == nil || !strings.Contains(err.Error(), "recursively") {
			t.Fatalf("err = %v", err)
		}
		// the refused inner call is not entered
		diffLines(t, "events", p.events, []string{"enter recur", "exit recur err=function recur called recursively"})
	})
	t.Run("arguments refused", func(t *testing.T) {
		p := &pairTracer{t: t}
		thread, g := load(t, p)
		for _, args := range [][]starlark.Value{nil, {three, three}} {
			if _, err := call(thread, g["inner"], args...); err == nil {
				t.Fatalf("inner%v: no error", args)
			}
		}
		if _, err := starlark.Call(thread, g["inner"], starlark.Tuple{three}, []starlark.Tuple{{starlark.String("zzz"), three}}); err == nil {
			t.Fatal("unexpected keyword argument: no error")
		}
		if len(p.events) != 0 || len(p.open) != 0 {
			t.Errorf("a call refused by the arguments has events %v", p.events)
		}
	})
}

// TestTraceEnterArgsReused: the args slice of OnEnter is the thread's buffer:
// the hook sees the right arguments for each call, also for calls nested in
// each other, and a call costs no allocation more than without a Tracer.
func TestTraceEnterArgsReused(t *testing.T) {
	const src = `def leaf(x, y):
    return x

def mid(a, b, c):
    return leaf(a, b) + leaf(b, c)

def top(p):
    return mid(p, p + 1, p + 2)
`
	thread := new(starlark.Thread)
	globals, err := starlark.ExecFileOptions(traceOpts, thread, "t.star", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	thread.Trace = &starlark.Tracer{OnEnter: func(_ *starlark.Thread, fn *starlark.Function, _ starlark.CallSite, args []starlark.Arg) {
		var as []string
		for _, a := range args {
			as = append(as, fmt.Sprintf("%s=%s", a.Name, a.Value))
		}
		log = append(log, fn.Name()+"("+strings.Join(as, ",")+")")
	}}
	if _, err := starlark.Call(thread, globals["top"], starlark.Tuple{starlark.MakeInt(10)}, nil); err != nil {
		t.Fatal(err)
	}
	diffLines(t, "enter", log, []string{"top(p=10)", "mid(a=10,b=11,c=12)", "leaf(x=10,y=11)", "leaf(x=11,y=12)"})

	args := starlark.Tuple{starlark.MakeInt(10)}
	noop := &starlark.Tracer{OnEnter: func(*starlark.Thread, *starlark.Function, starlark.CallSite, []starlark.Arg) {}}
	measure := func(tr *starlark.Tracer) float64 {
		thread.Trace = tr
		starlark.Call(thread, globals["top"], args, nil) // warm up the buffer
		return testing.AllocsPerRun(50, func() { starlark.Call(thread, globals["top"], args, nil) })
	}
	without, with := measure(nil), measure(noop)
	if with != without {
		t.Errorf("a Tracer with OnEnter allocates %.0f per run, without a Tracer %.0f: the arguments are not reused", with, without)
	}
}
