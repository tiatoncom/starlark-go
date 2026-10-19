package starlark_test

// The calibration of the prices (prices.go): each probe is a program that does
// one operation on a large operand, in the form that is the worst for it, and
// the harness measures the nanoseconds it takes (the least of several runs, to
// leave out what the machine does meanwhile) and the work it was charged. The
// ratio is what a unit of work costs for that operation. It is run by hand, on
// a quiet machine, and its output is turned by TestWriteCalibrationFloors into
// the floors file of that platform (the table the floors tests and the table of
// prices, doc/prices.md, judge by).
//
// The procedure of a calibration of a host (v2): SIX separate processes of
// seven repeats each (STARLARK_CALIBRATE_REPS=7), from the root of the
// repository; the floors are the least time of each probe over all six. Each
// process is preceded by a warm-up of 10 s of CPU (a busy loop, so that the
// clock of the CPU is up), runs with transparent huge pages off
// (GODEBUG=disablethp=1) and, on Linux, on three fixed cores (taskset -c 1-3);
// the load (uptime) and the clocks of the cores (grep MHz /proc/cpuinfo) are
// written into the run file before and after it (they are not CAL lines: the
// awk script ignores them, the writer copies them into the header of the file).
// On Linux (taskset and /proc/cpuinfo are Linux's):
//
//	for i in 1 2 3 4 5 6; do
//	  for j in 1 2 3; do taskset -c 1-3 timeout 10 sh -c 'while :; do :; done' & done; wait
//	  { uptime; grep MHz /proc/cpuinfo
//	    GODEBUG=disablethp=1 STARLARK_CALIBRATE=1 STARLARK_CALIBRATE_REPS=7 LC_ALL=C taskset -c 1-3 go test ./starlark -run '^TestCalibrate$' -count=1 -timeout 0 -v | grep '^CAL'
//	    uptime; grep MHz /proc/cpuinfo; } > cal-$i.txt
//	done
//
// Or, from a test binary that was built elsewhere (GOOS=linux GOARCH=amd64 go
// test -c -o starlark.test ./starlark), run from the directory that holds
// starlark.test and its testdata/ (the same loop, with the line of go test
// replaced by):
//
//	GODEBUG=disablethp=1 STARLARK_CALIBRATE=1 STARLARK_CALIBRATE_REPS=7 LC_ALL=C taskset -c 1-3 ./starlark.test -test.run '^TestCalibrate$' -test.count=1 -test.timeout 0 -test.v | grep '^CAL'
//
// Then the noise of the six is judged, and the writer reads them (on the
// machine that has the repository):
//
//	LC_ALL=C awk -f internal/calibrate/cal-check.awk cal-1.txt cal-2.txt cal-3.txt cal-4.txt cal-5.txt cal-6.txt   # ACCEPT/REJECT
//	STARLARK_CALIBRATE_INPUT=cal-1.txt,cal-2.txt,cal-3.txt,cal-4.txt,cal-5.txt,cal-6.txt STARLARK_CALIBRATE_WRITE=starlark/calibrate_floors_<name>_test.go go test ./starlark -run '^TestWriteCalibrationFloors$' -count=1
//
// internal/calibrate/cal-check.awk says what the criterion is (the worst ns/unit
// of the runs within 5%; the probes that decide, and the rest, by the least time
// of the first half of the runs against the second); doc/budgets.md says why.
//
// The writer keeps the least time of each probe over ALL the runs; the file is
// sorted and gofmt-formatted, the same inputs give the same bytes (the date is
// that of the machine line of the runs, STARLARK_CALIBRATE_DATE overrides it;
// STARLARK_CALIBRATE_CPU names the CPU when it is not known cheaply). The file
// holds the C of the platform: C = ceil of the dearest ns/unit of any run over
// the probes of minProbeNanoseconds or more, each run by its own times (not
// by the least times: a promise of time must hold in a bad process too).
// Each platform has its own file (calibrate_floors*_test.go, the name must not
// end in _<goos>_<goarch>_test.go, which is a build constraint) that registers
// its table, and its C, with registerCalibration. There is NO merge over the
// platforms: the tests judge each platform by its own floors with its own C
// (floors_test.go). A new file of an already registered platform must replace
// the old one, not be added.
//
// A test cannot depend on time, so the prices are tested against counts (the
// work charged is at least a function of the size of the operand), and these
// probes are what the constants of those counts were taken from.

import (
	"bytes"
	"fmt"
	"go/format"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// calibrationFileOptions is what the probes are compiled with, by the harness
// that measures them (runProbe) and by the one that counts their work
// (probeWork, floors_test.go). Recursion is allowed: a probe may build its value
// with a recursive def ("nested == (wide, 5 deep)"), and the probe must run as
// it is written.
var calibrationFileOptions = &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true, Recursion: true}

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
		_, err := starlark.ExecFileOptions(calibrationFileOptions, th, "p.star", p.setup+"\nt0()\n"+p.op+"\nt1()\n", predeclared)
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
	fmt.Println(machineLine())
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

// machineLine is the line that every run prints first, for the writer:
// CAL machine date=<day> platform=<goos>/<goarch> go=<version> cpu=<model>.
func machineLine() string {
	date := os.Getenv("STARLARK_CALIBRATE_DATE")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	return fmt.Sprintf("CAL machine date=%s platform=%s/%s go=%s cpu=%s", date, runtime.GOOS, runtime.GOARCH, runtime.Version(), cpuModel())
}

func cpuModel() string {
	if v := os.Getenv("STARLARK_CALIBRATE_CPU"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	case "linux":
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	return "unknown"
}

// calibrationPlatform is the floors of one measured platform.
type calibrationPlatform struct {
	Platform string  // "<goos>/<goarch>"
	C        float64 // nanoseconds a unit may cost on this platform: its own, never mixed with another's
	Machine  string  // "<goos>/<goarch>, <go version>[, <cpu>]"
	Date     string
	Uptime   []string // the load of the machine at the start and the end of each run
	Ns       map[string]float64
}

// calibrationPlatforms are the registered platforms; the files of the platforms
// register themselves while the variables of the package are initialised. The
// tests judge each by its own Ns and C: the platforms are not merged.
var calibrationPlatforms []calibrationPlatform

func registerCalibration(p calibrationPlatform) bool {
	calibrationPlatforms = append(calibrationPlatforms, p)
	return true
}

// referencePlatform is the platform of the tables of doc/prices.md and of
// workNanoseconds (the C of the invariant of work.go).
const referencePlatform = "darwin/arm64"

var (
	machineRe = regexp.MustCompile(`^CAL machine date=(\S+) platform=(\S+) go=(\S+) cpu=(.*)$`)
	calRe     = regexp.MustCompile(`^CAL (.+?)\s+(\d+(?:\.\d+)?) ns\s+(\d+) work\s+\S+ ns/unit\s+\d+ bytes$`)
	mhzRe     = regexp.MustCompile(`^cpu MHz\s*:\s*(\d+(?:\.\d+)?)$`)
)

// mhzRange is "min-max" of the clocks of the cores, in whole MHz.
func mhzRange(v []float64) string {
	if len(v) == 0 {
		return "none"
	}
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = min(lo, x), max(hi, x)
	}
	return fmt.Sprintf("%.0f-%.0f", lo, hi)
}

// writeCalibrationFloors builds the floors file from the captured outputs of the
// runs (one string per run): the least time of each probe over all of them.
// ERROR lines and the "worst first" section are ignored; a probe that is not
// in every run (or that failed in one) is an error. Deterministic.
func writeCalibrationFloors(runs []string) ([]byte, error) {
	if len(runs) == 0 {
		return nil, fmt.Errorf("no runs")
	}
	var machine, date, platform, goVersion, cpu string
	var uptime, mhz []string
	least := map[string]float64{}
	workOf := map[string]uint64{}
	seen := map[string]int{} // probe -> number of runs it is in
	// the dearest ns a unit of each run (probes of minProbeNanoseconds or more,
	// by the time of that run), which C is taken from
	runDear := make([]float64, len(runs))
	runDearName := make([]string, len(runs))
	for i, run := range runs {
		var m string
		var before, after []float64 // the clocks of the cores, before the machine line and after it
		inRun := map[string]bool{}
		for _, line := range strings.Split(run, "\n") {
			line = strings.TrimRight(line, "\r ")
			switch {
			case line == "":
			case mhzRe.MatchString(line):
				x, _ := strconv.ParseFloat(mhzRe.FindStringSubmatch(line)[1], 64)
				if m == "" {
					before = append(before, x)
				} else {
					after = append(after, x)
				}
			case strings.HasPrefix(line, "CAL machine "):
				mm := machineRe.FindStringSubmatch(line)
				if mm == nil {
					return nil, fmt.Errorf("run %d: bad machine line %q", i+1, line)
				}
				if m != "" {
					return nil, fmt.Errorf("run %d: two machine lines", i+1)
				}
				m = fmt.Sprintf("%s %s %s", mm[2], mm[3], mm[4])
				if i == 0 {
					date, platform, goVersion, cpu = mm[1], mm[2], mm[3], mm[4]
				} else if mm[1] != date {
					return nil, fmt.Errorf("run %d: date %s, run 1 had %s (set STARLARK_CALIBRATE_DATE)", i+1, mm[1], date)
				}
			case strings.HasPrefix(line, "CAL "):
				mm := calRe.FindStringSubmatch(line)
				if mm == nil {
					continue // ERROR lines, "--- worst first" and its rows
				}
				ns, err := strconv.ParseFloat(mm[2], 64)
				if err != nil {
					return nil, fmt.Errorf("run %d: %q: %v", i+1, line, err)
				}
				work, _ := strconv.ParseUint(mm[3], 10, 64)
				if old, ok := workOf[mm[1]]; ok && old != work {
					return nil, fmt.Errorf("run %d: %q charged %d units of work, another run %d (the work is deterministic: the runs are of different trees)", i+1, mm[1], work, old)
				}
				workOf[mm[1]] = work
				if old, ok := least[mm[1]]; !ok || ns < old {
					least[mm[1]] = ns
				}
				if ns >= minProbeNanoseconds {
					if r := ns / float64(max(work, 1)); r > runDear[i] {
						runDear[i], runDearName[i] = r, mm[1]
					}
				}
				if !inRun[mm[1]] {
					inRun[mm[1]] = true
					seen[mm[1]]++
				}
			default:
				uptime = append(uptime, strings.Join(strings.Fields(line), " "))
			}
		}
		if m == "" {
			return nil, fmt.Errorf("run %d: no machine line (CAL machine ...)", i+1)
		}
		if len(before)+len(after) > 0 {
			mhz = append(mhz, mhzRange(before)+" / "+mhzRange(after))
		}
		if i == 0 {
			machine = m
		} else if m != machine {
			return nil, fmt.Errorf("run %d: machine %q differs from run 1: %q", i+1, m, machine)
		}
	}
	if len(least) == 0 {
		return nil, fmt.Errorf("no probes")
	}
	names := make([]string, 0, len(least))
	for k := range least {
		names = append(names, k)
	}
	sort.Strings(names)
	var missing []string
	for _, k := range names {
		if seen[k] != len(runs) {
			missing = append(missing, fmt.Sprintf("%q (in %d of %d runs)", k, seen[k], len(runs)))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("probes missing in a run (failed or not run): %s", strings.Join(missing, ", "))
	}
	ident := "calibratedNs_" + regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(platform, "_")
	machineText := fmt.Sprintf("%s, %s, %s", platform, goVersion, cpu)

	// C of the platform: the ceiling of the dearest nanoseconds a unit cost in a
	// probe of minProbeNanoseconds or more in ANY run (each run by its own
	// times), not by the least times: a promise of time on the platform must
	// hold in a bad process too. The dearest by the least times is printed
	// beside it.
	dearRun := 0
	for i := range runDear {
		if runDear[i] > runDear[dearRun] {
			dearRun = i
		}
	}
	if runDear[dearRun] == 0 {
		return nil, fmt.Errorf("no probe of %.0f ns or more: C cannot be taken", minProbeNanoseconds)
	}
	c := math.Ceil(runDear[dearRun])
	leastDear, leastDearName := 0.0, ""
	for _, k := range names {
		if least[k] < minProbeNanoseconds {
			continue
		}
		if r := least[k] / float64(max(workOf[k], 1)); r > leastDear {
			leastDear, leastDearName = r, k
		}
	}

	var b bytes.Buffer
	b.WriteString("package starlark_test\n\n")
	fmt.Fprintf(&b, "// Code generated by TestWriteCalibrationFloors (calibrate_test.go) on %s; DO NOT EDIT.\n", date)
	fmt.Fprintf(&b, "// Machine: %s. %d runs; the least time of each probe over all of them is kept.\n", machineText, len(runs))
	fmt.Fprintf(&b, "// C = %.0f ns a unit: the ceiling of the dearest probe of any run (run %d: %s, %.2f ns a unit);\n", c, dearRun+1, runDearName[dearRun], runDear[dearRun])
	fmt.Fprintf(&b, "// by the least times the dearest is %s, %.2f ns a unit.\n", leastDearName, leastDear)
	b.WriteString("// The load of the machine at the start and the end of each run:\n")
	for _, u := range uptime {
		fmt.Fprintf(&b, "//   %s\n", u)
	}
	if len(mhz) > 0 {
		b.WriteString("// The clocks of the cores, MHz (least-greatest), at the start / the end of each run:\n")
		for _, x := range mhz {
			fmt.Fprintf(&b, "//   %s\n", x)
		}
	}
	b.WriteString("//\n// The least time, in nanoseconds, of each probe of the table of prices.\n\n")
	fmt.Fprintf(&b, "var %s = map[string]float64{\n", ident)
	for _, k := range names {
		fmt.Fprintf(&b, "\t%s: %s,\n", strconv.Quote(k), strconv.FormatFloat(least[k], 'f', -1, 64))
	}
	b.WriteString("}\n\nvar _ = registerCalibration(calibrationPlatform{\n")
	fmt.Fprintf(&b, "\tPlatform: %s,\n\tC: %s,\n\tMachine: %s,\n\tDate: %s,\n\tUptime: []string{\n", strconv.Quote(platform), strconv.FormatFloat(c, 'f', -1, 64), strconv.Quote(machineText), strconv.Quote(date))
	for _, u := range uptime {
		fmt.Fprintf(&b, "\t\t%s,\n", strconv.Quote(u))
	}
	fmt.Fprintf(&b, "\t},\n\tNs: %s,\n})\n", ident)
	return format.Source(b.Bytes())
}

// TestWriteCalibrationFloors writes the floors file of a platform from the
// captured runs (see the top of this file).
func TestWriteCalibrationFloors(t *testing.T) {
	out := os.Getenv("STARLARK_CALIBRATE_WRITE")
	in := os.Getenv("STARLARK_CALIBRATE_INPUT")
	if out == "" || in == "" {
		t.Skip("set STARLARK_CALIBRATE_INPUT=run1.txt,run2.txt,... and STARLARK_CALIBRATE_WRITE=<file> to write the floors")
	}
	var runs []string
	for _, f := range strings.Split(in, ",") {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, string(data))
	}
	src, err := writeCalibrationFloors(runs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(src))
}

func TestWriteCalibrationFloors_Synthetic(t *testing.T) {
	const machine = "CAL machine date=2030-01-02 platform=linux/amd64 go=go1.26 cpu=Test CPU 9000\n"
	run := func(a, b string) string {
		return "10:00  up 1 day,  1:00, 1 user, load averages: 0.10 0.20 0.30\n" +
			"cpu MHz\t\t: 800.000\ncpu MHz\t\t: 3400.123\n" + machine +
			"CAL % d                                                  " + a + " ns        100 work       1.00 ns/unit         0 bytes\n" +
			"CAL 'x' y  z                                         " + b + " ns        100 work       1.00 ns/unit         0 bytes\n" +
			"CAL tiny                                              900 ns        100 work       9.00 ns/unit         0 bytes\n" +
			"CAL broken                                     ERROR boom\n" +
			"CAL --- worst first\n" +
			"CAL % d                                          1.00 ns/unit\n" +
			"10:05  up 1 day,  1:05, 1 user, load averages: 0.10 0.20 0.30\n" +
			"cpu MHz\t\t: 3300.000\ncpu MHz\t\t: 3400.000\n"
	}
	// the least of "% d" is 4000 (40 ns a unit), of "'x' y  z" 8010 (80.1 ns a
	// unit); the dearest of a run is "'x' y  z" in run 2, 10000 (100 ns a unit),
	// so C = 100, not 81; "tiny" is under minProbeNanoseconds and does not count
	runs := []string{run("5000", "9000"), run("4000", "10000"), run("4500", "8010")}
	a, err := writeCalibrationFloors(runs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeCalibrationFloors(runs)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("not deterministic: %v", err)
	}
	s := strings.Join(strings.Fields(string(a)), " ") // gofmt aligns the values
	for _, want := range []string{`"% d": 4000,`, `"'x' y z": 8010,`, "var calibratedNs_linux_amd64 = ", "on 2030-01-02", "linux/amd64, go1.26, Test CPU 9000",
		"10:00 up 1 day, 1:00, 1 user", `Platform: "linux/amd64", C: 100,`, "C = 100 ns a unit", "(run 2: 'x' y z, 100.00 ns a unit)", "the dearest is 'x' y z, 80.10 ns a unit", "800-3400 / 3300-3400"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "broken") || strings.Contains(s, "worst") {
		t.Errorf("ERROR or worst-first leaked:\n%s", s)
	}
	if got, err := format.Source(a); err != nil || !bytes.Equal(got, a) {
		t.Errorf("not gofmt-formatted: %v", err)
	}
	// a probe missing in one run is an error
	var kept []string
	for _, l := range strings.Split(runs[1], "\n") {
		if !strings.Contains(l, "'x' y") {
			kept = append(kept, l)
		}
	}
	short := strings.Join(kept, "\n")
	if _, err := writeCalibrationFloors([]string{runs[0], short}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing probe not an error: %v", err)
	}
	// the work of a probe differing between runs is an error
	otherWork := strings.Replace(runs[2], "8010 ns        100 work", "8010 ns        101 work", 1)
	if _, err := writeCalibrationFloors([]string{runs[0], otherWork}); err == nil || !strings.Contains(err.Error(), "units of work") {
		t.Errorf("differing work not an error: %v", err)
	}
	// differing machines are an error
	other := strings.Replace(runs[2], "linux/amd64", "linux/arm64", 1)
	if _, err := writeCalibrationFloors([]string{runs[0], other}); err == nil {
		t.Error("differing machines accepted")
	}
}

// TestCalibration_Registered: each registered platform is whole (its name, its
// C, its floors) and registered once (a new calibration of a platform replaces
// its file); there is no merge of the platforms. The C of the reference
// platform is workNanoseconds.
func TestCalibration_Registered(t *testing.T) {
	if len(calibrationPlatforms) == 0 {
		t.Fatal("no platform registered")
	}
	seen := map[string]bool{}
	for _, p := range calibrationPlatforms {
		if p.Platform == "" || !strings.HasPrefix(p.Machine, p.Platform) {
			t.Errorf("platform %q does not name its machine %q", p.Platform, p.Machine)
		}
		if seen[p.Platform] {
			t.Errorf("platform %s is registered twice: the new file must replace the old one", p.Platform)
		}
		seen[p.Platform] = true
		if p.C <= 0 {
			t.Errorf("%s: C = %v", p.Platform, p.C)
		}
		if len(p.Ns) == 0 {
			t.Errorf("%s: no floors", p.Platform)
		}
		for k, v := range p.Ns {
			if v <= 0 {
				t.Errorf("%s: %s: floor %v", p.Platform, k, v)
			}
		}
		if p.Platform == referencePlatform && p.C != float64(starlark.WorkNanoseconds) {
			t.Errorf("%s: C = %v, not workNanoseconds = %d", p.Platform, p.C, starlark.WorkNanoseconds)
		}
	}
	if !seen[referencePlatform] {
		t.Errorf("the reference platform %s is not registered", referencePlatform)
	}
}
