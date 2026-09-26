package starlark

// These tests check that a single operation cannot allocate an unbounded
// result while counting as only one execution step.

import (
	"strings"
	"testing"
	"time"

	"github.com/tiatoncom/starlark-go/syntax"
)

// smallLimit lowers maxAlloc so the tests need no large allocations.
func smallLimit(t *testing.T) {
	t.Helper()
	old := maxAlloc
	maxAlloc = 1 << 16
	t.Cleanup(func() { maxAlloc = old })
}

func mustErr(t *testing.T, src, wantSubstr string) {
	t.Helper()
	smallLimit(t)
	done := make(chan error, 1)
	go func() {
		th := &Thread{}
		_, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true}, th, "t.star", "def f():\n"+src+"\nf()\n", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), wantSubstr) {
			t.Fatalf("want error containing %q, got %v", wantSubstr, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no error within 20s; the limit did not fire cheaply (src=%q)", src)
	}
}

func mustOK(t *testing.T, src string) {
	t.Helper()
	smallLimit(t)
	th := &Thread{}
	if _, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true}, th, "t.star", "def f():\n"+src+"\nf()\n", nil); err != nil {
		t.Fatalf("legitimate program errored: %v (src=%q)", err, src)
	}
}

func TestAllocLimit_StringConcatDoubling(t *testing.T) {
	mustErr(t, "  s = 'x'\n  for i in range(40): s = s + s", "excessive string concatenation")
}

func TestAllocLimit_ListConcatDoubling(t *testing.T) {
	mustErr(t, "  x = [1]\n  for i in range(40): x = x + x", "excessive list concatenation")
}

func TestAllocLimit_TupleConcatDoubling(t *testing.T) {
	mustErr(t, "  x = (1,)\n  for i in range(40): x = x + x", "excessive tuple concatenation")
}

func TestAllocLimit_ListInplaceAddDoubling(t *testing.T) {
	mustErr(t, "  x = [1]\n  for i in range(40): x += x", "excessive list extension")
}

func TestAllocLimit_StrOfSharedSubgraph(t *testing.T) {
	mustErr(t, "  x = [1]\n  for i in range(40): x = [x, x]\n  s = str(x)", "size limit")
}

func TestAllocLimit_PercentOfSharedSubgraph(t *testing.T) {
	mustErr(t, "  x = [1]\n  for i in range(40): x = [x, x]\n  s = '%s' % (x,)", "excessive string interpolation")
}

// Operations that remain within the limit should behave normally.
func TestAllocLimit_NormalOpsUnchanged(t *testing.T) {
	mustOK(t, "  s = 'a' + 'b'\n  x = [1] + [2, 3]\n  y = (1,) + (2,)\n  z = [1]\n  z += [2, 3]")
	mustOK(t, "  d = {'a': [1, 2]}\n  s = str(d)\n  t = '%s' % (d,)")
	mustOK(t, "  x = []\n  for i in range(1000): x = x + [i]\n  s = str(x)")
	mustOK(t, "  x = [1]\n  for i in range(10): x = [x, x]\n  s = str(x)") // 2^10 nodes, below the limit
}
