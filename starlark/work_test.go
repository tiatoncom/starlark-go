package starlark

// The counter of work (work.go): its API, its limit, what happens when the limit
// is reached, and that the steps are the interpreter's alone.

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"go.starlark.net/syntax"
)

var workOpts = &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}

func TestWork_CountsWithoutALimit(t *testing.T) {
	th := &Thread{}
	if err := th.ChargeWork(100); err != nil {
		t.Fatal(err)
	}
	if err := th.ChargeWork(0); err != nil {
		t.Fatal(err)
	}
	if th.Work() != 100 {
		t.Errorf("Work() = %d, want 100", th.Work())
	}
	// a nil thread is not charged and does not fail
	if err := (*Thread)(nil).ChargeWork(1 << 40); err != nil {
		t.Fatal(err)
	}
	// the steps are units of work: Work is at least Steps
	g, err := ExecFileOptions(workOpts, th, "t.star", "x = 0\nfor i in range(1000):\n    x += i\n", nil)
	if err != nil || g["x"] != MakeInt(499500) {
		t.Fatal(err)
	}
	if th.Work() < th.Steps+100 || th.Steps < 3000 {
		t.Errorf("work %d, steps %d", th.Work(), th.Steps)
	}
}

// A built-in's charge over the limit fails with the typed error, the thread is
// cancelled, and the work is the limit, not more.
func TestWork_LimitReachedByACharge(t *testing.T) {
	th := &Thread{}
	th.SetMaxWork(1000)
	if err := th.ChargeWork(600); err != nil {
		t.Fatal(err)
	}
	err := th.ChargeWork(500)
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	if we.Limit != 1000 || we.Charged != 600 || we.Requested != 500 {
		t.Errorf("%+v", we)
	}
	if th.Work() != 1000 {
		t.Errorf("work after the refusal %d, want the limit 1000", th.Work())
	}
	if !strings.Contains(err.Error(), "work budget exhausted") || strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "600") {
		t.Errorf("text %q: it names the limit only", err.Error())
	}
	// the thread is cancelled: the next charge fails the same way, and Work stays
	if err := th.ChargeWork(1); err == nil {
		t.Errorf("a charge on a thread that is out of work succeeded")
	}
	if th.Work() > 1000 {
		t.Errorf("work %d is over the limit", th.Work())
	}
	// Charging exactly up to the limit succeeds.
	th2 := &Thread{}
	th2.SetMaxWork(1000)
	if err := th2.ChargeWork(1000); err != nil {
		t.Errorf("a charge up to the limit: %v", err)
	}
}

// The steps alone reach the limit, since each is a unit of work, at a work that
// is the limit; the error is the same type.
func TestWork_LimitReachedBySteps(t *testing.T) {
	th := &Thread{}
	th.SetMaxWork(5000)
	_, err := ExecFileOptions(workOpts, th, "t.star", "x = 0\nfor i in range(100000):\n    x += i\n", nil)
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	// (the call of range is a built-in: baseCall units beyond its steps)
	if th.Work() != 5000 || th.Steps != 5000-baseCall {
		t.Errorf("work %d, steps %d: the work is the limit, the steps are the work less the %d units of the call of range", th.Work(), th.Steps, baseCall)
	}
	// With work charged by operations too: the limit is hit by the sum.
	th = &Thread{}
	th.SetMaxWork(100000)
	_, err = ExecFileOptions(workOpts, th, "t.star", "l = list(range(10000))\nfor i in range(100000):\n    r = l * 3\n", nil)
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	if th.Work() != 100000 {
		t.Errorf("work %d, want the limit", th.Work())
	}
	if th.Steps >= 100000 {
		t.Errorf("steps %d: the operations' work is not in them", th.Steps)
	}
}

// The step limit works as it always did: the same steps at the cutoff, the same
// error, OnMaxSteps called; and it wins over a work limit that is not reached.
func TestWork_StepLimitIsAsBefore(t *testing.T) {
	th := &Thread{}
	th.SetMaxExecutionSteps(2000)
	_, err := ExecFileOptions(workOpts, th, "t.star", "x = 0\nfor i in range(100000):\n    x += i\n", nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("err = %v", err)
	}
	if th.Steps != 2000 {
		t.Errorf("steps %d, want exactly the limit", th.Steps)
	}
	var we *WorkBudgetError
	if errors.As(err, &we) {
		t.Errorf("a step limit is not a work error")
	}
	// both limits: the steps' first
	th = &Thread{}
	th.SetMaxExecutionSteps(2000)
	th.SetMaxWork(1 << 30)
	_, err = ExecFileOptions(workOpts, th, "t.star", "x = 0\nfor i in range(100000):\n    x += i\n", nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") || th.Steps != 2000 {
		t.Fatalf("err = %v, steps %d", err, th.Steps)
	}
	// OnMaxSteps is called, and a host that raises the limit there runs on.
	th = &Thread{}
	calls := 0
	th.OnMaxSteps = func(th *Thread) {
		calls++
		th.SetMaxExecutionSteps(th.Steps + 1000)
	}
	th.SetMaxExecutionSteps(1000)
	if _, err := ExecFileOptions(workOpts, th, "t.star", "x = 0\nfor i in range(5000):\n    x += i\n", nil); err != nil {
		t.Fatal(err)
	}
	if calls < 5 {
		t.Errorf("OnMaxSteps called %d times", calls)
	}
}

// The steps of a program are the same whatever the work limit, and the same as
// with none.
func TestWork_TheLimitDoesNotChangeTheSteps(t *testing.T) {
	src := "l = [i % 7 for i in range(3000)]\ns = sorted(l)\nd = {}\nfor x in l:\n    d[x] = d.get(x, 0) + 1\nr = ','.join([str(k) for k in d])\n"
	run := func(max uint64) (uint64, uint64) {
		th := &Thread{}
		th.SetMaxWork(max)
		if _, err := ExecFileOptions(workOpts, th, "t.star", src, nil); err != nil {
			t.Fatal(err)
		}
		return th.Steps, th.Work()
	}
	s0, w0 := run(0)
	s1, w1 := run(1 << 40)
	if s0 != s1 || w0 != w1 {
		t.Errorf("steps %d/%d, work %d/%d with and without a limit", s0, s1, w0, w1)
	}
	if w0 <= s0 {
		t.Errorf("work %d is not more than steps %d for a program that sorts", w0, s0)
	}
}

// A built-in of the host charges and is stopped as a built-in of the package.
func TestWork_HostBuiltin(t *testing.T) {
	th := &Thread{}
	th.SetMaxWork(10000)
	var chunks int
	host := NewBuiltin("host", func(th *Thread, _ *Builtin, args Tuple, _ []Tuple) (Value, error) {
		for i := 0; i < 1000; i++ {
			if err := th.ChargeWork(100); err != nil {
				return nil, err
			}
			chunks++
		}
		return None, nil
	})
	_, err := ExecFileOptions(workOpts, th, "t.star", "host()\n", StringDict{"host": host})
	var we *WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	if chunks > 100 || th.Work() != 10000 {
		t.Errorf("%d chunks of 100 ran, work %d: stopped at the limit", chunks, th.Work())
	}
	// A host that swallows the error is stopped at the next opcode.
	th = &Thread{}
	th.SetMaxWork(10000)
	swallow := NewBuiltin("swallow", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		th.ChargeWork(1 << 20)
		return None, nil
	})
	_, err = ExecFileOptions(workOpts, th, "t.star", "swallow()\nx = 1\nx = 2\n", StringDict{"swallow": swallow})
	if !errors.As(err, &we) {
		t.Fatalf("a swallowed refusal: err = %v", err)
	}
}

// A cancellation by the host (Cancel, from another goroutine) is seen at the
// next charge, so a long operation of the package stops, and not at its end.
func TestWork_ACancelStopsALongOperation(t *testing.T) {
	th := &Thread{}
	var n atomic.Int64
	host := NewBuiltin("host", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		for i := 0; i < 1000; i++ {
			if i == 10 {
				th.Cancel("host says stop")
			}
			if err := th.ChargeWork(1); err != nil {
				return nil, err
			}
			n.Add(1)
		}
		return None, nil
	})
	_, err := ExecFileOptions(workOpts, th, "t.star", "host()\n", StringDict{"host": host})
	if err == nil || !strings.Contains(err.Error(), "host says stop") {
		t.Fatalf("err = %v", err)
	}
	if n.Load() > 10 {
		t.Errorf("%d charges went through after the cancel", n.Load())
	}
	// the work error is not the cancel's
	var we *WorkBudgetError
	if errors.As(err, &we) {
		t.Errorf("a cancel by the host is not a work error")
	}
}

// SetMaxWork after some work: the limit is on the total.
func TestWork_LimitIsOnTheTotal(t *testing.T) {
	th := &Thread{}
	th.ChargeWork(800)
	th.SetMaxWork(1000)
	if err := th.ChargeWork(100); err != nil {
		t.Fatal(err)
	}
	if err := th.ChargeWork(101); err == nil {
		t.Error("a charge over the limit succeeded")
	}
	// a limit that is already passed: the next charge fails and the work is not lowered
	th = &Thread{}
	th.ChargeWork(5000)
	th.SetMaxWork(1000)
	if err := th.ChargeWork(1); err == nil {
		t.Error("a charge past a limit that was already passed succeeded")
	}
	if th.Work() < 1000 {
		t.Errorf("work %d", th.Work())
	}
}

// The meter charges as it goes, a chunk of 1024 units at a time, and at the end,
// and a nil meter does nothing.
func TestWork_Meter(t *testing.T) {
	th := &Thread{}
	m := th.meter()
	for i := 0; i < 1023; i++ {
		m.add(1)
	}
	if th.Work() != 0 {
		t.Errorf("work %d before a chunk", th.Work())
	}
	m.add(1)
	if th.Work() != 1024 {
		t.Errorf("work %d after a chunk of 1024, want 1024", th.Work())
	}
	m.add(7)
	if err := m.flush(); err != nil || th.Work() != 1024+7 {
		t.Errorf("work %d after the flush (%v)", th.Work(), err)
	}
	// a jump past a chunk is charged at once
	m = th.meter()
	th.extraWork = 0
	m.add(5000)
	if th.Work() != 5000 {
		t.Errorf("work %d after 5000 units at once", th.Work())
	}
	var nilm *meter
	if err := nilm.add(1 << 40); err != nil {
		t.Fatal(err)
	}
	nm := (*Thread)(nil).meter()
	if err := nm.add(1 << 40); err != nil {
		t.Fatal(err)
	}
	// a meter stops a loop near the limit
	th = &Thread{}
	th.SetMaxWork(10000)
	m = th.meter()
	var added uint64
	var err error
	for err == nil && added < 1_000_000 {
		err = m.add(1)
		added++
	}
	if err == nil {
		t.Fatal("a meter did not stop")
	}
	if added > 10000+1024 || added < 10000-1024 {
		t.Errorf("stopped after %d units, limit 10000", added)
	}
}
