package starlark

import (
	"fmt"
	"math"
	"testing"

	"go.starlark.net/syntax"
)

// g4bBytes runs src and returns the bytes allocated by the thread.
func g4bBytes(t *testing.T, src string) uint64 {
	t.Helper()
	th := &Thread{}
	if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, Set: true}, th, "g4b.star", src, nil); err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return th.AllocatedBytes()
}

// M5 (round 2): the sums and the products of the sizes of the search saturate
// (a needle of a hundred megabytes, 16*m, wrapped negative on a platform of
// 32-bit ints, and done+step went negative, so that the window went backwards).
// They are tested here at the widths of this platform with the sizes that
// overflow them (MaxInt/16+1 for the 16*m, MaxInt/2+1 for the 2*m), which are
// the same sizes at any width, as the platform of 32 bits is not run.
func TestG4bSearchSizesSaturate(t *testing.T) {
	chunk, step := searchWindowSizes(math.MaxInt/16 + 1)
	if chunk != math.MaxInt || step != 2*(math.MaxInt/16+1) {
		t.Errorf("m = MaxInt/16+1: chunk %d step %d, want MaxInt and %d", chunk, step, 2*(math.MaxInt/16+1))
	}
	chunk, step = searchWindowSizes(math.MaxInt/2 + 1)
	if chunk != math.MaxInt || step != math.MaxInt {
		t.Errorf("m = MaxInt/2+1: chunk %d step %d, want MaxInt, MaxInt", chunk, step)
	}
	if chunk, step = searchWindowSizes(10); chunk != max(flushWork*searchDivisor(10), 160) || step != 64 {
		t.Errorf("m = 10: chunk %d step %d", chunk, step)
	}
	if next := searchNext(math.MaxInt-5, 100, math.MaxInt); next != math.MaxInt {
		t.Errorf("searchNext(MaxInt-5, 100, MaxInt) = %d, want MaxInt", next)
	}
	if next := searchNext(10, 100, 50); next != 50 {
		t.Errorf("searchNext(10, 100, 50) = %d, want 50", next)
	}
}

// M1 (round 2): the integers of a range beyond int32 are boxes of their own
// (rangeBoxBytes), and every consumer of a range that keeps its elements
// charges them, by the same rule: n * rangeBoxBytes, once for each range whose
// elements are kept. The test is the difference between the same operation on
// range(2**40, 2**40 + n) and on range(n): n boxes of one word, exactly (the
// base of the two programs, without the operation, is subtracted).
func TestG4bRangeConsumersChargeTheBoxes(t *testing.T) {
	const n = 1000
	box := intBytesOfWords(1)
	for _, c := range []struct {
		name, op string
		ranges   uint64 // the ranges whose elements are kept
	}{
		{"list", "r = list(R)", 1},
		{"tuple", "r = tuple(R)", 1},
		{"sorted", "r = sorted(R)", 1},
		{"reversed", "r = reversed(R)", 1},
		{"l += R", "r = []\nr += R", 1},
		{"l.extend(R)", "r = []\nr.extend(R)", 1},
		{"f(*R)", "def f(*a): pass\nf(*R)", 1},
		{"enumerate", "r = enumerate(R)", 1},
		{"zip(R, R)", "r = zip(R, R)", 2},
		{"zip(R, small)", "r = zip(R, range(5000))", 1},
		{"set", "r = set(R)", 1},
		{"s.update(R)", "r = set()\nr.update(R)", 1},
		{"s.union(R)", "r = set().union(R)", 1},
		{"dict(zip(R, R))", "r = dict(zip(R, R))", 2},
	} {
		run := func(r string) uint64 {
			return g4bBytes(t, fmt.Sprintf("R = %s\n%s", r, c.op))
		}
		base := func(r string) uint64 { return g4bBytes(t, fmt.Sprintf("R = %s\n", r)) }
		big, small := fmt.Sprintf("range(1 << 40, (1 << 40) + %d)", n), fmt.Sprintf("range(%d)", n)
		got := (run(big) - base(big)) - (run(small) - base(small))
		if want := c.ranges * n * box; got != want {
			t.Errorf("%s: the boxes of the range are charged %d bytes, want %d = %d * %d * %d", c.name, got, want, c.ranges, n, box)
		}
	}
}
