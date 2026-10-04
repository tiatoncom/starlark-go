package starlark_test

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func lc(p syntax.Position) string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// logTracer returns a Tracer that appends one line per event to *log.
func logTracer(log *[]string) *starlark.Tracer {
	add := func(format string, args ...any) { *log = append(*log, fmt.Sprintf(format, args...)) }
	return &starlark.Tracer{
		OnBranch: func(th *starlark.Thread, fn *starlark.Function, b starlark.Branch, tested starlark.Value, taken bool) {
			neg := ""
			if b.Negated {
				neg = " neg"
			}
			add("branch %s#%d %s tested=%s taken=%v%s", b.Kind, b.Operand, lc(b.Cond), tested, taken, neg)
		},
		OnCompare: func(th *starlark.Thread, fn *starlark.Function, b starlark.Branch, op syntax.Token, x, y starlark.Value, result bool) {
			add("compare %s %s %s %s -> %v", lc(b.Cond), x, op, y, result)
		},
		OnEnter: func(th *starlark.Thread, fn *starlark.Function, call starlark.CallSite, args []starlark.Arg) {
			var as []string
			for _, a := range args {
				as = append(as, a.Name+"="+a.Value.String())
			}
			site := lc(call.Pos)
			if call.Synthetic {
				site = "synthetic"
			}
			top := ""
			if fn.IsToplevel() {
				top = " toplevel"
			}
			add("enter %s%s call=%s args=[%s]", fn.Name(), top, site, strings.Join(as, " "))
		},
		OnExit: func(th *starlark.Thread, fn *starlark.Function, result starlark.Value, err error) {
			if err != nil {
				add("exit %s err=%v", fn.Name(), err)
			} else {
				add("exit %s result=%s", fn.Name(), result)
			}
		},
	}
}

var traceOpts = &syntax.FileOptions{While: true, Set: true, TopLevelControl: true, GlobalReassign: true}

func execTraced(t *testing.T, src string, tr *starlark.Tracer, predeclared starlark.StringDict) (*starlark.Thread, starlark.StringDict, error) {
	t.Helper()
	thread := &starlark.Thread{Trace: tr}
	globals, err := starlark.ExecFileOptions(traceOpts, thread, "t.star", src, predeclared)
	return thread, globals, err
}

func diffLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestTraceEventOrder: Enter, then for a condition Compare before Branch,
// nested calls inside, Exit last.
func TestTraceEventOrder(t *testing.T) {
	src := `def inner(x):
    if x > 1:
        return 1
    return 0

def outer(n):
    if n == 3 and inner(n) == 1:
        return "a"
    return "b"

result = outer(3)
`
	var log []string
	_, globals, err := execTraced(t, src, logTracer(&log), nil)
	if err != nil {
		t.Fatal(err)
	}
	if globals["result"].String() != `"a"` {
		t.Errorf("result = %v", globals["result"])
	}
	diffLines(t, "events", log, []string{
		`enter <toplevel> toplevel call=synthetic args=[]`,
		`enter outer call=11:15 args=[n=3]`,
		`compare 7:8 3 == 3 -> true`,
		`branch and#0 7:8 tested=True taken=true`,
		`enter inner call=7:24 args=[x=3]`,
		`compare 2:8 3 > 1 -> true`,
		`branch if#-1 2:8 tested=True taken=true`,
		`exit inner result=1`,
		`compare 7:19 1 == 1 -> true`,
		`branch and#1 7:19 tested=True taken=true`,
		`exit outer result="a"`,
		`exit <toplevel> result=None`,
	})
}

// TestTraceChains: a chain reports one Branch per operand evaluated; a
// short-circuit shows as the absence of the later operands.
func TestTraceChains(t *testing.T) {
	for _, test := range []struct {
		cond string
		args string
		want string
	}{
		{"a and b and c and d", "0, 1, 1, 1", "and#0=F"},
		{"a and b and c and d", "1, 0, 1, 1", "and#0=T and#1=F"},
		{"a and b and c and d", "1, 1, 0, 1", "and#0=T and#1=T and#2=F"},
		{"a and b and c and d", "1, 1, 1, 0", "and#0=T and#1=T and#2=T and#3=F"},
		{"a and b and c and d", "1, 1, 1, 1", "and#0=T and#1=T and#2=T and#3=T"},
		{"a or b or c or d", "1, 0, 0, 0", "or#0=T"},
		{"a or b or c or d", "0, 1, 0, 0", "or#0=F or#1=T"},
		{"a or b or c or d", "0, 0, 1, 0", "or#0=F or#1=F or#2=T"},
		{"a or b or c or d", "0, 0, 0, 0", "or#0=F or#1=F or#2=F or#3=F"},

		// mixed: a and b or c
		{"a and b or c", "0, 1, 1, 0", "and#0=F or#1=T"},
		{"a and b or c", "1, 0, 1, 0", "and#0=T and#1=F or#1=T"},
		{"a and b or c", "1, 1, 0, 0", "and#0=T and#1=T"},
		{"a and b or c", "0, 0, 0, 0", "and#0=F or#1=F"},
		// mixed: a or b and c
		{"a or b and c", "1, 0, 0, 0", "or#0=T"},
		{"a or b and c", "0, 1, 0, 0", "or#0=F and#0=T and#1=F"},
		{"a or b and c", "0, 0, 1, 0", "or#0=F and#0=F"},
		{"a or b and c", "0, 1, 1, 0", "or#0=F and#0=T and#1=T"},

		// parentheses
		{"a and (b or c)", "0, 1, 1, 0", "and#0=F"},
		{"a and (b or c)", "1, 1, 0, 0", "and#0=T or#0=T"},
		{"a and (b or c)", "1, 0, 1, 0", "and#0=T or#0=F or#1=T"},
		{"a and (b or c)", "1, 0, 0, 0", "and#0=T or#0=F or#1=F"},
		{"(a and b) or c", "0, 1, 1, 0", "and#0=F or#1=T"},
		{"(a and b) or c", "1, 1, 0, 0", "and#0=T and#1=T"},
		{"(a and b) or c", "1, 0, 1, 0", "and#0=T and#1=F or#1=T"},
		{"(a and b) or (c and d)", "0, 1, 1, 0", "and#0=F and#0=T and#1=F"},
		{"not (a and b)", "0, 1, 1, 0", "and#0=F"},
		{"not (a and b)", "1, 1, 1, 0", "and#0=T and#1=T"},
		{"a and b and (c or d)", "1, 1, 0, 1", "and#0=T and#1=T or#0=F or#1=T"},
		{"a and b and (c or d)", "1, 0, 1, 1", "and#0=T and#1=F"},
		{"((a and b) and c) and d", "0, 1, 1, 1", "and#0=F"},
		{"((a and b) and c) and d", "1, 1, 1, 1", "and#0=T and#1=T and#1=T and#1=T"},
	} {
		src := fmt.Sprintf("def f(a, b, c, d):\n    if %s:\n        pass\n", test.cond)
		var got []string
		tr := &starlark.Tracer{OnBranch: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, _ starlark.Value, taken bool) {
			got = append(got, fmt.Sprintf("%s#%d=%s", b.Kind, b.Operand, map[bool]string{true: "T", false: "F"}[taken]))
		}}
		thread, globals, err := execTraced(t, src, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		thread.Trace = tr
		args, err := starlark.ExecFileOptions(traceOpts, new(starlark.Thread), "args.star", "args = ("+test.args+")", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := starlark.Call(thread, globals["f"], args["args"].(starlark.Tuple), nil); err != nil {
			t.Fatal(err)
		}
		if g := strings.Join(got, " "); g != test.want {
			t.Errorf("if %s with (%s):\n got %s\nwant %s", test.cond, test.args, g, test.want)
		}
	}
}

// TestTraceValueContext: and/or that are not conditions report nothing.
func TestTraceValueContext(t *testing.T) {
	src := `def f(a, b, c):
    x = a and b
    y = a or b and c
    z = [a and b for _ in range(2)]
    return x, y, z

def g(h, a, b):
    if h(a and b, lambda: a or b):
        return 1
    return 0

def h(x, y):
    return x

f(1, 0, 1)
g(h, 1, 0)
`
	var log []string
	tr := &starlark.Tracer{
		OnBranch: func(_ *starlark.Thread, fn *starlark.Function, b starlark.Branch, _ starlark.Value, taken bool) {
			log = append(log, fn.Name()+" "+b.Kind.String())
		},
		OnCompare: func(_ *starlark.Thread, fn *starlark.Function, b starlark.Branch, _ syntax.Token, _, _ starlark.Value, _ bool) {
			log = append(log, fn.Name()+" cmp")
		},
	}
	if _, _, err := execTraced(t, src, tr, nil); err != nil {
		t.Fatal(err)
	}
	// the only condition is the if of g, which tests the call h(...)
	diffLines(t, "events", log, []string{"g if"})
}

// TestTraceCompare: OnCompare comes before the OnBranch of the same test, only
// for a comparison whose result a branch tests.
func TestTraceCompare(t *testing.T) {
	for _, test := range []struct {
		cond string
		want string
	}{
		{"a == 1", "cmp:1==1:true br:if#-1=T"},
		{"a != 1", "cmp:1!=1:false br:if#-1=F"},
		{"a < b", "cmp:1<2:true br:if#-1=T"},
		{"b >= a", "cmp:2>=1:true br:if#-1=T"},
		{"a in xs", "cmp:1 in [1]:true br:if#-1=T"},
		{"a not in xs", "cmp:1 in [1]:true br:if#-1=T!"},
		{"not a == 1", "cmp:1==1:true br:if#-1=T!"},
		{"(a == 1)", "cmp:1==1:true br:if#-1=T"},
		{"a == 1 and b == 2", "cmp:1==1:true br:and#0=T cmp:2==2:true br:and#1=T"},
		{"a == 2 and b == 2", "cmp:1==2:false br:and#0=F"},
		{"a == 2 or b == 2", "cmp:1==2:false br:or#0=F cmp:2==2:true br:or#1=T"},
		{"a == 1 and b not in xs", "cmp:1==1:true br:and#0=T cmp:2 in [1]:false br:and#1=F!"},
		{"a", "br:if#-1=T"},
		{"id(a == 1)", "br:if#-1=T"},
		{"a < xs", "error"},
	} {
		src := "def id(x):\n    return x\n\ndef f(a, b, xs):\n    if " + test.cond + ":\n        pass\n"
		var got []string
		tr := &starlark.Tracer{
			OnCompare: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, op syntax.Token, x, y starlark.Value, r bool) {
				sp := ""
				if op == syntax.IN {
					sp = " "
				}
				got = append(got, fmt.Sprintf("cmp:%s%s%s%s%s:%v", x, sp, op, sp, y, r))
			},
			OnBranch: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, _ starlark.Value, taken bool) {
				s := fmt.Sprintf("br:%s#%d=%s", b.Kind, b.Operand, map[bool]string{true: "T", false: "F"}[taken])
				if b.Negated {
					s += "!"
				}
				got = append(got, s)
			},
		}
		thread, globals, err := execTraced(t, src, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		thread.Trace = tr
		xs := starlark.NewList([]starlark.Value{starlark.MakeInt(1)})
		_, err = starlark.Call(thread, globals["f"], starlark.Tuple{starlark.MakeInt(1), starlark.MakeInt(2), xs}, nil)
		g := strings.Join(got, " ")
		if test.want == "error" {
			if err == nil || g != "" {
				t.Errorf("if %s: err = %v, events %q; want an error and no event", test.cond, err, g)
			}
			continue
		}
		if err != nil {
			t.Fatalf("if %s: %v", test.cond, err)
		}
		if g != test.want {
			t.Errorf("if %s:\n got %s\nwant %s", test.cond, g, test.want)
		}
	}
}

// TestTraceWhileEvents: a loop of 100000 iterations reports exactly one
// comparison and one branch per test of the condition.
func TestTraceWhileEvents(t *testing.T) {
	const n = 100000
	src := `def count(n):
    i = 0
    while i < n:
        i += 1
    return i
`
	var branches, compares, taken int
	tr := &starlark.Tracer{
		OnBranch: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, _ starlark.Value, tk bool) {
			branches++
			if tk {
				taken++
			}
			if b.Kind != starlark.BranchWhile {
				t.Fatalf("kind %v", b.Kind)
			}
		},
		OnCompare: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, _ syntax.Token, _, _ starlark.Value, _ bool) {
			compares++
		},
	}
	thread, globals, err := execTraced(t, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	thread.Trace = tr
	if _, err := starlark.Call(thread, globals["count"], starlark.Tuple{starlark.MakeInt(n)}, nil); err != nil {
		t.Fatal(err)
	}
	if branches != n+1 || compares != n+1 || taken != n {
		t.Errorf("branches=%d compares=%d taken=%d, want %d, %d, %d", branches, compares, taken, n+1, n+1, n)
	}
}

// TestTraceEnterArgs: the arguments are the bound parameters by name, with
// defaults, *args and **kwargs.
func TestTraceEnterArgs(t *testing.T) {
	src := `def f(a, b=2, *rest, k, kd=5, **kw):
    return a

f(1, 3, 4, k=7, z=9)
f(1, k=8)
`
	var log []string
	tr := &starlark.Tracer{OnEnter: func(_ *starlark.Thread, fn *starlark.Function, _ starlark.CallSite, args []starlark.Arg) {
		var as []string
		for _, a := range args {
			as = append(as, a.Name+"="+a.Value.String())
		}
		log = append(log, fn.Name()+" "+strings.Join(as, " "))
	}}
	if _, _, err := execTraced(t, src, tr, nil); err != nil {
		t.Fatal(err)
	}
	diffLines(t, "enter", log, []string{
		"<toplevel> ",
		`f a=1 b=3 k=7 kd=5 rest=(4,) kw={"z": 9}`,
		`f a=1 b=2 k=8 kd=5 rest=() kw={}`,
	})
}

// TestTraceExitError: an error inside a function reaches OnExit with err.
func TestTraceExitError(t *testing.T) {
	src := `def g(n):
    fail("boom", n)

def h(n):
    g(n)
    return 1

h(7)
`
	var log []string
	_, _, err := execTraced(t, src, logTracer(&log), nil)
	if err == nil {
		t.Fatal("no error")
	}
	diffLines(t, "events", log, []string{
		`enter <toplevel> toplevel call=synthetic args=[]`,
		`enter h call=8:2 args=[n=7]`,
		`enter g call=5:6 args=[n=7]`,
		`exit g err=fail: boom 7`,
		`exit h err=fail: boom 7`,
		`exit <toplevel> err=fail: boom 7`,
	})
}

// TestTracePanic: a function left by a panic reports no exit; the thread
// stays usable.
func TestTracePanic(t *testing.T) {
	src := `def f():
    boom()
    return 1

def ok():
    return 2

f()
`
	boom := starlark.NewBuiltin("boom", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		panic("boom")
	})
	var log []string
	thread := &starlark.Thread{Trace: logTracer(&log)}
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Errorf("recovered %v, want boom", r)
			}
		}()
		starlark.ExecFileOptions(traceOpts, thread, "t.star", src, starlark.StringDict{"boom": boom})
		t.Error("no panic")
	}()
	diffLines(t, "events", log, []string{
		`enter <toplevel> toplevel call=synthetic args=[]`,
		`enter f call=8:2 args=[]`,
	})
	if d := thread.CallStackDepth(); d != 0 {
		t.Errorf("stack depth %d after panic", d)
	}
	log = nil
	if _, err := starlark.ExecFileOptions(traceOpts, thread, "u.star", "x = 1", nil); err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 {
		t.Errorf("thread no longer traces after a panic: %v", log)
	}
}

// TestTraceCallSite: a call from Starlark code has the position of the call;
// one from a built-in (sorted with key=) or from Go is marked synthetic.
func TestTraceCallSite(t *testing.T) {
	src := `def k(x):
    return -x

def direct():
    return k(1)

def indirect():
    return sorted([1, 3, 2], key=k)

direct()
indirect()
`
	type site struct {
		fn        string
		pos       string
		synthetic bool
	}
	var got []site
	tr := &starlark.Tracer{OnEnter: func(_ *starlark.Thread, fn *starlark.Function, call starlark.CallSite, _ []starlark.Arg) {
		got = append(got, site{fn.Name(), lc(call.Pos), call.Synthetic})
	}}
	thread, globals, err := execTraced(t, src, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := starlark.Call(thread, globals["direct"], nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []site{
		{"<toplevel>", "0:0", true},
		{"direct", "10:7", false},
		{"k", "5:13", false},
		{"indirect", "11:9", false},
		{"k", "0:0", true}, // three elements: the key function is called by sorted
		{"k", "0:0", true},
		{"k", "0:0", true},
		{"direct", "0:0", true}, // called from Go
		{"k", "5:13", false},
	}
	// the position of a built-in frame is "<builtin>", line 0
	for i := range got {
		if got[i].synthetic && got[i].fn == "k" {
			got[i].pos = "0:0"
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("call sites:\n got %v\nwant %v", got, want)
	}
}

// TestTraceReentry: a hook that runs Starlark on the thread it observes gets
// a panic with an explanation, and the thread can be used again.
func TestTraceReentry(t *testing.T) {
	src := `def f(a):
    if a == 1:
        return 1
    return 0

f(1)
`
	for _, hook := range []string{"OnBranch", "OnCompare", "OnEnter", "OnExit"} {
		t.Run(hook, func(t *testing.T) {
			thread := new(starlark.Thread)
			identity := starlark.NewBuiltin("id", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				return args[0], nil
			})
			reenter := func(th *starlark.Thread) {
				starlark.Call(th, identity, starlark.Tuple{starlark.None}, nil)
			}
			tr := new(starlark.Tracer)
			switch hook {
			case "OnBranch":
				tr.OnBranch = func(th *starlark.Thread, _ *starlark.Function, _ starlark.Branch, _ starlark.Value, _ bool) {
					reenter(th)
				}
			case "OnCompare":
				tr.OnCompare = func(th *starlark.Thread, _ *starlark.Function, _ starlark.Branch, _ syntax.Token, _, _ starlark.Value, _ bool) {
					reenter(th)
				}
			case "OnEnter":
				tr.OnEnter = func(th *starlark.Thread, _ *starlark.Function, _ starlark.CallSite, _ []starlark.Arg) { reenter(th) }
			case "OnExit":
				tr.OnExit = func(th *starlark.Thread, _ *starlark.Function, _ starlark.Value, _ error) { reenter(th) }
			}
			thread.Trace = tr
			func() {
				defer func() {
					r := recover()
					msg, _ := r.(string)
					if !strings.Contains(msg, "Tracer hook") {
						t.Errorf("recovered %v, want a panic that mentions the Tracer hook", r)
					}
				}()
				starlark.ExecFileOptions(traceOpts, thread, "t.star", src, nil)
			}()
			// the thread works again once the hooks are gone
			thread.Trace = nil
			if _, err := starlark.ExecFileOptions(traceOpts, thread, "u.star", src, nil); err != nil {
				t.Errorf("thread unusable after the panic: %v", err)
			}
			// and a hook that does not run Starlark is fine
			thread.Trace = &starlark.Tracer{OnBranch: func(*starlark.Thread, *starlark.Function, starlark.Branch, starlark.Value, bool) {}}
			if _, err := starlark.ExecFileOptions(traceOpts, thread, "v.star", src, nil); err != nil {
				t.Error(err)
			}
		})
	}
}

// The Steps of a thread, and so every budget, do not depend on the Tracer:
// TestStepsGolden runs the samples in each of these modes against the golden
// that the v0.2.0 interpreter produced.
func init() {
	noop := &starlark.Tracer{
		OnBranch: func(*starlark.Thread, *starlark.Function, starlark.Branch, starlark.Value, bool) {},
		OnCompare: func(*starlark.Thread, *starlark.Function, starlark.Branch, syntax.Token, starlark.Value, starlark.Value, bool) {
		},
		OnEnter: func(*starlark.Thread, *starlark.Function, starlark.CallSite, []starlark.Arg) {},
		OnExit:  func(*starlark.Thread, *starlark.Function, starlark.Value, error) {},
	}
	stepModes = append(stepModes,
		stepMode{"empty tracer", func(th *starlark.Thread) { th.Trace = new(starlark.Tracer) }},
		stepMode{"no-op hooks", func(th *starlark.Thread) { th.Trace = noop }},
		stepMode{"recording hooks", func(th *starlark.Thread) {
			var log []string
			th.Trace = logTracer(&log)
		}},
	)
}
