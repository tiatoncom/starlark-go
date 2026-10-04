package starlark_test

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

var updateStepsGolden = flag.Bool("update-steps-golden", false, "rewrite testdata/steps_v020.golden (only ever from the v0.2.0 interpreter)")

// stepSamples are programs with every kind of conditional and call. Each one
// leaves its answer in the global "result".
var stepSamples = map[string]string{
	"branchy": `
def classify(n, *rest, scale=2, **opts):
    if n % 15 == 0:
        return "fizzbuzz"
    elif n % 5 == 0 and scale > 1:
        return "buzz"
    elif n % 3 == 0 or (n > 40 and not n in [41, 43]):
        return "fizz"
    else:
        if n not in [1, 2, 4]:
            return "plain" if n & 1 else "even"
    return "small"

def run(limit):
    i = 0
    seen = {}
    while i < limit:
        k = classify(i, 1, 2, scale=i % 4, tag="x")
        seen[k] = seen.get(k, 0) + 1
        i += 1
    evens = [x for x in range(limit) if x % 2 == 0 if x != 4]
    srt = sorted(evens, key=lambda v: -v if v and v % 3 else v)
    return sorted(seen.items()), len(evens), srt[:3]

result = str(run(120))
`,
	"chains": `
def f(a, b, c, d):
    if a and b and c:
        return 1
    if a or b or c:
        return 2
    if a and b or c:
        return 3
    if a or b and c:
        return 4
    if a and (b or c):
        return 5
    if not (a and b):
        return 6
    if (a and b) or (c and d):
        return 7
    return 0

def run():
    out = []
    for a in [0, 1, "", "x"]:
        for b in [None, 2]:
            for c in [[], [1]]:
                out.append(f(a, b, c, a == b))
                x = a and b
                y = a or b and c
                out.append((x, y))
    return out

result = str(run())
`,
	"failing": `
def inner(n):
    if n > 3 and n < 10:
        fail("boom", n)
    return n

def outer(n):
    s = 0
    for i in range(n):
        s += inner(i)
    return s

result = str(outer(20))
`,
	"toplevel_loop": `
total = 0
for i in range(50):
    if i % 2 == 0 and i % 3 == 0:
        total += i
    elif i > 40 or i < 3:
        total -= 1
result = str(total)
`,
}

// stepMode configures a thread before a sample runs.
type stepMode struct {
	name string
	set  func(*starlark.Thread)
}

// stepModes are the observation modes whose Steps must be identical.
var stepModes = []stepMode{{"nil", func(*starlark.Thread) {}}}

type stepRun struct {
	steps  uint64
	result string
	err    string
}

func runStepSample(t *testing.T, mode stepMode, name string, max uint64) stepRun {
	t.Helper()
	thread := &starlark.Thread{Name: name}
	mode.set(thread)
	if max > 0 {
		thread.SetMaxExecutionSteps(max)
	}
	opts := &syntax.FileOptions{While: true, Set: true, TopLevelControl: true, GlobalReassign: true}
	globals, err := starlark.ExecFileOptions(opts, thread, name+".star", stepSamples[name], nil)
	r := stepRun{steps: thread.ExecutionSteps()}
	if err != nil {
		r.err = err.Error()
	}
	if v, ok := globals["result"]; ok {
		r.result = v.String()
	}
	return r
}

func (r stepRun) String() string {
	return fmt.Sprintf("steps=%d result=%q err=%q", r.steps, r.result, r.err)
}

func sampleNames() []string {
	names := make([]string, 0, len(stepSamples))
	for n := range stepSamples {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// renderSteps runs every sample to the end and again cut at half of its steps.
func renderSteps(t *testing.T, mode stepMode) string {
	var b strings.Builder
	for _, name := range sampleNames() {
		full := runStepSample(t, mode, name, 0)
		fmt.Fprintf(&b, "%s full %s\n", name, full)
		cut := full.steps / 2
		fmt.Fprintf(&b, "%s cut@%d %s\n", name, cut, runStepSample(t, mode, name, cut))
	}
	return b.String()
}

// TestStepsGolden pins Thread.Steps, results and errors of the sample
// programs to what the v0.2.0 interpreter produced, in every observation mode.
func TestStepsGolden(t *testing.T) {
	const golden = "testdata/steps_v020.golden"
	if *updateStepsGolden {
		if err := os.WriteFile(golden, []byte(renderSteps(t, stepModes[0])), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range stepModes {
		t.Run(mode.name, func(t *testing.T) {
			if got := renderSteps(t, mode); got != string(want) {
				t.Errorf("mode %s differs from the v0.2.0 golden:\n got:\n%s\nwant:\n%s", mode.name, got, want)
			}
		})
	}
}
