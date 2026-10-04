package compile

import (
	"fmt"
	"testing"
)

// TestBranchSerialization checks that the table survives Encode/Decode and
// that the format version was raised for it.
func TestBranchSerialization(t *testing.T) {
	if Version != 15 {
		t.Errorf("Version = %d, want 15 (the branch table is part of the format)", Version)
	}
	file := "testdata/branches.star"
	for key, prog := range compileChunks(t, file) {
		got, err := DecodeProgram(prog.Encode())
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		funcs := append([]*Funcode{prog.Toplevel}, prog.Functions...)
		gotFuncs := append([]*Funcode{got.Toplevel}, got.Functions...)
		total := 0
		for i, fn := range funcs {
			want := fmt.Sprintf("%+v", positionsOf(fn.Branches))
			have := fmt.Sprintf("%+v", positionsOf(gotFuncs[i].Branches))
			if want != have {
				t.Errorf("%s: %s: decoded branches differ:\n got %s\nwant %s", key, fn.Name, have, want)
			}
			total += len(fn.Branches)
		}
		if total < 30 {
			t.Errorf("%s: only %d branches in the sample", key, total)
		}
	}
}

// positionsOf renders branches with their positions as text (a Position
// holds a pointer to its file name, which differs after decoding).
func positionsOf(bs []Branch) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = fmt.Sprintf("pc=%d %s#%d cond=%s chain=%s kw=%s neg=%v", b.PC, b.Kind, b.Operand, b.Cond, b.Chain, b.Keyword, b.Negated)
	}
	return out
}
