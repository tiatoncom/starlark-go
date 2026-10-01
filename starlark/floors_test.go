package starlark_test

// Each price is tested against what it was measured to cost: for every probe of
// the calibration, the work that the operation is charged is at least the
// nanoseconds that the probe took, divided by priceNanoseconds. The time is in
// the table calibratedNs (calibrate_floors_test.go, written by the calibration
// on a quiet machine); the test itself counts, and cannot be made red by the
// machine it runs on. A price that is dropped, halved or mixed up with
// another is a probe below its floor.

import (
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// priceNanoseconds is the cost of a unit of work that the prices are held to:
// a priced operation does at most this many nanoseconds of work for each unit
// that it is charged (the nominal unit is 8 ns). The steps of the interpreter,
// which are not priced but counted, cost up to 16 (workNanoseconds: the C of
// the invariant).
const priceNanoseconds = 10.0

func probeWork(t *testing.T, p probe) (work, steps uint64, err error) {
	t.Helper()
	th := &starlark.Thread{Name: "floor"}
	th.Print = func(*starlark.Thread, string) {}
	predeclared := starlark.StringDict{
		"json": json.Module,
		"t0":   starlark.NewBuiltin("t0", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) { return starlark.None, nil }),
		"t1":   starlark.NewBuiltin("t1", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) { return starlark.None, nil }),
	}
	var w0, s0, w1, s1 uint64
	predeclared["t0"] = starlark.NewBuiltin("t0", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		w0, s0 = th.Work(), th.Steps
		return starlark.None, nil
	})
	predeclared["t1"] = starlark.NewBuiltin("t1", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		w1, s1 = th.Work(), th.Steps
		return starlark.None, nil
	})
	_, err = starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "p.star", p.setup+"\nt0()\n"+p.op+"\nt1()\n", predeclared)
	return w1 - w0, s1 - s0, err
}

func TestPrices_AtLeastTheMeasuredCost(t *testing.T) {
	if len(calibratedNs) == 0 {
		t.Skip("no calibration")
	}
	checked := 0
	for _, p := range calibrationProbes {
		ns, ok := calibratedNs[p.name]
		if !ok || strings.HasPrefix(p.name, "loop:") {
			continue
		}
		work, steps, err := probeWork(t, p)
		if err != nil {
			t.Errorf("%s: %v", p.name, err)
			continue
		}
		checked++
		floor := ns / priceNanoseconds
		if float64(work) < floor {
			t.Errorf("%s: %d units of work (%d of them steps) for %.0f ns: %.1f ns a unit, over %.0f", p.name, work, steps, ns, ns/float64(max(work, 1)), priceNanoseconds)
		}
	}
	if checked < 150 {
		t.Errorf("only %d probes were checked", checked)
	}
}
