package starlark_test

// A differential test against v0.2.0 of the STEP LIMIT: a program that reaches
// SetMaxExecutionSteps must stop at the same step, with the same error (text,
// Go type, backtrace) and the same Thread.Steps / ExecutionSteps() as in
// v0.2.0, whatever the mode in which the host observes it (the default
// Cancel, an OnMaxSteps that counts, raises the limit or cancels with its own
// reason, a limit set on a thread that has run before, a call from Go of a
// function of the program).
//
// The expectations are in testdata/diff_limits_v020.golden. They were produced
// by running this very file, with STARLARK_WRITE_GOLDEN set, in a tree of
// v0.2.0 (the commit d34a821): the file uses only the API that is the same in
// both versions.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const goldenLimits = "testdata/diff_limits_v020.golden"

type limitCase struct {
	Src   string `json:"src"`
	Mode  string `json:"mode"`
	Limit uint64 `json:"limit"`
}

type limitOutcome struct {
	limitCase
	Status    string `json:"status"` // ok, error, panic
	Result    string `json:"result,omitempty"`
	Err       string `json:"err,omitempty"`
	ErrType   string `json:"err_type,omitempty"`
	Backtrace string `json:"backtrace,omitempty"`
	Steps     uint64 `json:"steps"`
	ExecSteps uint64 `json:"exec_steps"`
	OnMax     int    `json:"on_max,omitempty"`
	Steps2    uint64 `json:"steps2,omitempty"` // after the second run on the same thread (modes "reuse*")
	Err2      string `json:"err2,omitempty"`
	Result2   string `json:"result2,omitempty"`
}

var limitOpts = &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}

// limitPredeclared has callit(f, *args): a built-in that calls f from Go, as a
// host function does.
func limitPredeclared() starlark.StringDict {
	callit := starlark.NewBuiltin("callit", func(th *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("callit: no function")
		}
		return starlark.Call(th, args[0], args[1:], kwargs)
	})
	// setlimit(n) is a host function that sets the step limit of the running thread
	setlimit := starlark.NewBuiltin("setlimit", func(th *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		n, _ := starlark.AsInt32(args[0])
		if n < 0 {
			th.SetMaxExecutionSteps(th.Steps + uint64(-n))
		} else {
			th.SetMaxExecutionSteps(uint64(n))
		}
		return starlark.None, nil
	})
	return starlark.StringDict{"callit": callit, "setlimit": setlimit}
}

func describeErr(o *limitOutcome, err error) {
	o.Err = err.Error()
	o.ErrType = fmt.Sprintf("%T", err)
	var ee *starlark.EvalError
	if errors.As(err, &ee) {
		o.Backtrace = ee.Backtrace()
		// (the type under the EvalError is unexported in this version, and was
		// an *errors.errorString in v0.2.0: not compared)
		o.ErrType += "/" + fmt.Sprintf("%T", ee.Unwrap())
		if strings.Contains(o.Err, "Starlark computation cancelled") {
			o.ErrType = fmt.Sprintf("%T", err) + "/cancelled"
		}
	}
}

func runLimitCase(c limitCase) (o limitOutcome) {
	o.limitCase = c
	th := &starlark.Thread{Name: "limit"}
	defer func() {
		if p := recover(); p != nil {
			o.Status = "panic"
			o.Err = fmt.Sprint(p)
		}
		o.Steps = th.Steps
		o.ExecSteps = th.ExecutionSteps()
	}()
	pre := limitPredeclared()
	exec := func(src string) (starlark.StringDict, error) {
		return starlark.ExecFileOptions(limitOpts, th, "p.star", src, pre)
	}
	result := func(g starlark.StringDict) string {
		if r, ok := g["result"]; ok {
			return r.Type() + ":" + r.String()
		}
		return ""
	}
	finish := func(g starlark.StringDict, err error) {
		if err != nil {
			o.Status = "error"
			describeErr(&o, err)
			return
		}
		o.Status = "ok"
		o.Result = result(g)
	}
	switch c.Mode {
	case "cancel":
		th.SetMaxExecutionSteps(c.Limit)
		finish(exec(c.Src))
	case "onmax_count":
		// the hook counts and does not stop: the program runs on, and the hook
		// is called at every step from the limit
		th.OnMaxSteps = func(*starlark.Thread) { o.OnMax++ }
		th.SetMaxExecutionSteps(c.Limit)
		finish(exec(c.Src))
	case "onmax_cancel":
		th.OnMaxSteps = func(t *starlark.Thread) { o.OnMax++; t.Cancel("custom reason") }
		th.SetMaxExecutionSteps(c.Limit)
		finish(exec(c.Src))
	case "onmax_raise":
		// the hook raises the limit by 37 steps, three times, then lets the default stop
		th.OnMaxSteps = func(t *starlark.Thread) {
			o.OnMax++
			if o.OnMax <= 3 {
				t.SetMaxExecutionSteps(t.Steps + 37)
			} else {
				t.Cancel("too many steps")
			}
		}
		th.SetMaxExecutionSteps(c.Limit)
		finish(exec(c.Src))
	case "reuse":
		// a run to the end without a limit, then the same program with a limit
		// that is relative to the steps already taken
		g, err := exec(c.Src)
		if err != nil {
			o.Status = "error"
			describeErr(&o, err)
			return
		}
		o.Steps2 = th.Steps
		th.SetMaxExecutionSteps(th.Steps + c.Limit)
		g2, err := exec(c.Src)
		_ = g
		finish(g2, err)
		o.Result2 = o.Result
		o.Err2 = o.Err
	case "reuse_zero":
		// a limit of 0 set on a thread that has run: whatever v0.2.0 does with it
		_, err := exec("x = 1\n")
		if err != nil {
			o.Status = "error"
			describeErr(&o, err)
			return
		}
		o.Steps2 = th.Steps
		th.SetMaxExecutionSteps(0)
		finish(exec(c.Src))
	case "reuse_clear":
		// a limit set, then cleared by 0, on a thread that has not run
		th.SetMaxExecutionSteps(c.Limit)
		th.SetMaxExecutionSteps(0)
		finish(exec(c.Src))
	case "inrun":
		// the program sets the limit itself while it runs (limit 0 on a thread that
		// has run: v0.2.0 stops at the next step), with or without a counting hook
		th.OnMaxSteps = func(t *starlark.Thread) {
			o.OnMax++
			if o.OnMax >= 3 {
				t.Cancel("too many steps")
			}
		}
		finish(exec(c.Src))
	case "inrun_default":
		finish(exec(c.Src))
	case "call":
		// the program defines f; the host calls f with a limit relative to the steps taken
		g, err := exec(c.Src)
		if err != nil {
			o.Status = "error"
			describeErr(&o, err)
			return
		}
		o.Steps2 = th.Steps
		th.SetMaxExecutionSteps(th.Steps + c.Limit)
		v, err := starlark.Call(th, g["f"], starlark.Tuple{starlark.MakeInt(7)}, nil)
		if err != nil {
			o.Status = "error"
			describeErr(&o, err)
			return
		}
		o.Status = "ok"
		o.Result = v.Type() + ":" + v.String()
	default:
		panic("unknown mode " + c.Mode)
	}
	return
}

func limitStepsOf(src string) uint64 {
	th := &starlark.Thread{Name: "limit"}
	_, _ = starlark.ExecFileOptions(limitOpts, th, "p.star", src, limitPredeclared())
	return th.Steps
}

// limitSweepPrograms are small: every limit from 1 to a little over their steps
// is tried.
var limitSweepPrograms = []string{
	"x = 0\nfor i in range(12):\n    x += i\nresult = x\n",
	"def f(a, b=2):\n    return a * b\nresult = [f(i) for i in range(6)] + [f(1, 3)]\n",
	"result = sorted([3, 1, 2, 5, 4], key=lambda v: -v)\n",
	"def key(v):\n    return v % 3\nresult = sorted(range(8), key=key)\n",
	"def g(x):\n    return x + 1\ndef f(x):\n    return callit(g, x) + callit(g, 2)\nresult = f(1)\n",
	"d = {}\nfor i in range(8):\n    d['k%d' % i] = i\nresult = sorted(d.items())[:3]\n",
	"i = 0\nwhile i < 9:\n    i += 1\nresult = i\n",
	"def f():\n    fail('boom')\nresult = f()\n",
	"x = [1, 2, 3]\nx[5] = 1\n",
	"s = ''\nfor c in 'abcdef'.elems():\n    s += c.upper()\nresult = s\n",
	"result = [x * y for x in range(4) for y in range(3) if x != y]\n",
	"def f(*a, **k):\n    return len(a) + len(k)\nresult = f(1, 2, a=3) + f(*[1, 2], **{'z': 1})\n",
	"result = max([1, 5, 3], key=lambda x: -x) + min(range(5), key=lambda x: (x - 2) * (x - 2))\n",
	"l = [5, 3, 1]\nl.sort()\nresult = (l, any([x > 4 for x in l]), all([x > 0 for x in l]))\n",
	"s = set()\nfor i in range(6):\n    s.add(i % 4)\nresult = len(s)\n",
}

// limitBigPrograms are larger: the limits are chosen around their steps.
var limitBigPrograms = []string{
	"x = 0\nfor i in range(3000):\n    x += i * 2 % 7\nresult = x\n",
	"def fib(n):\n    a, b = 0, 1\n    for _ in range(n):\n        a, b = b, a + b\n    return a\nresult = [fib(i) for i in range(60)]\n",
	"l = [(i * 7919) % 1009 for i in range(400)]\nresult = sorted(l, key=lambda v: (v % 10, -v))[:5]\n",
	"d = {}\nfor i in range(500):\n    d['key-%d' % i] = i\nresult = len(d) + sum([v for v in d.values()])\n",
	"ids = ['order-' + str(i) + '-abcdef' for i in range(300)]\nresult = len(set(ids))\n",
	"l = list(range(2000))\nresult = sorted(l, reverse=True)[0] + len(str(l)) + len(','.join([str(x) for x in l]))\n",
	"x = 'a' * 100000\nresult = len(x.upper()) + len(x + x) + x.count('a')\n",
}

var limitCallPrograms = []string{
	"def f(n):\n    t = 0\n    for i in range(n * 20):\n        t += i\n    return t\n",
	"def f(n):\n    return sorted(range(n * 10), key=lambda v: -v)[0]\n",
	"def g(n):\n    return n + 1\ndef f(n):\n    return sum([callit(g, i) for i in range(n * 5)])\n",
}

func limitCorpus() []limitCase {
	var out []limitCase
	for _, src := range limitSweepPrograms {
		total := limitStepsOf(src)
		for lim := uint64(1); lim <= total+3; lim++ {
			out = append(out, limitCase{Src: src, Mode: "cancel", Limit: lim})
		}
		for _, mode := range []string{"onmax_count", "onmax_cancel", "onmax_raise"} {
			for _, lim := range []uint64{1, 2, total / 3, total / 2, total - 1, total, total + 1} {
				if lim > 0 {
					out = append(out, limitCase{Src: src, Mode: mode, Limit: lim})
				}
			}
		}
		for _, lim := range []uint64{1, 3, total / 2, total - 1, total, total + 1} {
			if lim > 0 {
				out = append(out, limitCase{Src: src, Mode: "reuse", Limit: lim})
			}
		}
		out = append(out,
			limitCase{Src: src, Mode: "reuse_zero"},
			limitCase{Src: src, Mode: "reuse_clear", Limit: total / 2},
		)
	}
	for _, src := range limitBigPrograms {
		total := limitStepsOf(src)
		for _, lim := range []uint64{1, 2, 7, 100, total / 4, total / 2, total - 2, total - 1, total, total + 1, total + 100} {
			if lim > 0 {
				out = append(out, limitCase{Src: src, Mode: "cancel", Limit: lim})
			}
		}
		for _, mode := range []string{"onmax_cancel", "onmax_raise"} {
			for _, lim := range []uint64{5, total / 2, total - 1, total} {
				out = append(out, limitCase{Src: src, Mode: mode, Limit: lim})
			}
		}
		out = append(out,
			limitCase{Src: src, Mode: "reuse", Limit: total / 2},
			limitCase{Src: src, Mode: "reuse_zero"},
		)
	}
	for _, src := range []string{
		"x = 1\nsetlimit(0)\nresult = x + 1\n",
		"def f():\n    setlimit(0)\n    return 3\nresult = f()\n",
		"x = 1\nsetlimit(-5)\nresult = [x for _ in range(20)]\n",
		"x = 1\nsetlimit(-50)\nsetlimit(0)\nresult = x\n",
		"x = 1\nsetlimit(100000)\nsetlimit(0)\nresult = x\n",
		"setlimit(0)\nresult = 1\n",
	} {
		out = append(out,
			limitCase{Src: src, Mode: "inrun"},
			limitCase{Src: src, Mode: "inrun_default"},
		)
	}
	for _, src := range limitCallPrograms {
		for _, lim := range []uint64{1, 2, 3, 5, 10, 17, 40, 100, 150, 300, 600, 1000, 5000} {
			out = append(out, limitCase{Src: src, Mode: "call", Limit: lim})
		}
	}
	return out
}

func writeLimitsGolden(t *testing.T, path string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	for _, c := range limitCorpus() {
		b, _ := json.Marshal(runLimitCase(c))
		w.Write(b)
		w.WriteByte('\n')
	}
}

// TestWriteLimitsGolden writes the golden file. It is run in a tree of v0.2.0,
// not here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark package>.
func TestWriteLimitsGolden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden file (in a tree of v0.2.0)")
	}
	refuseInV030(t)
	writeLimitsGolden(t, dir+"/"+goldenLimits)
}

// TestDifferentialStepLimit: a program that reaches the step limit stops at
// the same step, with the same error and the same ExecutionSteps() as in
// v0.2.0, in every mode (B3 of the review of compatibility).
func TestDifferentialStepLimit(t *testing.T) {
	f, err := os.Open(goldenLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	n, hit := 0, 0
	var bad []string
	shown := map[string]int{}
	ndiff := 0
	for sc.Scan() {
		var g limitOutcome
		if err := json.Unmarshal(sc.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		n++
		if g.Status == "error" && strings.Contains(g.Err, "too many steps") {
			hit++
		}
		got := runLimitCase(g.limitCase)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(g)
		if string(gb) != string(wb) {
			// (the first two of each mode are shown)
			if shown[g.Mode]++; shown[g.Mode] <= 2 {
				bad = append(bad, fmt.Sprintf("%.100q mode=%s limit=%d\n   v0.2.0: %.400s\n   now:    %.400s", g.Src, g.Mode, g.Limit, wb, gb))
			}
			ndiff++
		}
	}
	if n < 1000 || hit < 400 {
		t.Fatalf("the golden file has %d cases, %d of them reach the limit: too few", n, hit)
	}
	if ndiff > 0 {
		t.Fatalf("%d of %d cases differ from v0.2.0:\n%s", ndiff, n, strings.Join(bad, "\n"))
	}
	t.Logf("%d cases (%d reach the limit): identical to v0.2.0", n, hit)
}
