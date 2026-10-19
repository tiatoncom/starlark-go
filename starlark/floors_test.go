package starlark_test

// Each price is tested against what it was measured to cost: for every platform
// that is registered (calibrate_floors*_test.go, written by the calibration on a
// quiet machine of that platform), and for every probe of the calibration, the
// work that the operation is charged is at least the nanoseconds that the probe
// took on that platform, divided by the C of that platform. The units of work
// are deterministic and the same everywhere; C is what turns them into time on a
// given machine, so each platform is judged with its own floors and its own C,
// never with a mixture of them. The test itself counts, and cannot be made red
// by the machine it runs on. A price that is dropped, halved or mixed up with
// another is a probe below its floor.
//
// A probe that has no floor on a platform (it was added, or failed, after the
// calibration of that platform) is not judged on that platform: it is skipped,
// not failed. Only a new calibration of the platform gives it a floor.

import (
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// minProbeNanoseconds is the time under which a probe measures the harness (two
// marks, a call of a built-in, the clock) and not the operation: it is not held
// to a price.
const minProbeNanoseconds = 2000.0

func probeWork(t *testing.T, p probe) (work, steps uint64, err error) {
	t.Helper()
	th := &starlark.Thread{Name: "floor"}
	th.Print = func(*starlark.Thread, string) {}
	predeclared := starlark.StringDict{
		"json": json.Module,
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
	_, err = starlark.ExecFileOptions(calibrationFileOptions, th, "p.star", p.setup+"\nt0()\n"+p.op+"\nt1()\n", predeclared)
	return w1 - w0, s1 - s0, err
}

type probeCount struct {
	work, steps uint64
	err         error
}

var probeCounts = map[string]probeCount{}

// countedProbe is probeWork, once per probe for the whole run of the tests: the
// work is deterministic, and every platform is judged by it.
func countedProbe(t *testing.T, p probe) probeCount {
	t.Helper()
	if c, ok := probeCounts[p.name]; ok {
		return c
	}
	var c probeCount
	c.work, c.steps, c.err = probeWork(t, p)
	probeCounts[p.name] = c
	return c
}

func TestPrices_AtLeastTheMeasuredCost(t *testing.T) {
	if len(calibrationPlatforms) == 0 {
		t.Skip("no calibration")
	}
	for _, plat := range calibrationPlatforms {
		t.Run(plat.Platform, func(t *testing.T) {
			checked := 0
			for _, p := range calibrationProbes {
				// no floor on this platform: not judged here
				ns, ok := plat.Ns[p.name]
				if !ok || strings.HasPrefix(p.name, "loop:") || ns < minProbeNanoseconds {
					continue
				}
				c := countedProbe(t, p)
				if c.err != nil {
					t.Errorf("%s: %v", p.name, c.err)
					continue
				}
				checked++
				floor := ns / plat.C
				if float64(c.work) < floor {
					t.Errorf("%s: %d units of work (%d of them steps) for %.0f ns: %.1f ns a unit, over C = %.0f of %s", p.name, c.work, c.steps, ns, ns/float64(max(c.work, 1)), plat.C, plat.Platform)
				}
			}
			if checked < 150 {
				t.Errorf("only %d probes were checked", checked)
			}
		})
	}
}

// The invariant: no probe, priced or not, loops included, has cost more than C
// nanoseconds for each unit of work that it was charged, C being that of the
// platform it was measured on (calibrationPlatform.C; that of darwin/arm64 is
// workNanoseconds, the reference of the invariant of work.go). Each platform is
// judged by its own floors and its own C. A probe without a floor on a platform
// is not judged on it.
func TestWork_NoProbeCostsMoreThanC(t *testing.T) {
	if len(calibrationPlatforms) == 0 {
		t.Skip("no calibration")
	}
	for _, plat := range calibrationPlatforms {
		t.Run(plat.Platform, func(t *testing.T) {
			worst, worstName := 0.0, ""
			for _, p := range calibrationProbes {
				ns, ok := plat.Ns[p.name]
				if !ok || ns < minProbeNanoseconds {
					continue
				}
				c := countedProbe(t, p)
				if c.err != nil {
					t.Errorf("%s: %v", p.name, c.err)
					continue
				}
				r := ns / float64(max(c.work, 1))
				if r > worst {
					worst, worstName = r, p.name
				}
				if r > plat.C {
					t.Errorf("%s: %d units of work for %.0f ns: %.1f ns a unit, over C = %.0f of %s", p.name, c.work, ns, r, plat.C, plat.Platform)
				}
			}
			t.Logf("%s, C = %.0f: the worst probe: %s, %.1f ns a unit", plat.Platform, plat.C, worstName, worst)
		})
	}
}
