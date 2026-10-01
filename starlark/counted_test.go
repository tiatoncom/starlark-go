package starlark

// The values and the harness of the tests that bound an attack: a value that
// counts the comparisons, hashes and truth tests done on it, so that the work
// that an operation really did is known, and compared with the work that was
// charged for it.

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"go.starlark.net/syntax"
)

// ---- counted values ----

// A counted is a value that counts the work done on it: comparisons (Equal,
// <), hashes, truth tests. The counters are global: the tests that use them
// run one at a time.
type counted struct{ id int }

var (
	cmpCount   atomic.Uint64
	hashCount  atomic.Uint64
	truthCount atomic.Uint64
)

func resetCounts() { cmpCount.Store(0); hashCount.Store(0); truthCount.Store(0) }

// runawayWork is the number of comparisons or hashes after which a counted
// value refuses to go on: the attacks are bounded by their steps, and if they
// are not (a regression), the test fails with this error rather than not
// returning. The legitimate attacks of the tests do at most ~80 million.
const runawayWork = 250_000_000

var errRunaway = errors.New("runaway: the steps did not stop the work")

func (c counted) String() string { return fmt.Sprintf("counted(%d)", c.id) }
func (c counted) Type() string   { return "counted" }
func (c counted) Freeze()        {}
func (c counted) Truth() Bool    { truthCount.Add(1); return c.id != 0 }
func (c counted) Hash() (uint32, error) {
	if hashCount.Add(1) > runawayWork {
		return 0, errRunaway
	}
	return uint32(c.id) * 2654435761, nil
}
func (c counted) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	if cmpCount.Add(1) > runawayWork {
		return false, errRunaway
	}
	d := y.(counted)
	switch op {
	case syntax.EQL:
		return c.id == d.id, nil
	case syntax.NEQ:
		return c.id != d.id, nil
	case syntax.LT:
		return c.id < d.id, nil
	case syntax.LE:
		return c.id <= d.id, nil
	case syntax.GT:
		return c.id > d.id, nil
	default:
		return c.id >= d.id, nil
	}
}

func countedBuiltins() StringDict {
	// mk(n): a list of n counted values with ids 1..n; mk(n, off): off+1..off+n
	mk := NewBuiltin("mk", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n, off int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n, &off); err != nil {
			return nil, err
		}
		elems := make([]Value, n)
		for i := range elems {
			elems[i] = counted{off + i + 1}
		}
		return NewList(elems), nil
	})
	zeros := NewBuiltin("zeros", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		var n int
		if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &n); err != nil {
			return nil, err
		}
		elems := make([]Value, n)
		for i := range elems {
			elems[i] = counted{0}
		}
		return NewList(elems), nil
	})
	leaf := NewBuiltin("leaf", func(_ *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) { return counted{1}, nil })
	return StringDict{"mk": mk, "zeros": zeros, "leaf": leaf, "reset": NewBuiltin("reset", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) {
		resetCounts()
		return None, nil
	})}
}

// runCounted runs src (which may call reset()) with a limit of work, and returns
// the work done since the first reset, and the error.
func runCounted(t *testing.T, limit uint64, src string) (work uint64, err error) {
	t.Helper()
	th := &Thread{}
	th.SetMaxWork(limit)
	var base uint64
	extra := countedBuiltins()
	extra["reset"] = NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		resetCounts()
		base = th.Work()
		return None, nil
	})
	_, err = ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, extra)
	return th.Work() - base, err
}
