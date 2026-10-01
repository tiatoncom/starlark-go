package starlark

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"testing"
)

// A recorder is a sortable slice of ints that records the calls that sorted it:
// how many of each, and a hash of the sequence.
type recorder struct {
	a             []int
	less, swaps   int
	seq           uint64
	byValueStable []int // the index each value came from (to see stability)
}

func (r *recorder) Len() int { return len(r.a) }
func (r *recorder) Less(i, j int) bool {
	r.less++
	r.seq = r.seq*1000003 + uint64(i)*7919 + uint64(j)*31 + 1
	return r.a[i] < r.a[j]
}
func (r *recorder) Swap(i, j int) {
	r.swaps++
	r.seq = r.seq*1000003 + uint64(i)*104729 + uint64(j)*17 + 2
	r.a[i], r.a[j] = r.a[j], r.a[i]
	r.byValueStable[i], r.byValueStable[j] = r.byValueStable[j], r.byValueStable[i]
}

func newRecorder(a []int) *recorder {
	r := &recorder{a: append([]int(nil), a...), byValueStable: make([]int, len(a))}
	for i := range r.byValueStable {
		r.byValueStable[i] = i
	}
	return r
}

func sortCases() map[string][]int {
	rnd := rand.New(rand.NewSource(1))
	cases := map[string][]int{}
	for _, n := range []int{0, 1, 2, 5, 19, 20, 21, 40, 41, 100, 1000, 5000} {
		a := make([]int, n)
		for i := range a {
			a[i] = rnd.Intn(max(n/3, 1))
		}
		cases[fmt.Sprintf("random/%d", n)] = a
		b := make([]int, n)
		for i := range b {
			b[i] = i
		}
		cases[fmt.Sprintf("ascending/%d", n)] = b
		c := make([]int, n)
		for i := range c {
			c[i] = n - i
		}
		cases[fmt.Sprintf("descending/%d", n)] = c
		d := make([]int, n)
		cases[fmt.Sprintf("equal/%d", n)] = d
	}
	return cases
}

// stableSort sorts, and is stable, and the calls it makes are the same as
// sort.Stable's of the Go that the code was copied from.
func TestStableSort_SortsStably(t *testing.T) {
	for name, a := range sortCases() {
		r := newRecorder(a)
		stableSort(r, len(a))
		if !sort.IntsAreSorted(r.a) {
			t.Errorf("%s: not sorted", name)
		}
		for i := 1; i < len(r.a); i++ {
			if r.a[i] == r.a[i-1] && r.byValueStable[i] < r.byValueStable[i-1] {
				t.Errorf("%s: not stable at %d", name, i)
				break
			}
		}
	}
}

// The sequence of calls is a function of the code of this package (and the
// data): these are the numbers of calls and the hash of the sequence, for fixed
// data, taken from sort.Stable of Go 1.26. A different number is a different
// algorithm, whose work the prices of sorted() were not measured on.
func TestStableSort_TheSequenceOfCallsIsFixed(t *testing.T) {
	h := fnv.New64a()
	for _, name := range sortedKeys(sortCases()) {
		a := sortCases()[name]
		r := newRecorder(a)
		stableSort(r, len(a))
		fmt.Fprintf(h, "%s %d %d %d\n", name, r.less, r.swaps, r.seq)
	}
	const want uint64 = 11731904273205551358
	if got := h.Sum64(); got != want {
		t.Errorf("the calls of the sort are not those that were measured: %d, want %d", got, want)
	}
	t.Logf("hash of the calls of the sort: %d", h.Sum64())
}

func sortedKeys(m map[string][]int) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// The same sequence as sort.Stable (of the Go that built the test, so this
// test is the one to look at if the standard library's algorithm changes: it
// does not matter to the work, which is of THIS code).
func TestStableSort_IsTheStandardLibrarysToday(t *testing.T) {
	for name, a := range sortCases() {
		r1, r2 := newRecorder(a), newRecorder(a)
		stableSort(r1, len(a))
		sort.Stable(r2)
		if r1.less != r2.less || r1.swaps != r2.swaps || r1.seq != r2.seq {
			t.Logf("%s: this sort makes %d/%d calls, sort.Stable %d/%d (the algorithm of the standard library changed: the copy is the one that counts)", name, r1.less, r1.swaps, r2.less, r2.swaps)
		}
	}
}
