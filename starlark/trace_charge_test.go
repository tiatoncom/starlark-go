package starlark_test

// The trace hooks charge nothing. A Tracer is the host's eye on a thread, not
// part of its computation: with a hook installed (even one that reads the
// values it is given) the steps, the work and the bytes charged to the thread
// are those of the same program without a Tracer, and so is the place where a
// budget refuses. TestStepsGolden pins the steps to the v0.2.0 interpreter;
// this test pins the other two counters (and the steps again, next to them) to
// the run without a hook, on programs of every kind of operation.

import (
	"fmt"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// chargeSamples are the programs of the test: the step samples (calls, loops,
// chains of conditions, a failing call) and programs of the operations that
// charge work and bytes. Each one leaves its answer in the global "result".
var chargeSamples = map[string]string{
	"strings": `
def f(words):
    out = []
    for w in words:
        if w.startswith("a") or len(w) > 3:
            out.append(w.upper() + "-" + w[1:])
        elif "b" in w and w != "bob":
            out.append(w * 3)
        else:
            out.append("%s/%d" % (w, len(w)))
    return ",".join(out).replace("-", "+").split(",")

result = str(f(["apple", "bob", "cat", "dodo", "ab"] * 40)[:7])
`,
	"lists_dicts_sets": `
def g(n):
    d = {}
    l = []
    for i in range(n):
        d[str(i)] = [i, i * 2]
        l += [i] if i % 3 == 0 else []
        if i in l and d.get(str(i)) != None:
            l.append(len(d))
    s = sorted(l, key=lambda v: -v)
    e = {k: v for k, v in d.items() if v[0] > n - 5}
    t = set(l) | set(range(5)) - set([1])
    return len(d), s[:5], e, sorted(t)[:4], list(reversed(l))[:3]

result = str(g(300))
`,
	"json": `
data = {"a": [1, 2, 3, {"b": "x" * 50}], "c": None, "d": 1.5, "e": [True, False]}

def h(n):
    t = 0
    for i in range(n):
        s = json.encode(data)
        v = json.decode(s)
        if v["a"][3]["b"] == "x" * 50 and v != None:
            t += len(s)
    return t, json.indent(json.encode(data))

result = str(h(25))
`,
	"ints_bytes_floats": `
def p(n):
    a = 1
    for i in range(n):
        a = a * 3 + i
        if a > 1000000000000000000000000000000 and a % 2 == 0:
            a = a // 7
    b = bytes("hello") * 5
    return a, len(b), 1.5 * n, n << 40, -n

result = str(p(80))
`,
	"failing_builtin": `
def k(s):
    if s == "" or s == "x":
        return 0
    return int(s)

result = str([k("1"), k("22"), k("zz")])
`,
	"failing_compare": `
def c(xs):
    n = 0
    for x in xs:
        if x < 3 or x == None:
            n += 1
    return n

result = str(c([1, 2, 3, "a"]))
`,
	"refused_recursion": `
def r(n):
    if n > 0:
        return r(n - 1)
    return 0

result = str(r(3))
`,
}

// chargeFailing are the samples that end on an error (and every other one must
// end without).
var chargeFailing = map[string]bool{
	"failing_builtin":   true,
	"failing_compare":   true,
	"refused_recursion": true,
	"steps/failing":     true,
}

func init() {
	for name, src := range stepSamples {
		chargeSamples["steps/"+name] = src
	}
}

// chargeCount is what a recording tracer saw.
type chargeCount struct {
	branch, compare, enter, exit int
	read                         int // bytes of text the hooks made of the values
}

func (c chargeCount) events() int { return c.branch + c.compare + c.enter + c.exit }

// touch reads a value as a host that records a trace does: its text, its
// truth, its type, its length, and the elements of a container.
func touch(v starlark.Value) int {
	if v == nil {
		return 0
	}
	n := len(v.String()) + len(v.Type())
	if v.Truth() {
		n++
	}
	if l, ok := v.(starlark.Indexable); ok {
		for i := 0; i < l.Len(); i++ {
			n += len(l.Index(i).String())
		}
	}
	if it, ok := v.(starlark.Iterable); ok {
		iter := it.Iterate()
		var e starlark.Value
		for iter.Next(&e) {
			n += len(e.Type())
		}
		iter.Done()
	}
	if m, ok := v.(starlark.IterableMapping); ok {
		for _, item := range m.Items() {
			n += len(item[0].String()) + len(item[1].String())
		}
	}
	return n
}

// readingTracer is a tracer that sets all four hooks and reads everything they
// are given.
func readingTracer(c *chargeCount) *starlark.Tracer {
	return &starlark.Tracer{
		OnBranch: func(th *starlark.Thread, fn *starlark.Function, b starlark.Branch, tested starlark.Value, taken bool) {
			c.branch++
			c.read += touch(tested) + len(fn.Name()) + len(fn.Position().String()) + len(fmt.Sprint(b))
			_ = th.CallStack().At(0).Pos.String()
		},
		OnCompare: func(th *starlark.Thread, fn *starlark.Function, b starlark.Branch, op syntax.Token, x, y starlark.Value, result bool) {
			c.compare++
			c.read += touch(x) + touch(y) + len(op.String()) + len(fmt.Sprint(b))
		},
		OnEnter: func(th *starlark.Thread, fn *starlark.Function, call starlark.CallSite, args []starlark.Arg) {
			c.enter++
			c.read += len(fn.Name()) + len(call.Pos.String())
			for _, a := range args {
				c.read += len(a.Name) + touch(a.Value)
			}
		},
		OnExit: func(th *starlark.Thread, fn *starlark.Function, result starlark.Value, err error) {
			c.exit++
			c.read += touch(result)
			if err != nil {
				c.read += len(err.Error())
			}
		},
	}
}

// chargeRun is the counters of a thread after a program, and what it ended on.
type chargeRun struct {
	steps, work, bytes uint64
	result, err        string
}

func (r chargeRun) String() string {
	return fmt.Sprintf("steps=%d work=%d bytes=%d result=%q err=%q", r.steps, r.work, r.bytes, r.result, r.err)
}

// runCharged runs a sample on a fresh thread, with a Tracer (or none) and the
// limits (0: none) of work and of bytes.
func runCharged(name string, tr *starlark.Tracer, maxWork, maxBytes uint64) chargeRun {
	thread := &starlark.Thread{Name: name, Trace: tr}
	if maxWork > 0 {
		thread.SetMaxWork(maxWork)
	}
	if maxBytes > 0 {
		thread.SetMaxAllocBytes(maxBytes)
	}
	opts := &syntax.FileOptions{While: true, Set: true, TopLevelControl: true, GlobalReassign: true}
	predeclared := starlark.StringDict{"json": json.Module}
	globals, err := starlark.ExecFileOptions(opts, thread, name+".star", chargeSamples[name], predeclared)
	r := chargeRun{steps: thread.ExecutionSteps(), work: thread.Work(), bytes: thread.AllocatedBytes()}
	if err != nil {
		r.err = err.Error()
	}
	if v, ok := globals["result"]; ok {
		r.result = v.String()
	}
	return r
}

func TestTraceHooksChargeNothing(t *testing.T) {
	var total chargeCount
	for name := range chargeSamples {
		t.Run(name, func(t *testing.T) {
			var seen chargeCount
			base := runCharged(name, nil, 0, 0)
			if failing := chargeFailing[name]; failing != (base.err != "") {
				t.Fatalf("the sample should fail: %v, ended on %s", failing, base)
			}
			traced := runCharged(name, readingTracer(&seen), 0, 0)
			if base != traced {
				t.Errorf("a Tracer changed the counters:\n  without: %s\n  with:    %s", base, traced)
			}
			if seen.events() == 0 {
				t.Errorf("the hooks were never called")
			}
			total.branch += seen.branch
			total.compare += seen.compare
			total.enter += seen.enter
			total.exit += seen.exit
			total.read += seen.read

			// The same with limits that are set and not reached: the gates
			// of the interpreter are armed, the counters must not move.
			const far = 1 << 60
			if b, tr := runCharged(name, nil, far, far), runCharged(name, readingTracer(new(chargeCount)), far, far); b != tr {
				t.Errorf("a Tracer changed the counters under far limits:\n  without: %s\n  with:    %s", b, tr)
			} else if b != base {
				t.Errorf("limits that are not reached changed the counters:\n  none:    %s\n  limited: %s", base, b)
			}

			// And the limits refuse in the same place: a cut at half of the
			// work (or of the bytes) of the whole run ends the program on
			// the same operation, with the same error, with and without.
			for _, cut := range []struct {
				kind string
				n    uint64
			}{{"work", base.work / 2}, {"bytes", base.bytes / 2}} {
				if cut.n == 0 {
					continue
				}
				var withWork, withBytes uint64
				if cut.kind == "work" {
					withWork = cut.n
				} else {
					withBytes = cut.n
				}
				b := runCharged(name, nil, withWork, withBytes)
				tr := runCharged(name, readingTracer(new(chargeCount)), withWork, withBytes)
				if b != tr {
					t.Errorf("a Tracer moved the refusal of the %s limit %d:\n  without: %s\n  with:    %s", cut.kind, cut.n, b, tr)
				}
				if b.err == "" {
					t.Errorf("a cut of the %s at %d (half of the run) did not refuse: %s", cut.kind, cut.n, b)
				}
			}
		})
	}
	// Every hook was used by the corpus, and the reading hooks read.
	if total.branch == 0 || total.compare == 0 || total.enter == 0 || total.exit == 0 || total.read == 0 {
		t.Errorf("the corpus did not exercise every hook: %+v", total)
	}
}
