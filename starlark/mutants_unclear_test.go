package starlark

// Tests that kill mutants that were left alive by the mutation run and that
// are observable (doc/mutants-equivalent.md has the ones that are not).

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

// struct-0136: enterFunction scans the frames when there are at most
// recursionScanDepth of them below the call, and counts them in a map when
// there are more. A call with exactly recursionScanDepth frames below it still
// scans (no map); `<` there would build the map one level early.
func TestMU_ScanDepthBoundaryIsInclusive(t *testing.T) {
	var sawBoundary bool
	for k := 1; k <= recursionScanDepth+8; k++ {
		var mapped bool
		var stackLen int
		probe := NewBuiltin("probe", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
			mapped, stackLen = th.active != nil, len(th.stack)
			return None, nil
		})
		var b strings.Builder
		for i := 0; i < k-1; i++ {
			fmt.Fprintf(&b, "def f%d(): return f%d()\n", i, i+1)
		}
		fmt.Fprintf(&b, "def f%d(): return probe()\nresult = f0()\n", k-1)
		th := &Thread{}
		_, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", b.String(), StringDict{"probe": probe})
		if err != nil {
			t.Fatal(err)
		}
		// frames below the innermost function: the stack has it and the probe on top.
		below := stackLen - 2
		if want := below > recursionScanDepth; mapped != want {
			t.Errorf("chain of %d (%d frames below the last call): map of active calls = %v, want %v", k, below, mapped, want)
		}
		if below == recursionScanDepth {
			sawBoundary = true
		}
	}
	if !sawBoundary {
		t.Fatal("no chain reached exactly recursionScanDepth frames below a call")
	}

	// The scan hook sees the same: a scan at 32 and the counting at 33.
	var seen []int
	scanHook = func(kind string, n int) {
		if kind == "recursion" {
			seen = append(seen, n)
		}
	}
	defer func() { scanHook = nil }()
	if _, _, err := execDeep(t, chainProgram(60), 1000); err != nil {
		t.Fatal(err)
	}
	has := func(n int) bool {
		for _, s := range seen {
			if s == n {
				return true
			}
		}
		return false
	}
	if !has(recursionScanDepth) || !has(recursionScanDepth+1) {
		t.Errorf("the recursion scans were at %v, want one at %d (frames looked at) and one at %d (counted)", seen, recursionScanDepth, recursionScanDepth+1)
	}
}

// struct-0157: sorted() charges a unit for each two swaps, the second of each
// pair: an odd number of swaps is charged one unit fewer than the half rounded
// up.
func TestMU_SortChargesTheSecondOfEachPairOfSwaps(t *testing.T) {
	th := &Thread{}
	s := &sortSlice{values: []Value{MakeInt(1), MakeInt(2)}, m: th.meter()}
	for i, want := range []uint64{0, 1, 1, 2, 2} { // units after 1, 2, 3, 4, 5 swaps
		s.Swap(0, 1)
		if got := s.m.units; got != want {
			t.Errorf("after %d swaps %d units were recorded, want %d", i+1, got, want)
		}
	}
	// Through sorted(): the same comparisons, and a swap of the one pair that
	// is out of order is free, the first of two.
	work := func(src string) uint64 {
		th := &Thread{}
		if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true}, th, "t.star", src, nil); err != nil {
			t.Fatal(err)
		}
		return th.Work()
	}
	if a, b := work("r = sorted([1, 2])"), work("r = sorted([2, 1])"); a != b {
		t.Errorf("one swap was charged: work %d for an ordered pair, %d for a reversed one", a, b)
	}
}

// struct-0091: all the words of a big int take part in its hash, not the first.
func TestMU_IntHashUsesAllTheWords(t *testing.T) {
	seen := map[uint32]int{}
	const n = 64
	for k := 64; k < 64+n; k++ {
		h, err := MakeInt(1).Lsh(uint(k)).Add(MakeInt(1)).Hash()
		if err != nil {
			t.Fatal(err)
		}
		seen[h]++
	}
	if len(seen) < n-2 {
		t.Errorf("the hashes of 2**k+1 for k in [64, 128) take %d values, want about %d: the words above the first are not mixed in", len(seen), n)
	}
	// Two ints that differ only in the second word.
	a, _ := MakeInt(1).Lsh(64).Add(MakeInt(1)).Hash()
	b, _ := MakeInt(1).Lsh(65).Add(MakeInt(1)).Hash()
	if a == b {
		t.Errorf("2**64+1 and 2**65+1 have the hash %d", a)
	}
}

// struct-0147: pathSetDepth is also where the form of a value charges 8 units
// for each container it enters (value.go push): the work of str and repr of a
// list nested 20 and 32 levels is exact. With a depth of 16 it is 40 and 128
// units more.
func TestMU_FormOfANestedListChargesFromDepth32(t *testing.T) {
	for _, c := range []struct {
		depth int
		work  uint64
	}{{20, 365}, {32, 577}} {
		for _, fn := range []string{"str", "repr"} {
			src := fmt.Sprintf("x = []\nfor i in range(%d):\n    x = [x]\nr = %s(x)\n", c.depth, fn)
			th := &Thread{}
			if _, err := ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star", src, nil); err != nil {
				t.Fatal(err)
			}
			if got := th.Work(); got != c.work {
				t.Errorf("%s of a list nested %d levels: work %d, want %d", fn, c.depth, got, c.work)
			}
		}
	}
}
