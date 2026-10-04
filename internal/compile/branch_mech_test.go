package compile

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// shortName is the name of a ShortCircuit in the tests.
func shortName(s ShortCircuit) string {
	return [...]string{"-", "truthy", "falsy"}[s]
}

// TestBranchMechanics checks Echo, Short, Flip and CmpPC, which the
// interpreter reads instead of looking at the code. Per branch:
// kind#operand echo/short/flip/cmp (cmp: whether the jump tests a comparison).
func TestBranchMechanics(t *testing.T) {
	for _, test := range []struct {
		cond string
		want []string
	}{
		{"a", []string{"if#-1 -/-/-/-"}},
		{"a == b", []string{"if#-1 -/-/-/cmp"}},
		{"a not in b", []string{"if#-1 -/-/-/cmp"}},      // ifelse swaps the successors: no NOT
		{"not a not in b", []string{"if#-1 -/-/-/cmp"}},  // two swaps
		{"(a not in b)", []string{"if#-1 -/-/flip/cmp"}}, // evaluated as a value: IN, NOT
		{"not (a not in b)", []string{"if#-1 -/-/flip/cmp"}},

		// ifelse-level operands have no DUP and no Short; a chain that is
		// compiled as a value is closed by an Echo
		{"a and b", []string{"and#0 -/-/-/-", "and#1 -/-/-/-"}},
		{"a == 1 and b < 2", []string{"and#0 -/-/-/cmp", "and#1 -/-/-/cmp"}},
		{"(a and b)", []string{"and#0 -/falsy/-/-", "and#1 echo/-/-/-"}},
		{"(a or b)", []string{"or#0 -/truthy/-/-", "or#1 echo/-/-/-"}},
		{"(a == 1 or b == 2)", []string{"or#0 -/truthy/-/cmp", "or#1 echo/-/-/cmp"}},
		{"a and (b or c)", []string{"and#0 -/-/-/-", "or#0 -/truthy/-/-", "or#1 echo/-/-/-"}},
		{"(a or b) and c", []string{"or#0 -/truthy/-/-", "or#1 echo/-/-/-", "and#1 -/-/-/-"}},
		// a spine of one operator: the inner test is itself a DUP operand and
		// an echo of the one before it
		{"(a and b and c)", []string{"and#0 -/falsy/-/-", "and#1 echo/falsy/-/-", "and#2 echo/-/-/-"}},
		{"a and b and c", []string{"and#0 -/falsy/-/-", "and#1 echo/-/-/-", "and#2 -/-/-/-"}}, // (a and b) is a value
		{"a or b or c", []string{"or#0 -/truthy/-/-", "or#1 echo/-/-/-", "or#2 -/-/-/-"}},
		{"not (a and b)", []string{"and#0 -/falsy/-/-", "and#1 echo/-/-/-"}},
		// not on a value: the jump sees the opposite truth
		{"not (a and b) and c", []string{"and#0 -/falsy/-/-", "and#1 echo/-/flip/-", "and#1 -/-/-/-"}},
		{"not not (a and b) and c", []string{"and#0 -/falsy/-/-", "and#1 echo/-/-/-", "and#1 -/-/-/-"}},
		{"not a and b", []string{"and#0 -/-/flip/-", "and#1 -/-/-/-"}},
		{"not a == 1 and b", []string{"and#0 -/-/flip/cmp", "and#1 -/-/-/-"}},
		{"a not in c and b", []string{"and#0 -/-/flip/cmp", "and#1 -/-/-/-"}},
		{"a and not (b or c)", []string{"and#0 -/-/-/-", "or#0 -/truthy/-/-", "or#1 echo/-/-/-"}},
	} {
		src := "def f(a, b, c, d, e):\n    if " + test.cond + ":\n        pass\n"
		fn := function(t, compileSrc(t, src), "f")
		var got []string
		for _, b := range fn.Branches {
			s := func(v bool, name string) string {
				if v {
					return name
				}
				return "-"
			}
			got = append(got, fmt.Sprintf("%s#%d %s/%s/%s/%s", b.Kind, b.Operand, s(b.Echo, "echo"), shortName(b.Short), s(b.Flip, "flip"), s(b.CmpPC >= 0, "cmp")))
		}
		sort.Strings(got)
		want := append([]string(nil), test.want...)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("if %s:\n got:\n  %s\nwant:\n  %s", test.cond, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

// TestBranchCompareIndex: BranchForCompare finds, for the address of a
// comparison, the branch whose CmpPC it is, and nothing for any other
// address; the address is that of a comparison instruction.
func TestBranchCompareIndex(t *testing.T) {
	check := func(name string, fn *Funcode) int {
		n := 0
		for i := range fn.Branches {
			b := &fn.Branches[i]
			if b.CmpPC < 0 {
				continue
			}
			n++
			switch op := Opcode(fn.Code[b.CmpPC]); op {
			case EQL, NEQ, LT, LE, GT, GE, IN:
			default:
				t.Errorf("%s: CmpPC %d of the branch at %d is %s", name, b.CmpPC, b.PC, op)
			}
			if b.CmpPC >= int(b.PC) && Opcode(fn.Code[b.CmpPC]) != IN {
				// the comparison may be laid out after the jump (a chain
				// compiled as a value), but only when the jump is a join
				if !b.Echo {
					t.Errorf("%s: CmpPC %d is after the jump at %d that is not an echo", name, b.CmpPC, b.PC)
				}
			}
			got, ok := fn.BranchForCompare(uint32(b.CmpPC))
			if !ok || got != b {
				t.Errorf("%s: BranchForCompare(%d) = %v, %v; want the branch at %d", name, b.CmpPC, got, ok, b.PC)
			}
		}
		for pc := range fn.Code {
			if _, ok := fn.BranchForCompare(uint32(pc)); ok {
				found := false
				for _, b := range fn.Branches {
					found = found || b.CmpPC == pc
				}
				if !found {
					t.Errorf("%s: BranchForCompare(%d) found a branch that does not have CmpPC %d", name, pc, pc)
				}
			}
		}
		return n
	}
	total := 0
	for key, prog := range compileChunks(t, "testdata/branches.star") {
		for _, fn := range append([]*Funcode{prog.Toplevel}, prog.Functions...) {
			total += check(key+" "+fn.Name, fn)
		}
		// the index survives encoding
		got, err := DecodeProgram(prog.Encode())
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range append([]*Funcode{got.Toplevel}, got.Functions...) {
			check(key+" decoded "+fn.Name, fn)
		}
	}
	if total < 10 {
		t.Errorf("only %d comparisons tested by a branch in the sample", total)
	}
}

// TestBranchChainNumbers: ChainID counts the chains of the function in the
// order in which they start in the source, for all its conditions, also when
// the compiler meets a condition of a conditional expression before one that
// stands earlier in the source; ParentChain is the ChainID of the chain around.
func TestBranchChainNumbers(t *testing.T) {
	src := `def two(a, b, c, d):
    if a and b:
        pass
    if c or d:
        pass
    while a and (b or c):
        pass

def source_order(a, b, c, d):
    return (1 if (a and b) else 2) if (c and d) else 3
`
	prog := compileSrc(t, src)
	type rec struct {
		id, parent int
		kind       string
	}
	chains := func(name string) []rec {
		var out []rec
		seen := map[rec]bool{}
		for _, b := range function(t, prog, name).Branches {
			r := rec{b.ChainID, b.ParentChain, b.Kind.String()}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
		return out
	}
	if got, want := fmt.Sprint(chains("two")), fmt.Sprint([]rec{{0, -1, "and"}, {1, -1, "or"}, {2, -1, "and"}, {3, 2, "or"}}); got != want {
		t.Errorf("two chains in different statements:\n got %s\nwant %s", got, want)
	}
	// "(a and b)" starts before "(c and d)", although the compiler meets the
	// condition (c and d) first
	got := function(t, prog, "source_order").Branches
	byCol := map[int]int{}
	for _, b := range got {
		byCol[int(b.Chain.Col)] = b.ChainID
	}
	line := strings.Split(src, "\n")[9]
	ab, cd := strings.Index(line, "(a and b)")+1, strings.Index(line, "(c and d)")+1
	if byCol[ab] != 0 || byCol[cd] != 1 {
		t.Errorf("chain numbers by start column: (a and b)@%d=%d, (c and d)@%d=%d; want 0 and 1", ab, byCol[ab], cd, byCol[cd])
	}
}
