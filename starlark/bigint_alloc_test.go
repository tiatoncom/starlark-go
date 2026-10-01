package starlark

// Every operation that makes a big integer charges the memory of its result
// (allocBaseInt + allocBytesPerWord a word), before it holds more than the
// budget: the steps that make a million of them bound the time, not the
// memory, and a list of a million integers of 32000 bits holds 4 GiB.

import (
	"fmt"
	"testing"

	"go.starlark.net/syntax"
)

// intOpRun runs the program with mark() around the statement r = op, and
// returns the bytes charged for it and the value of r.
func intOpRun(t *testing.T, setup, op string) (uint64, Value, error) {
	t.Helper()
	th := &Thread{Name: "t"}
	var marks []uint64
	extra := StringDict{"mark": NewBuiltin("mark", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		marks = append(marks, th.AllocatedBytes())
		return None, nil
	})}
	g, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", setup+"\nmark()\nr = "+op+"\nmark()\n", extra)
	if err != nil {
		return 0, nil, err
	}
	return marks[1] - marks[0], g["r"], nil
}

const bigIntOpsSetup = `
y = 1 << 500
for i in range(3):
    y = y * y
y = y >> 20
y2 = y + 12345
neg = -y
f = 1e300
s = '9' * 1000
ns = '-' + s
a = 2147483647
p = 65536
m = 0 - 2147483647 - 1
`

func TestBigInt_EveryOperationChargesItsResult(t *testing.T) {
	for _, op := range []string{
		"y + 1", "1 + y", "y + y2", "y - 1", "1 - y", "y - y2", "y * 3", "y * y2", "3 * y",
		"y << 1", "y << 100", "y << 511", "y >> 1", "y >> 100", "y & y2", "y | 1", "y ^ y2", "y & neg", "y | neg",
		"~y", "-y", "-neg", "abs(neg)", "y // 3", "y // y2", "y2 // 7", "y2 % 1000", "y2 % y", "neg // 3", "neg % 7",
		"int(f)", "int(s)", "int(ns)", "int(f * 1e5)",
		// small operands, big result
		"a + 1", "p * p", "1 << 40", "-m", "m - 1", "a * 2",
		// big operands, small result
		"y - y", "y // y", "y2 % 3", "y >> 600", "y & 7",
	} {
		charged, r, err := intOpRun(t, bigIntOpsSetup, op)
		if err != nil {
			t.Errorf("%s: %v", op, err)
			continue
		}
		var want uint64
		if i, ok := r.(Int); ok {
			want = bigIntBytes(i)
		}
		if charged != want {
			t.Errorf("%s: charged %d bytes, the result (%v bits) is %d bytes", op, charged, r.(Int).BigInt().BitLen(), want)
		}
	}
	// A result that exists is not charged again: abs of a positive, int() of an int, +x.
	for _, op := range []string{"abs(y)", "int(y)", "+y", "y if y else 0", "max(y, y2)"} {
		charged, _, err := intOpRun(t, bigIntOpsSetup, op)
		if err != nil || charged != 0 {
			t.Errorf("%s: charged %d (%v), want 0: the value exists", op, charged, err)
		}
	}
}

// The program of the review: a list of a million integers of 32000 bits holds
// 4 GiB. With a budget it holds the budget, and no more, and the memory the
// process allocated stays within a few times the budget.
func TestBigInt_ListOfBigIntegersIsBoundedByTheBudget(t *testing.T) {
	const budget = 32 * budgetMiB
	src := `
y = 1 << 500
for i in range(6):
    y = y * y
def f():
    r = []
    mark()
    for i in range(2000000):
        r.append(y + i)
        trace(i)
f()
`
	r := runProg(t, budget, src)
	wantBudgetErr(t, r, budget)
	// 32000 bits are 500 words: the charge of an integer is 64 + 9 * 501.
	perInt := intBytesOfWords(501)
	if got, want := uint64(len(r.traced)), uint64(budget)/perInt; got > want || got < want*9/10 {
		t.Errorf("%d integers were made before the refusal, want about %d", got, want)
	}
	if alloc := r.memEnd - r.memMark; alloc > 6*budget {
		t.Errorf("the process allocated %d MiB for a budget of %d MiB", alloc>>20, budget>>20)
	}
}

// A refusal comes before the result exists: an operation that would not fit
// allocates nothing, and charges nothing.
func TestBigInt_RefusalPrecedesTheResult(t *testing.T) {
	const budget = 8 * budgetMiB
	// y of 64000 bits is 8 KB; fill the budget to within a few KB, then try
	// each operation.
	setup := "y = 1 << 500\nfor i in range(7): y = y * y\n"
	used := runProg(t, 0, setup).th.AllocatedBytes()
	// The room left is 5000 bytes: a result of 64000 bits is charged 9064.
	n := (budget - used - 5000 - 48) / 16
	for _, op := range []string{"y + 1", "y - 1", "y * 3", "y << 5", "y & y", "y | 1", "y ^ 1", "-y", "~y", "y // 2", "y >> 1", "abs(-y)"} {
		src := setup + "pad = [0] * " + fmt.Sprint(n) + "\nmark()\nr = " + op + "\n"
		r := runProg(t, budget, src)
		wantBudgetErr(t, r, budget)
		if len(r.marks) != 1 || r.th.AllocatedBytes() != r.marks[0] {
			t.Errorf("%s: a refused operation charged %d bytes", op, r.th.AllocatedBytes()-r.marks[0])
		}
	}
}

// The ceiling of one operation applies: no integer of 1 GiB is made, whatever
// the budget.
func TestBigInt_CeilingOfOneOperation(t *testing.T) {
	saved := maxAlloc
	maxAlloc = 4096
	defer func() { maxAlloc = saved }()
	r := runProg(t, 0, "y = 1 << 500\nfor i in range(7): y = y * y\n") // 64000 bits: 1000 words, 9064 bytes
	if r.err == nil {
		t.Fatalf("an integer over the ceiling was made")
	}
}
