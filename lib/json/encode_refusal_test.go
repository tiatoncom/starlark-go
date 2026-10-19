package json_test

// json.encode refuses an output that would outgrow the allocation budget while
// it is being counted, not when it is done: the size at which it refuses (the
// Requested of the *AllocBudgetError) is that of the output at the moment, a
// few bytes past the headroom, and not the size of the whole output that is
// never made. No heap is measured: the number is the observable.

import (
	"errors"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

func mcEncodeRefused(t *testing.T, budget uint64, x starlark.Value) (*starlark.AllocBudgetError, *starlark.Thread) {
	t.Helper()
	th := &starlark.Thread{Name: "t"}
	th.SetMaxAllocBytes(budget)
	_, err := starlark.Call(th, json.Module.Members["encode"], starlark.Tuple{x}, nil)
	var be *starlark.AllocBudgetError
	if !errors.As(err, &be) {
		t.Fatalf("json.encode: error %v, want an *AllocBudgetError", err)
	}
	if be.Limit != budget {
		t.Fatalf("limit = %d, want %d", be.Limit, budget)
	}
	return be, th
}

// A value that shares its children, x = [x, x] nested 22 levels, is 4 million
// leaves, which the encoder walks in full if it does not stop at the headroom:
// it stops at the first node past the budget (at most the 3 bytes of a "[", a
// "," and a "]" later), not at the end of the output.
func TestMCEncode_SharedValueIsRefusedAtTheHeadroom(t *testing.T) {
	const budget = 1 << 20
	var x starlark.Value = starlark.MakeInt(1)
	for i := 0; i < 22; i++ {
		x = starlark.Tuple{x, x}
	}
	be, th := mcEncodeRefused(t, budget, x)
	if be.Requested <= budget || be.Requested > budget+8 {
		t.Errorf("refused at %d bytes of output, want just over the headroom of %d", be.Requested, budget)
	}
	if be.Allocated != 0 || th.AllocatedBytes() != 0 {
		t.Errorf("the refused encoding charged %d bytes (the error says %d)", th.AllocatedBytes(), be.Allocated)
	}
}

// A string of 16 MiB of "\x00" quotes to 96 MiB (6 bytes each): the size of the
// leaf is checked before it is written, so that the refusal asks for the output
// that there is at the leaf (the "[" before it, and its own quoted size), not
// for that of the whole, which a leaf that was let through would be followed by
// the "]" of.
func TestMCEncode_QuotedLeafIsRefusedBeforeItIsWritten(t *testing.T) {
	const n = 16 << 20
	s := starlark.String(strings.Repeat("\x00", n))
	quoted := uint64(2 + 6*n)
	for _, c := range []struct {
		name string
		x    starlark.Value
		want uint64
	}{
		{"in a tuple", starlark.Tuple{s}, 1 + quoted},
		{"in a list", starlark.NewList([]starlark.Value{s}), 1 + quoted},
		{"alone", s, quoted},
	} {
		be, th := mcEncodeRefused(t, 20<<20, c.x)
		if be.Requested != c.want {
			t.Errorf("%s: refused at %d bytes, want %d", c.name, be.Requested, c.want)
		}
		if be.Allocated != 0 || th.AllocatedBytes() != 0 {
			t.Errorf("%s: the refused encoding charged %d bytes", c.name, th.AllocatedBytes())
		}
	}
}
