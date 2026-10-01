package starlark

// The prices that do not depend on the size of an operand, each in units counted:
// the call of a built-in, a call that a built-in makes back, a call that is deep,
// an integer that is not small, a lookup in a large table.

import (
	"fmt"
	"strings"
	"testing"
)

// extraOf is the work beyond the steps of the one statement op, after the setup.
func extraOf(t *testing.T, setup, op string) uint64 {
	t.Helper()
	extra, _, _ := extraWork(t, setup+"\nreset()\n"+op)
	return extra
}

// A call of a built-in is charged what the call costs beyond its steps: baseCall
// for the built-ins that have nothing to do, more for those that are known to cost more.
func TestFixed_TheCallOfABuiltin(t *testing.T) {
	for _, c := range []struct {
		op   string
		want uint64
	}{
		{"r = len(l)", 4},
		{"r = type(l)", 4},
		{"r = bool(l)", 4},
		{"l.append(1)", 4},
		{"r = getattr(s, 'upper')", 12},
		{"r = hasattr(s, 'upper')", 12},
		{"r = max(1, 2)", 8 + 2}, // and a comparison of each of the two
		{"r = min(1, 2)", 8 + 2},
	} {
		got := extraOf(t, "l = [1, 2, 3]\ns = 'abc'", c.op)
		// (the unit of an allocation of the result is within a unit or two)
		if got < c.want || got > c.want+3 {
			t.Errorf("%s: %d units beyond the steps, want %d", c.op, got, c.want)
		}
	}
}

// A function that a built-in calls back is charged 3 units a call.
func TestFixed_TheCallbackOfABuiltin(t *testing.T) {
	const n = 1000
	setup := fmt.Sprintf("l = list(range(%d))\ndef f(x):\n    return x\n", n)
	with := extraOf(t, setup, "r = max(l, key=f)")
	without := extraOf(t, setup, "r = max(l)")
	if got, want := with-without, uint64(n*3); got < want {
		t.Errorf("%d callbacks of f were charged %d units more than max(l), want at least %d", n, got, want)
	}
	// the call of f from the code of a script is not a callback
	script := extraOf(t, setup, fmt.Sprintf("for x in l:\n    f(x)\n"))
	if script > n*(4+4) {
		t.Errorf("%d calls of f from a script were charged %d units beyond the steps: a callback price on them", n, script)
	}
}

// A call that is more than recursionScanDepth frames deep is charged 56 units.
func TestFixed_TheCallThatIsDeep(t *testing.T) {
	deep := func(n int) uint64 {
		var b strings.Builder
		fmt.Fprintf(&b, "def f%d():\n    return 1\n", n)
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "def f%d():\n    return f%d()\n", i, i+1)
		}
		return extraOf(t, b.String(), "r = f0()")
	}
	d20, d70 := deep(20), deep(70)
	// 50 more levels, of which those past the 32nd (38 calls) pay
	if got, want := d70-d20, uint64(38*56); got < want {
		t.Errorf("70 levels were charged %d units more than 20, want at least %d for the deep calls", got, want)
	}
	if d20 > 20*4+8 {
		t.Errorf("20 levels were charged %d units beyond the steps: a deep price on a shallow stack", d20)
	}
}

// An integer that is not small costs 5 units to make, and a small one nothing.
func TestFixed_TheIntegerThatIsNotSmall(t *testing.T) {
	small := extraOf(t, "x = 1000", "y = x + 1")
	big := extraOf(t, "x = 1 << 40", "y = x + 1")
	if small != 0 {
		t.Errorf("a sum of small integers was charged %d units beyond the steps", small)
	}
	if big != 5+1 { // the integer, and a word of the operation
		t.Errorf("a sum that is not small was charged %d units, want 6 (5 for the integer, and a word)", big)
	}
}

// A lookup in a table of more than a thousand entries pays for the cache: lookupWork.
func TestFixed_TheLookupInALargeTable(t *testing.T) {
	probe := func(n int) uint64 {
		return extraOf(t, fmt.Sprintf("d = {i: i for i in range(%d)}\nk = %d", n, n/2), "r = k in d")
	}
	if got := probe(1000); got > 2 {
		t.Errorf("a lookup in a table of 1000 entries was charged %d units beyond the steps", got)
	}
	for _, n := range []int{4000, 70000} {
		want := lookupWork(n)
		if got := probe(n); got < want || want == 0 {
			t.Errorf("a lookup in a table of %d entries was charged %d units, want at least %d", n, got, want)
		}
	}
	if lookupWork(1<<20) != 22 || lookupWork(1<<17) != 16 || lookupWork(1000) != 0 {
		t.Errorf("lookupWork: %d at 2^20, %d at 2^17, %d at 1000: want 22, 16 and 0", lookupWork(1<<20), lookupWork(1<<17), lookupWork(1000))
	}
	// the insert pays more, with the table
	if insertWork(1<<20) != 1+4*11 || insertWork(1000) != 1 {
		t.Errorf("insertWork: %d at 2^20 and %d at 1000, want 45 and 1", insertWork(1<<20), insertWork(1000))
	}
}
