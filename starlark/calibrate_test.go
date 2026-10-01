package starlark_test

// The calibration of the prices (prices.go): each probe is a program that does
// one operation on a large operand, in the form that is the worst for it, and
// the harness measures the nanoseconds it takes (the least of several runs, to
// leave out what the machine does meanwhile) and the work it was charged. The
// ratio is what a unit of work costs for that operation. It is run by hand, on
// a quiet machine, to write the table of steps-prices.md:
//
//	STARLARK_CALIBRATE=1 go test -run TestCalibrate -v ./starlark/
//
// A test cannot depend on time, so the prices are tested against counts (the
// work charged is at least a function of the size of the operand), and these
// probes are what the constants of those counts were taken from.

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type probe struct {
	name  string
	setup string
	op    string
}

type probeResult struct {
	name       string
	ns         float64 // least wall time of the operation
	work       uint64  // work charged for it
	alloc      uint64  // bytes charged to the allocation budget
	err        error
	nsPerUnit  float64
	garbageMem uint64 // bytes the process allocated
}

// runProbe runs the program (setup, then the operation between two marks) reps
// times and returns the least time.
func runProbe(p probe, reps int) probeResult {
	res := probeResult{name: p.name, ns: -1}
	for i := 0; i < reps; i++ {
		th := &starlark.Thread{Name: "calibrate"}
		th.Print = func(*starlark.Thread, string) {}
		var t0 time.Time
		var ns float64
		var w0, w1, a0, a1, m0, m1 uint64
		predeclared := starlark.StringDict{
			"json": json.Module,
			"t0": starlark.NewBuiltin("t0", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				m0 = ms.TotalAlloc
				w0, a0 = th.Work(), th.AllocatedBytes()
				t0 = time.Now()
				return starlark.None, nil
			}),
			"t1": starlark.NewBuiltin("t1", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				ns = float64(time.Since(t0).Nanoseconds())
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				m1 = ms.TotalAlloc
				w1, a1 = th.Work(), th.AllocatedBytes()
				return starlark.None, nil
			}),
		}
		runtime.GC()
		_, err := starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "p.star", p.setup+"\nt0()\n"+p.op+"\nt1()\n", predeclared)
		if err != nil {
			res.err = err
			return res
		}
		if res.ns < 0 || ns < res.ns {
			res.ns, res.work, res.alloc, res.garbageMem = ns, w1-w0, a1-a0, m1-m0
		}
	}
	res.nsPerUnit = res.ns / float64(max(res.work, 1))
	return res
}

func TestCalibrate(t *testing.T) {
	if os.Getenv("STARLARK_CALIBRATE") == "" {
		t.Skip("set STARLARK_CALIBRATE=1 to run the calibration (on a quiet machine)")
	}
	reps := 7
	if v := os.Getenv("STARLARK_CALIBRATE_REPS"); v != "" {
		fmt.Sscan(v, &reps)
	}
	started := os.Getenv("STARLARK_CALIBRATE_FROM") == ""
	var results []probeResult
	for _, p := range calibrationProbes {
		if !started && strings.Contains(p.name, os.Getenv("STARLARK_CALIBRATE_FROM")) {
			started = true
		}
		if !started {
			continue
		}
		if f := os.Getenv("STARLARK_CALIBRATE_ONLY"); f != "" && !strings.Contains(p.name, f) {
			continue
		}
		r := runProbe(p, reps)
		results = append(results, r)
		if r.err != nil {
			fmt.Printf("CAL %-46s ERROR %v\n", r.name, r.err)
			continue
		}
		fmt.Printf("CAL %-46s %12.0f ns %12d work %10.2f ns/unit %12d bytes\n", r.name, r.ns, r.work, r.nsPerUnit, r.alloc)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].nsPerUnit > results[j].nsPerUnit })
	fmt.Println("CAL --- worst first")
	for i, r := range results {
		if i >= 25 {
			break
		}
		fmt.Printf("CAL %-46s %10.2f ns/unit\n", r.name, r.nsPerUnit)
	}
}
