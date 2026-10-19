package starlark

// The set of the cycle check of the form of a value (valueWriter.pathSet) is
// there for the time: with it the check of each container is a lookup, without
// it a linear search of the path, and the form of a value nested n deep is
// quadratic in time while its work counter stays linear. The result, the error
// and the counters are the same with and without the set (struct-0011 of
// doc/mutants-equivalent.md), so this test looks at the set itself, not at a
// time (no guard asserts a wall time).

import (
	"fmt"
	"testing"
)

// pathSetProblem says what is wrong with the set of w after depth pushes: it
// does not exist from pathSetDepth on, or holds other than depth entries.
func pathSetProblem(w *valueWriter, depth int) string {
	if depth < pathSetDepth {
		if w.pathSet != nil {
			return fmt.Sprintf("after %d pushes the set exists already (it is for the depth %d and beyond)", depth, pathSetDepth)
		}
		return ""
	}
	if w.pathSet == nil {
		return fmt.Sprintf("after %d pushes (>= pathSetDepth %d) pathSet is nil: the cycle check is a linear search", depth, pathSetDepth)
	}
	if len(w.pathSet) != depth {
		return fmt.Sprintf("after %d pushes pathSet holds %d entries, want %d", depth, len(w.pathSet), depth)
	}
	return ""
}

// onPathProblem says what is wrong with onPath of a writer of the depth
// pathSetDepth (the first in which the set exists): it must consult the set
// and nothing else from there on (a container that is only in the set is
// found, one that is only in the slice is not), and the path below. The json
// twin is TestEncodePath_ContainsConsultsTheSetFromTheThreshold.
func onPathProblem(onPath func(w *valueWriter, x Value) bool) string {
	var out sink
	w := valueWriter{out: &out, limit: maxAlloc}
	for i := 0; i < pathSetDepth-1; i++ {
		w.push(NewList(nil))
	}
	onlyInSet, onlyInSlice := NewList(nil), NewList(nil)
	if w.pathSet != nil {
		return "a set below the threshold"
	}
	if !onPath(&w, w.path[0]) || onPath(&w, onlyInSet) {
		return "below the threshold onPath does not search the path"
	}
	w.push(NewList(nil)) // the threshold
	if w.pathSet == nil {
		return "no set at the threshold"
	}
	w.pathSet[onlyInSet] = struct{}{}
	if !onPath(&w, onlyInSet) {
		return "a container that is only in the set is not found: onPath does not consult the set"
	}
	w.path = append(w.path, onlyInSlice)
	if onPath(&w, onlyInSlice) {
		return "a container that is only in the slice is found: onPath searches the slice, not the set"
	}
	return ""
}

func TestPathSet_OnPathConsultsTheSetFromTheThreshold(t *testing.T) {
	if p := onPathProblem((*valueWriter).onPath); p != "" {
		t.Error(p)
	}
	// The check is not vacuous: onPath as a linear search of the path (what
	// onPath without the set is) is a problem, and so is one that looks at
	// the set only (below the threshold too).
	linear := func(w *valueWriter, x Value) bool { return pathContains(w.path, x) }
	if onPathProblem(linear) == "" {
		t.Error("an onPath that searches the slice is not detected")
	}
	setOnly := func(w *valueWriter, x Value) bool { _, ok := w.pathSet[x]; return ok }
	if onPathProblem(setOnly) == "" {
		t.Error("an onPath that never searches the path is not detected")
	}
}

func TestPathSet_ValueWriterHoldsThePathBeyondTheFirstLevels(t *testing.T) {
	var out sink
	w := valueWriter{out: &out, limit: maxAlloc}
	n := pathSetDepth + 50
	lists := make([]*List, n)
	for i := range lists {
		lists[i] = NewList(nil)
	}
	for i, l := range lists {
		w.push(l)
		if problem := pathSetProblem(&w, i+1); problem != "" {
			t.Fatal(problem)
		}
	}
	// The check is not vacuous: a writer without the set (what the mutant
	// struct-0011 makes), and a set that lost an entry, are each a problem.
	noSet := w
	noSet.pathSet = nil
	if pathSetProblem(&noSet, n) == "" {
		t.Error("a writer without the set is not detected")
	}
	lost := w
	lost.pathSet = map[Value]struct{}{}
	for k := range w.pathSet {
		lost.pathSet[k] = struct{}{}
	}
	delete(lost.pathSet, lists[0])
	if pathSetProblem(&lost, n) == "" {
		t.Error("a set that lost an entry is not detected")
	}
	if !w.onPath(lists[0]) || !w.onPath(lists[n-1]) || w.onPath(NewList(nil)) {
		t.Fatal("onPath is not the membership of the path")
	}
	// the pops take the entries out
	for i := n - 1; i >= pathSetDepth; i-- {
		w.pop()
		if len(w.pathSet) != i {
			t.Fatalf("after a pop to depth %d pathSet holds %d entries", i, len(w.pathSet))
		}
		if w.onPath(lists[i]) {
			t.Fatalf("a popped container is on the path (depth %d)", i)
		}
	}
}
