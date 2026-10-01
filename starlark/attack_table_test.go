package starlark_test

// The table of the attacks, run unchanged on v0.2.0 and on this version:
// the steps taken, the work done (counted on counted values) and the time, with
// the engine's default limit of 10M steps. STARLARK_ATTACK_TABLE=1 prints it.

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type tcounted struct{ id int }

var tcmp, thash, ttruth atomic.Uint64

func (c tcounted) String() string { return "c" }
func (c tcounted) Type() string   { return "tcounted" }
func (c tcounted) Freeze()        {}
func (c tcounted) Truth() starlark.Bool {
	ttruth.Add(1)
	return c.id != 0
}
func (c tcounted) Hash() (uint32, error) { thash.Add(1); return uint32(c.id) * 2654435761, nil }
func (c tcounted) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	tcmp.Add(1)
	d := y.(tcounted)
	switch op {
	case syntax.EQL:
		return c.id == d.id, nil
	case syntax.NEQ:
		return c.id != d.id, nil
	default:
		return c.id < d.id, nil
	}
}

func tcountedBuiltins() starlark.StringDict {
	mk := starlark.NewBuiltin("mk", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kw []starlark.Tuple) (starlark.Value, error) {
		var n int
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kw, 1, &n); err != nil {
			return nil, err
		}
		elems := make([]starlark.Value, n)
		for i := range elems {
			elems[i] = tcounted{i + 1}
		}
		return starlark.NewList(elems), nil
	})
	miss := starlark.NewBuiltin("miss", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return tcounted{-1}, nil
	})
	leaf := starlark.NewBuiltin("leaf", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return tcounted{1}, nil
	})
	reset := starlark.NewBuiltin("reset", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		tcmp.Store(0)
		thash.Store(0)
		ttruth.Store(0)
		return starlark.None, nil
	})
	return starlark.StringDict{"mk": mk, "miss": miss, "leaf": leaf, "reset": reset}
}

func TestAttackTable(t *testing.T) {
	if os.Getenv("STARLARK_ATTACK_TABLE") == "" {
		t.Skip("set STARLARK_ATTACK_TABLE=1 to print the table")
	}
	const S = 10_000_000
	// A nested list of width w and depth d, whose levels are shared.
	for _, a := range []struct{ name, src string }{
		{"x in l, 2M elements (miss)", "l = mk(2000000)\nx = miss()\nreset()\nr = x in l\n"},
		{"l.index(x), 2M elements (miss)", "l = mk(2000000)\nx = miss()\nreset()\nr = l.index(x)\n"},
		{"max(l), 2M elements", "l = mk(2000000)\nreset()\nr = max(l)\n"},
		{"sorted(l), 2M elements", "l = mk(2000000)\nreset()\nr = sorted(l)\n"},
		{"set(l), 2M elements", "l = mk(2000000)\nreset()\nr = set(l)\n"},
		{"l == m, 2M elements", "l = mk(2000000)\nm = mk(2000000)\nreset()\nr = l == m\n"},
		{"{t: 1}, t = (t, t) x 24 (16M leaves)", "t = (leaf(),)\nfor i in range(24): t = (t, t)\nreset()\nd = {t: 1}\n"},
		{"{t: 1}, t = (t, t) x 40 (10^12 leaves)", "t = (leaf(),)\nfor i in range(40): t = (t, t)\nreset()\nd = {t: 1}\n"},
		{"x == x, 60-wide 5-deep shared lists (777M leaves)", "l = [leaf()] * 60\nfor i in range(4): l = [l] * 60\nreset()\nr = l == l\n"},
		{"20000 keys i << 32 into a dict", "d = {}\nfor i in range(20000): d[i << 32] = 1\n"},
		{"int('9' * 65536)", "r = int('9' * 65536)\n"},
		{"int('9' * 1048576)", "r = int('9' * 1048576)\n"},
		{"1000 x l.insert(0, 1); l.pop(0), 1M slots", "l = [0] * 1000000\nfor i in range(1000):\n  l.insert(0, 1)\n  l.pop(0)\n"},
		{"1M x s.find('b'), s 4 KiB (no match)", "s = 'a' * 4096\nfor i in range(1000000): r = s.find('b')\n"},
		{"str(x), x nested 8000 deep", "x = []\nfor i in range(8000): x = [x]\nr = str(x)\n"},
		{"x = x * x, 500-bit int, 22 times", "x = 1 << 500\nfor i in range(22): x = x * x\n"},
	} {
		tcmp.Store(0)
		thash.Store(0)
		ttruth.Store(0)
		th := &starlark.Thread{}
		th.SetMaxExecutionSteps(S)
		t0 := time.Now()
		_, err := starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "t.star", a.src, tcountedBuiltins())
		d := time.Since(t0)
		res := "done"
		if err != nil {
			res = err.Error()
			if i := strings.Index(res, ":"); i > 0 && i < 12 {
				res = strings.TrimSpace(res[strings.Index(res, ": ")+1:])
			}
			if len(res) > 55 {
				res = res[:55]
			}
		}
		fmt.Printf("ATTACK | %-50s | steps %9d | cmp %10d hash %10d truth %8d | %8.0f ms | %s\n", a.name, th.Steps, tcmp.Load(), thash.Load(), ttruth.Load(), float64(d.Microseconds())/1000, res)
	}
}
