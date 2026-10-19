package json

// The cycle check of json.encode keeps a set of the containers on the path
// from the depth encodePathSetDepth on, and contains looks only at the set
// then, so that the check of each container is a lookup and not a linear
// search of the path. Without it the encoding of a value nested n deep is
// quadratic in time while its result, its error and its counters are the same
// (struct-0101 of doc/mutants-equivalent.md), so no black-box test, and no
// test of time that is not flaky, can tell it. This test looks at the
// encodePath itself, by what it holds and by what contains consults.

import (
	"strings"
	"testing"
	"unsafe"

	"go.starlark.net/starlark"
)

func pathItems(n int) []unsafe.Pointer {
	cells := make([]int, n)
	items := make([]unsafe.Pointer, n)
	for i := range cells {
		items[i] = unsafe.Pointer(&cells[i])
	}
	return items
}

func samePointers(set map[unsafe.Pointer]struct{}, stack []unsafe.Pointer) bool {
	if len(set) != len(stack) {
		return false
	}
	for _, p := range stack {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

func TestEncodePath_SetFromTheThresholdDepth(t *testing.T) {
	items := pathItems(encodePathSetDepth + 10)
	p := newEncodePath()
	for i, it := range items {
		p.push(it)
		depth := i + 1
		if depth < encodePathSetDepth {
			if p.set != nil {
				t.Fatalf("depth %d: there is a set below the threshold %d", depth, encodePathSetDepth)
			}
			continue
		}
		if p.set == nil {
			t.Fatalf("depth %d: there is no set from the threshold %d", depth, encodePathSetDepth)
		}
		if !samePointers(p.set, p.stack) {
			t.Fatalf("depth %d: the set has %d entries, not exactly the %d of the path", depth, len(p.set), len(p.stack))
		}
	}
	// pop removes from the set the entry it removes from the path
	for len(p.stack) > 0 {
		last := p.stack[len(p.stack)-1]
		p.pop()
		if _, ok := p.set[last]; ok {
			t.Fatalf("depth %d: pop left the entry in the set", len(p.stack)+1)
		}
		if !samePointers(p.set, p.stack) {
			t.Fatalf("depth %d after pop: the set has %d entries, the path %d", len(p.stack), len(p.set), len(p.stack))
		}
	}
	// every entry is found, whatever the depth
	for _, it := range items {
		p.push(it)
		if !p.contains(it) {
			t.Fatalf("depth %d: an entry of the path is not found", len(p.stack))
		}
	}
}

// From the threshold depth contains consults the set and nothing else: a
// pointer that is only in the set is found, and one that is only in the slice
// is not. Below it, contains searches the slice.
func TestEncodePath_ContainsConsultsTheSetFromTheThreshold(t *testing.T) {
	items := pathItems(encodePathSetDepth + 2)
	onlyInSet, onlyInSlice := items[len(items)-2], items[len(items)-1]

	p := newEncodePath()
	for _, it := range items[:encodePathSetDepth-1] {
		p.push(it)
	}
	if p.set != nil {
		t.Fatal("a set below the threshold")
	}
	if !p.contains(items[0]) || p.contains(onlyInSet) {
		t.Fatal("below the threshold contains does not search the path")
	}

	p.push(items[encodePathSetDepth-1]) // the threshold
	if p.set == nil {
		t.Fatal("no set at the threshold")
	}
	p.set[onlyInSet] = struct{}{}
	if !p.contains(onlyInSet) {
		t.Error("an entry that is only in the set is not found: contains does not consult the set")
	}
	p.stack = append(p.stack, onlyInSlice)
	if p.contains(onlyInSlice) {
		t.Error("an entry that is only in the slice is found: contains searches the slice, not the set")
	}
}

// Through the encoder: a cycle deeper than the threshold is an error, and a
// deep value without a cycle is encoded, so the set is consistent after pops.
func TestEncode_CycleDeeperThanTheSetThreshold(t *testing.T) {
	th := &starlark.Thread{}
	encode := func(v starlark.Value) (starlark.Value, error) {
		return starlark.Call(th, Module.Members["encode"], starlark.Tuple{v}, nil)
	}
	n := 3 * encodePathSetDepth
	root := starlark.NewList(nil)
	cur := root
	for i := 1; i < n; i++ {
		next := starlark.NewList(nil)
		cur.Append(next)
		cur = next
	}
	out, err := encode(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out.(starlark.String)); got != strings.Repeat("[", n)+strings.Repeat("]", n) {
		t.Errorf("a deep value was encoded as %.40s...", got)
	}
	cur.Append(root) // a cycle through every container of the path
	if _, err := encode(root); err == nil || !strings.Contains(err.Error(), "cycle in JSON structure") {
		t.Errorf("a cycle of %d containers: %v", n, err)
	}
	// the same container twice, not on one path: no cycle
	shared := starlark.NewList(nil)
	deep := starlark.NewList([]starlark.Value{shared})
	for i := 0; i < n; i++ {
		deep = starlark.NewList([]starlark.Value{deep, shared})
	}
	if _, err := encode(deep); err != nil {
		t.Errorf("a shared child is not a cycle: %v", err)
	}
}
