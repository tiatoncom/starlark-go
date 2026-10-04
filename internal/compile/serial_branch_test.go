package compile

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// TestBranchSerialization checks that the table survives Encode/Decode and
// that the format version was raised for it.
func TestBranchSerialization(t *testing.T) {
	if Version != 16 {
		t.Errorf("Version = %d, want 16 (the branch and chain tables are part of the format)", Version)
	}
	file := "testdata/branches.star"
	for key, prog := range compileChunks(t, file) {
		got, err := DecodeProgram(prog.Encode())
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		funcs := append([]*Funcode{prog.Toplevel}, prog.Functions...)
		gotFuncs := append([]*Funcode{got.Toplevel}, got.Functions...)
		total, chains, bare := 0, 0, 0
		for i, fn := range funcs {
			want := fmt.Sprintf("%+v", positionsOf(fn))
			have := fmt.Sprintf("%+v", positionsOf(gotFuncs[i]))
			if want != have {
				t.Errorf("%s: %s: decoded branches differ:\n got %s\nwant %s", key, fn.Name, have, want)
			}
			total += len(fn.Branches)
			if want, have := chainsText(fn), chainsText(gotFuncs[i]); fmt.Sprint(want) != fmt.Sprint(have) {
				t.Errorf("%s: %s: decoded chains differ:\n got %s\nwant %s", key, fn.Name, have, want)
			}
			chains += len(fn.Chains)
			bare += chainsWithoutBranch(fn)
		}
		if total < 30 {
			t.Errorf("%s: only %d branches in the sample", key, total)
		}
		if chains < 10 || bare == 0 {
			t.Errorf("%s: %d chains in the sample, %d of them without a Branch", key, chains, bare)
		}
	}
}

// positionsOf renders branches with all their fields, positions as text (a
// Position holds a pointer to its file name, which differs after decoding).
func positionsOf(fn *Funcode) []string {
	out := make([]string, len(fn.Branches))
	for i, b := range fn.Branches {
		m := fn.BranchMech[i]
		out[i] = fmt.Sprintf("pc=%d %s#%d cond=%s chain=%s kw=%s kwvalid=%v neg=%v id=%d parent=%d echo=%v short=%d flip=%v cmp=%d",
			b.PC, b.Kind, b.Operand, b.Cond, b.Chain, b.Keyword, b.Keyword.IsValid(), b.Negated, b.ChainID, b.ParentChain, m.Echo, m.Short, m.Flip, m.CmpPC)
	}
	return out
}

// chainsText renders the chain table with all its fields, positions as text.
func chainsText(fn *Funcode) []string {
	out := make([]string, len(fn.Chains))
	for i, c := range fn.Chains {
		out[i] = fmt.Sprintf("%d %s start=%s parent=%d slot=%d", i, c.Kind, c.Start, c.Parent, c.ParentOperand)
	}
	return out
}

// chainsWithoutBranch counts the chains that no Branch of fn names.
func chainsWithoutBranch(fn *Funcode) int {
	named := map[int]bool{}
	for _, b := range fn.Branches {
		if b.ChainID >= 0 {
			named[b.ChainID] = true
		}
	}
	return len(fn.Chains) - len(named)
}

// TestBranchSerializationFlags: each of the fields of the table that is not a
// position survives the round trip with a value other than the zero one.
func TestBranchSerializationFlags(t *testing.T) {
	src := `def f(a, b, c, xs):
    if not (a == 1 and b in xs) and (c or a < 2):
        pass
`
	prog := compileSrc(t, src)
	got, err := DecodeProgram(prog.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var echo, short, flip, cmp, neg, parent, chain, noKeyword int
	gf := function(t, got, "f")
	if len(gf.BranchMech) != len(gf.Branches) {
		t.Fatalf("%d mechanics for %d branches", len(gf.BranchMech), len(gf.Branches))
	}
	for i, b := range gf.Branches {
		m := gf.BranchMech[i]
		if m.Echo {
			echo++
		}
		if m.Short != ShortNone {
			short++
		}
		if m.Flip {
			flip++
		}
		if m.CmpPC >= 0 {
			cmp++
		}
		if b.Negated {
			neg++
		}
		if b.ParentChain >= 0 {
			parent++
		}
		if b.ChainID > 0 {
			chain++
		}
		if !b.Keyword.IsValid() {
			noKeyword++
		}
	}
	if echo == 0 || short == 0 || flip == 0 || cmp == 0 || neg == 0 || parent == 0 || chain == 0 || noKeyword == 0 {
		t.Errorf("decoded table lacks a flag: echo=%d short=%d flip=%d cmp=%d neg=%d parent=%d chain=%d nokeyword=%d", echo, short, flip, cmp, neg, parent, chain, noKeyword)
	}
	want := fmt.Sprintf("%+v", positionsOf(function(t, prog, "f")))
	if have := fmt.Sprintf("%+v", positionsOf(function(t, got, "f"))); have != want {
		t.Errorf("decoded branches differ:\n got %s\nwant %s", have, want)
	}
	for i, b := range gf.Branches {
		if cmpPC := gf.BranchMech[i].CmpPC; cmpPC >= 0 {
			if g, ok := gf.BranchForCompare(uint32(cmpPC)); !ok || g.PC != b.PC {
				t.Errorf("decoded program: BranchForCompare(%d) = %v, %v; want the branch at %d", cmpPC, g, ok, b.PC)
			}
		}
	}
}

// TestVersionRejected: a program of an earlier format (14, before the branch
// table; 15, before the chain table) is refused, not read with the tables of
// a later format.
func TestVersionRejected(t *testing.T) {
	prog := compileSrc(t, "def f(a):\n    if a:\n        pass\n")
	data := prog.Encode()
	if v, n := binary.Varint(data[8:]); v != Version || n != 1 {
		t.Fatalf("version in the data is %d (%d bytes), want %d in one byte", v, n, Version)
	}
	for _, v := range []int64{14, 15} {
		old := append([]byte(nil), data...)
		binary.PutVarint(old[8:], v)
		_, err := DecodeProgram(old)
		if want := fmt.Sprintf("version mismatch: read %d, want %d", v, Version); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("DecodeProgram of Version %d = %v; want %q", v, err, want)
		}
	}
	if _, err := DecodeProgram(data); err != nil {
		t.Errorf("DecodeProgram of the current version: %v", err)
	}
}
