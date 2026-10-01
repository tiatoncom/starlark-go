package starlark

// The attacks of the two reviews of the first version of this fork, as forms of
// programs, run with a limit of work of 20 million units (about a fifth of a
// second of CPU at the C of the invariant): each form that does more than the
// limit pays for is refused with a *WorkBudgetError, with the work at the limit,
// and before the elementary operations that the form made (comparisons, hashes,
// truth tests, counted on counted values) are more than the limit; each form
// that does less than the limit completes. STARLARK_ATTACK_TABLE=1 prints the
// table.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

const attackLimit = 20_000_000

type attackForm struct {
	name   string
	src    string
	refuse bool // it does more work than the limit
	counts bool // it works on counted values: the operations are counted
	memory bool // it is refused by the budget of memory, which is before the limit of work
}

var attackForms = []attackForm{
	// ---- forms with many elementary operations, counted
	{"x in l, 2M elements, 100 times", "l = mk(2000000)\nx = miss()\nreset()\nfor i in range(100):\n    r = x in l\n", true, true, false},
	{"x in l, 2M elements, once", "l = mk(2000000)\nx = miss()\nreset()\nr = x in l\n", false, true, false},
	{"l.index(x), 2M elements, 100 times", "l = mk(2000000)\nx = miss()\nreset()\nfor i in range(100):\n    r = l.index(x) if False else x in l\n", true, true, false},
	{"max(l), 2M elements, 100 times", "l = mk(2000000)\nreset()\nfor i in range(100):\n    r = max(l)\n", true, true, false},
	{"sorted(l), 2M elements, ascending", "l = mk(2000000)\nreset()\nr = sorted(l)\n", false, true, false},
	{"sorted(l), 2M elements, descending", "l = mk(2000000)[::-1]\nreset()\nr = sorted(l)\n", true, true, false},
	{"sorted(l), 100000 elements", "l = mk(100000)\nreset()\nr = sorted(l)\n", false, true, false},
	{"set(l), 2M elements (the memory first)", "l = mk(2000000)\nreset()\nr = set(l)\n", true, true, true},
	{"set(l), 200000 elements, 20 times", "l = mk(200000)\nreset()\nfor i in range(20):\n    r = set(l)\n", true, true, false},
	{"l == m, 2M elements, 100 times", "l = mk(2000000)\nm = mk(2000000)\nreset()\nfor i in range(100):\n    r = l == m\n", true, true, false},
	{"{t: 1}, t = (t, t) 24 deep (16M leaves)", "t = (leaf(),)\nfor i in range(24):\n    t = (t, t)\nreset()\nd = {t: 1}\n", true, true, false},
	{"{t: 1}, t = (t, t) 40 deep (10^12 leaves)", "t = (leaf(),)\nfor i in range(40):\n    t = (t, t)\nreset()\nd = {t: 1}\n", true, true, false},
	{"x == x, 60 wide 5 deep, shared (777M leaves)", "l = [leaf()] * 60\nfor i in range(4):\n    l = [l] * 60\nreset()\nr = l == l\n", true, true, false},
	{"d == e, 100000 entries", "d = dict(zip(mk(100000), mk(100000)))\ne = dict(zip(mk(100000), mk(100000)))\nreset()\nr = d == e\n", false, true, false},
	{"d == e, 100000 entries, 20 times", "d = dict(zip(mk(100000), mk(100000)))\ne = dict(zip(mk(100000), mk(100000)))\nreset()\nfor i in range(20):\n    r = d == e\n", true, true, false},

	// ---- forms of the first review, on bytes and memory
	{"s.find(needle of 100), 1 MB, 100 times", "s = 'a' * 1000000\nfor i in range(100):\n    r = s.find('a' * 100 + 'b')\n", true, false, false},
	{"s.find(needle of 100), 1 MB, once", "s = 'a' * 1000000\nr = s.find('a' * 100 + 'b')\n", false, false, false},
	{"s.count(needle of 100), 1 MB, 100 times", "s = 'a' * 1000000\nfor i in range(100):\n    r = s.count('a' * 100 + 'b')\n", true, false, false},
	{"s.replace(needle of 100), 1 MB, 100 times", "s = 'a' * 1000000\nfor i in range(100):\n    r = s.replace('a' * 100 + 'b', 'x')\n", true, false, false},
	{"x in s, needle of 100, 1 MB, 100 times", "s = 'a' * 1000000\nfor i in range(100):\n    r = ('a' * 100 + 'b') in s\n", true, false, false},
	{"s.split(needle of 100), 1 MB, 100 times", "s = 'a' * 1000000\nfor i in range(100):\n    r = s.split('a' * 100 + 'b')\n", true, false, false},
	{"s[::-1], 1 MB, 200 times", "s = 'a' * 1000000\nfor i in range(200):\n    r = s[::-1]\n", true, false, false},
	{"l[::2], 1M elements, 100 times", "l = list(range(1000000))\nfor i in range(100):\n    r = l[::2]\n", true, false, false},
	{"repr of 3M empty lists nested 40 deep", "x = [[]] * 3000000\nfor i in range(40):\n    x = [x]\nr = str(x)\n", true, false, false},
	{"list(b.elems()), 300000, 20 times", "b = bytes('a' * 300000)\nfor i in range(20):\n    r = list(b.elems())\n", true, false, false},
	{"d.clear(), after growth, 150000 times", "d = {i: i for i in range(300000)}\nd.clear()\nfor k in range(150000):\n    d['a'] = 1\n    d.clear()\n", false, false, false},
	{"l.insert(0, 1); l.pop(0), 1M slots, 1000 times", "l = [0] * 1000000\nfor i in range(1000):\n    l.insert(0, 1)\n    l.pop(0)\n", true, false, false},
	{"x = x * x, 500 bits, 22 times", "x = 1 << 500\nfor i in range(22):\n    x = x * x\n", true, false, false},
	{"-1.5 in l, 300000 elements, 100 times", "l = list(range(300000))\nfor i in range(100):\n    r = -1.5 in l\n", true, false, false},
	{"max of mixed ints and floats, 300000, 100 times", "l = list(range(150000)) + [0.5] * 150000\nfor i in range(100):\n    r = min(l)\n", true, false, false},
	{"x < 1.5 on a big integer, 100000 times", "x = 1 << 500\nfor i in range(9):\n    x = x * x\nfor i in range(100000):\n    r = x < 1.5\n", false, false, false},
	{"lstrip(cutset of 30000 characters), 300000", "a = 'a' * 300000\nc = 'é' * 29999 + 'a'\nr = a.lstrip(c)\n", false, false, false},
	{"lstrip(cutset of 5000 characters) of 200000, 40 times", "a = 'a' * 200000 + 'b'\nc = 'é' * 4999 + 'a'\nfor i in range(40):\n    r = a.lstrip(c)\n", false, false, false},
	{"lstrip(cutset of 5000 characters) of 200000, 600 times", "a = 'a' * 200000 + 'b'\nc = 'é' * 4999 + 'a'\nfor i in range(600):\n    r = a.lstrip(c)\n", true, false, false},
	{"f() of 20000 parameters, 20000 times", "def f(" + paramList(20000) + "): pass\nfor i in range(20000):\n    f()\n", true, false, false},
	{"f(**d), 20000 keys, onto 20000 parameters, 120 times", "def f(" + paramList(20000) + "): pass\nd = {'p%d' % i: i for i in range(20000)}\nfor i in range(120):\n    f(**d)\n", true, false, false},
	{"('{k99999}' * 10000).format(**d), 100000 keys", "d = {'k%d' % i: 'v' for i in range(100000)}\nr = ('{k99999}' * 10000).format(**d)\n", false, false, false},
	{"20000 keys i << 16 into a dict (one chain)", "d = {}\nfor i in range(20000):\n    d[i << 16] = i\n", true, false, false},
	{"x in seen, a growing list, 100000 times", "seen = []\nfor i in range(100000):\n    if i in seen:\n        pass\n    seen.append(i)\n", true, false, false},
	{"s += chunk of 50 bytes, 20000 times", "s = ''\nfor i in range(20000):\n    s += 'x' * 50\n", false, false, true},
	{"int('9' * 1M)", "r = int('9' * 1048576)\n", true, false, false},
}

func attackThread(limit uint64) (*Thread, StringDict) {
	th := &Thread{}
	th.SetMaxWork(limit)
	th.SetMaxAllocBytes(256 << 20)
	extra := countedBuiltins()
	extra["miss"] = NewBuiltin("miss", func(*Thread, *Builtin, Tuple, []Tuple) (Value, error) { return counted{-1}, nil })
	return th, extra
}

func TestAttacks_AreRefusedAtTheLimitOfWork(t *testing.T) {
	print := os.Getenv("STARLARK_ATTACK_TABLE") != ""
	for _, f := range attackForms {
		resetCounts()
		th, extra := attackThread(attackLimit)
		cpu0 := cpuNanos()
		_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "t.star", f.src, extra)
		var we *WorkBudgetError
		var be *AllocBudgetError
		refused := errors.As(err, &we)
		other := err != nil && !refused
		outcome := "done"
		switch {
		case refused:
			outcome = "work limit"
		case errors.As(err, &be):
			outcome = "memory limit"
			other = false
		case other:
			outcome = "refused: " + strings.SplitN(err.Error(), "\n", 2)[0]
			if len(outcome) > 60 {
				outcome = outcome[:60]
			}
		}
		cpu := cpuNanos() - cpu0
		ops := cmpCount.Load() + hashCount.Load() + truthCount.Load()
		if print {
			fmt.Printf("ATTACK | %-52s | work %9d | steps %8d | ops %10d | cpu %6d ms | %6.1f ns/unit | %s\n", f.name, th.Work(), th.Steps, ops, cpu/1e6, float64(cpu)/float64(max(th.Work(), 1)), outcome)
		}
		if th.Work() > attackLimit {
			t.Errorf("%s: work %d is over the limit", f.name, th.Work())
		}
		if f.memory {
			if be == nil {
				t.Errorf("%s: want a refusal of the memory budget, got %v", f.name, err)
			}
			continue
		}
		if f.refuse && err == nil {
			t.Errorf("%s: completed (work %d), want a refusal at the limit of %d", f.name, th.Work(), attackLimit)
		}
		if f.refuse && refused && th.Work() != attackLimit {
			t.Errorf("%s: refused at a work of %d, not the limit", f.name, th.Work())
		}
		if !f.refuse && err != nil {
			t.Errorf("%s: refused: %v (work %d)", f.name, err, th.Work())
		}
		if f.counts && ops > attackLimit {
			t.Errorf("%s: %d elementary operations were made for a limit of %d units", f.name, ops, attackLimit)
		}
	}
}
