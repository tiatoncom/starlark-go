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

// positionsOf renders branches with all their fields, positions as text (a
// Position holds a pointer to its file name, which differs after decoding).
func positionsOf(bs []Branch) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = fmt.Sprintf("pc=%d %s#%d cond=%s chain=%s kw=%s kwvalid=%v neg=%v id=%d parent=%d echo=%v short=%d flip=%v cmp=%d",
			b.PC, b.Kind, b.Operand, b.Cond, b.Chain, b.Keyword, b.Keyword.IsValid(), b.Negated, b.ChainID, b.ParentChain, b.Echo, b.Short, b.Flip, b.CmpPC)
	}
	return out
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
	for _, b := range function(t, got, "f").Branches {
		if b.Echo {
			echo++
		}
		if b.Short != ShortNone {
			short++
		}
		if b.Flip {
			flip++
		}
		if b.CmpPC >= 0 {
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
	want := fmt.Sprintf("%+v", positionsOf(function(t, prog, "f").Branches))
	if have := fmt.Sprintf("%+v", positionsOf(function(t, got, "f").Branches)); have != want {
		t.Errorf("decoded branches differ:\n got %s\nwant %s", have, want)
	}
	for _, b := range function(t, got, "f").Branches {
		if b.CmpPC >= 0 {
			if g, ok := function(t, got, "f").BranchForCompare(uint32(b.CmpPC)); !ok || g.PC != b.PC {
				t.Errorf("decoded program: BranchForCompare(%d) = %v, %v; want the branch at %d", b.CmpPC, g, ok, b.PC)
			}
		}
	}
}

// TestVersion14Rejected: a program of the format before the branch table
// (Version 14, which has no table) is refused, not read with the table
// of a later format.
func TestVersion14Rejected(t *testing.T) {
	prog := compileSrc(t, "def f(a):\n    if a:\n        pass\n")
	data := prog.Encode()
	if v, n := binary.Varint(data[8:]); v != Version || n != 1 {
		t.Fatalf("version in the data is %d (%d bytes), want %d in one byte", v, n, Version)
	}
	old := append([]byte(nil), data...)
	binary.PutVarint(old[8:], 14)
	_, err := DecodeProgram(old)
	if err == nil || !strings.Contains(err.Error(), "version mismatch: read 14, want 15") {
		t.Errorf("DecodeProgram of Version 14 = %v; want a version mismatch", err)
	}
	if _, err := DecodeProgram(data); err != nil {
		t.Errorf("DecodeProgram of the current version: %v", err)
	}
}
