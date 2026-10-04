package compile

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/resolve"
	"go.starlark.net/syntax"
)

// compileSrc compiles a whole file and returns its program.
func compileSrc(t *testing.T, src string) *Program {
	t.Helper()
	file := "t.star"
	opts := &syntax.FileOptions{While: true, Set: true}
	f, err := opts.Parse(file, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolve.File(f, func(string) bool { return false }, func(name string) bool { return name == "range" }); err != nil {
		t.Fatal(err)
	}
	m := f.Module.(*resolve.Module)
	return File(f.Options, f.Stmts, syntax.MakePosition(&file, 1, 1), "<toplevel>", m.Locals, m.Globals)
}

// function returns the compiled def named name.
func function(t *testing.T, prog *Program, name string) *Funcode {
	t.Helper()
	for _, fn := range prog.Functions {
		if fn.Name == name {
			return fn
		}
	}
	t.Fatalf("no function %s", name)
	return nil
}

// A wantBranch names expected positions by the source text that starts there
// (the first occurrence of the text in the line of the condition).
type wantBranch struct {
	kind    string
	operand int
	cond    string
	chain   string
	keyword string
	neg     bool
}

func renderBranch(b Branch) string {
	s := fmt.Sprintf("%s#%d cond=%d chain=%d kw=%d", b.Kind, b.Operand, b.Cond.Col, b.Chain.Col, b.Keyword.Col)
	if b.Negated {
		s += " neg"
	}
	return s
}

func renderWant(line string, w wantBranch) string {
	col := func(needle string) int {
		i := strings.Index(line, needle)
		if i < 0 {
			panic(fmt.Sprintf("%q not in %q", needle, line))
		}
		return i + 1
	}
	s := fmt.Sprintf("%s#%d cond=%d chain=%d kw=%d", w.kind, w.operand, col(w.cond), col(w.chain), col(w.keyword))
	if w.neg {
		s += " neg"
	}
	return s
}

// TestBranchTable checks the table of one condition at a time: the kinds,
// operand numbers, positions of the condition, of its chain, of the keyword
// or operator, and Negated.
func TestBranchTable(t *testing.T) {
	for _, test := range []struct {
		cond string
		want []wantBranch
	}{
		{"a", []wantBranch{{"if", -1, "a:", "a:", "if", false}}},
		{"(a)", []wantBranch{{"if", -1, "(a)", "(a)", "if", false}}},
		{"((a))", []wantBranch{{"if", -1, "((a))", "((a))", "if", false}}},
		{"a == b", []wantBranch{{"if", -1, "a ==", "a ==", "if", false}}},
		{"(a == b)", []wantBranch{{"if", -1, "(a ==", "(a ==", "if", false}}},
		{"not a", []wantBranch{{"if", -1, "not a", "not a", "if", true}}},
		{"not not a", []wantBranch{{"if", -1, "not not", "not not", "if", false}}},
		{"not (a == b)", []wantBranch{{"if", -1, "not (", "not (", "if", true}}},
		{"a in b", []wantBranch{{"if", -1, "a in", "a in", "if", false}}},
		{"a not in b", []wantBranch{{"if", -1, "a not", "a not", "if", true}}},
		{"not a not in b", []wantBranch{{"if", -1, "not a", "not a", "if", false}}},

		// chains
		{"a and b and c", []wantBranch{
			{"and", 0, "a and", "a and", "and b", false},
			{"and", 1, "b and", "a and", "and b", false},
			{"and", 2, "c:", "a and", "and c", false},
		}},
		{"a or b or c", []wantBranch{
			{"or", 0, "a or", "a or", "or b", false},
			{"or", 1, "b or", "a or", "or b", false},
			{"or", 2, "c:", "a or", "or c", false},
		}},
		{"a and b", []wantBranch{
			{"and", 0, "a and", "a and", "and b", false},
			{"and", 1, "b:", "a and", "and b", false},
		}},

		// mixed and nested chains: the examples that fix the numbering contract
		{"a and (b or c)", []wantBranch{
			{"and", 0, "a and", "a and", "and (", false},
			{"or", 0, "b or", "(b or c)", "or c", false},
			{"or", 1, "c)", "(b or c)", "or c", false},
		}},
		{"not (a and b)", []wantBranch{
			{"and", 0, "a and", "(a and b)", "and b", true},
			{"and", 1, "b)", "(a and b)", "and b", true},
		}},
		{"a or b and c", []wantBranch{
			{"or", 0, "a or", "a or", "or b", false},
			{"and", 0, "b and", "b and", "and c", false},
			{"and", 1, "c:", "b and", "and c", false},
		}},
		{"a and b or c", []wantBranch{
			{"and", 0, "a and", "a and", "and b", false},
			{"and", 1, "b or", "a and", "and b", false},
			{"or", 1, "c:", "a and", "or c", false},
		}},
		{"(a and b) or (c and d)", []wantBranch{
			{"and", 0, "a and", "(a and b)", "and b", false},
			{"and", 1, "b)", "(a and b)", "and b", false},
			{"and", 0, "c and", "(c and d)", "and d", false},
			{"and", 1, "d)", "(c and d)", "and d", false},
		}},
		{"a and (b and c)", []wantBranch{
			{"and", 0, "a and", "a and", "and (", false},
			{"and", 0, "b and", "(b and c)", "and c", false},
			{"and", 1, "c)", "(b and c)", "and c", false},
		}},
		{"((a and b))", []wantBranch{
			{"and", 0, "a and", "((a and", "and b", false},
			{"and", 1, "b))", "((a and", "and b", false},
		}},

		// not inside a chain
		{"a and not b", []wantBranch{
			{"and", 0, "a and", "a and", "and not", false},
			{"and", 1, "not b", "a and", "and not", true},
		}},
		{"not a and b", []wantBranch{
			{"and", 0, "not a", "not a", "and b", false},
			{"and", 1, "b:", "not a", "and b", false},
		}},
		{"a and not (b or c)", []wantBranch{
			{"and", 0, "a and", "a and", "and not", false},
			{"or", 0, "b or", "(b or c)", "or c", true},
			{"or", 1, "c)", "(b or c)", "or c", true},
		}},
		{"not (a and not b)", []wantBranch{
			{"and", 0, "a and", "(a and", "and not", true},
			{"and", 1, "not b", "(a and", "and not", true}, // "not b" is a value inside the parentheses
		}},

		// comparisons as operands
		{"a == 1 and b not in c or d in e", []wantBranch{
			{"and", 0, "a ==", "a ==", "and b", false},
			{"and", 1, "b not", "a ==", "and b", false}, // compiled as a value: no swap
			{"or", 1, "d in", "a ==", "or d", false},
		}},
	} {
		src := "def f(a, b, c, d, e):\n    if " + test.cond + ":\n        pass\n"
		line := strings.Split(src, "\n")[1]
		fn := function(t, compileSrc(t, src), "f")
		var got, want []string
		for _, b := range fn.Branches {
			got = append(got, renderBranch(b))
		}
		for _, w := range test.want {
			want = append(want, renderWant(line, w))
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("if %s:\n got:\n  %s\nwant:\n  %s", test.cond, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

// TestBranchKinds checks the construct of each condition and its keyword.
func TestBranchKinds(t *testing.T) {
	src := `def f(a, b, xs):
    if a:
        pass
    elif b:
        pass
    else:
        if a:
            pass
    while b:
        pass
    x = 1 if a else 2
    y = [i for i in xs if i]
    if a:
        pass
    else:
        z = 0
        if b:
            pass
`
	lines := strings.Split(src, "\n")
	col := func(line int, needle string) int { return strings.Index(lines[line-1], needle) + 1 }
	type key struct{ line, col int }
	want := map[key]string{
		{2, col(2, "if")}:     "if",
		{4, col(4, "elif")}:   "elif",
		{7, col(7, "if")}:     "if", // "else: if" is not an elif
		{9, col(9, "while")}:  "while",
		{11, col(11, "if")}:   "cond_expr",
		{12, col(12, "if i")}: "comp_if",
		{13, col(13, "if")}:   "if",
		{17, col(17, "if")}:   "if", // not the only statement of the else block
	}
	got := map[key]string{}
	for _, b := range function(t, compileSrc(t, src), "f").Branches {
		got[key{int(b.Keyword.Line), int(b.Keyword.Col)}] = b.Kind.String()
		if b.Operand != -1 || b.Chain != b.Cond {
			t.Errorf("%v: simple condition has Operand %d, Chain %v, Cond %v", b.Kind, b.Operand, b.Chain, b.Cond)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("kinds by keyword position:\n got %v\nwant %v", got, want)
	}
}

// TestBranchCondIsStartOfNode checks that Cond is syntax.Start of the very
// node of the AST that the compiler hands to ifelse, parentheses and "not"
// included: the engine's source map takes the same node.
func TestBranchCondIsStartOfNode(t *testing.T) {
	src := `def f(a, b, xs):
    if (a):
        pass
    if not a:
        pass
    if ((a)):
        pass
    if (a == b):
        pass
    if not (a == b):
        pass
    if a not in xs:
        pass
    while (not a):
        pass
    x = (a) if (b) else not a
    y = [i for i in xs if (i) if not i]
`
	file := "t.star"
	opts := &syntax.FileOptions{While: true}
	f, err := opts.Parse(file, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conds := map[[2]int32]syntax.Expr{} // keyword line, col -> condition
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.IfStmt:
			conds[[2]int32{n.If.Line, n.If.Col}] = n.Cond
		case *syntax.WhileStmt:
			conds[[2]int32{n.While.Line, n.While.Col}] = n.Cond
		case *syntax.CondExpr:
			conds[[2]int32{n.If.Line, n.If.Col}] = n.Cond
		case *syntax.IfClause:
			conds[[2]int32{n.If.Line, n.If.Col}] = n.Cond
		}
		return true
	})
	if len(conds) != 10 {
		t.Fatalf("found %d conditions, want 10", len(conds))
	}
	prog := compileSrc(t, src)
	fn := function(t, prog, "f")
	if len(fn.Branches) != len(conds) {
		t.Fatalf("%d branches for %d conditions", len(fn.Branches), len(conds))
	}
	for _, b := range fn.Branches {
		cond, ok := conds[[2]int32{b.Keyword.Line, b.Keyword.Col}]
		if !ok {
			t.Errorf("branch with keyword %v has no condition", b.Keyword)
			continue
		}
		if want := syntax.Start(cond); b.Cond.Line != want.Line || b.Cond.Col != want.Col {
			t.Errorf("keyword %d:%d: Cond is %d:%d, syntax.Start of the condition is %d:%d",
				b.Keyword.Line, b.Keyword.Col, b.Cond.Line, b.Cond.Col, want.Line, want.Col)
		}
	}
}

// TestBranchOnlyInConditions checks that and/or outside a condition, and
// inside an argument or a lambda of one, have no Branch, and that a Branch is
// always at a CJMP, in address order.
func TestBranchOnlyInConditions(t *testing.T) {
	src := `def value(a, b, c):
    x = a and b
    y = a or b and c
    z = [a and b for _ in range(2)]
    return x, y, z

def arg(f, a, b):
    if f(a and b, lambda: a or b):
        return 1

def after(a, b):
    if a:
        x = a and b
    return a or b
`
	prog := compileSrc(t, src)
	if n := len(function(t, prog, "value").Branches); n != 0 {
		t.Errorf("value contexts have %d branches, want 0", n)
	}
	if n := len(function(t, prog, "arg").Branches); n != 1 {
		t.Errorf("a call in a condition has %d branches, want 1", n)
	}
	if n := len(function(t, prog, "after").Branches); n != 1 {
		t.Errorf("only the if has a branch, got %d", n)
	}
	for _, fn := range prog.Functions {
		for i, b := range fn.Branches {
			if Opcode(fn.Code[b.PC]) != CJMP {
				t.Errorf("%s: branch %d at pc %d is %s, not CJMP", fn.Name, i, b.PC, Opcode(fn.Code[b.PC]))
			}
			if i > 0 && fn.Branches[i-1].PC >= b.PC {
				t.Errorf("%s: branches not sorted by pc", fn.Name)
			}
			if got, ok := fn.BranchAt(b.PC); !ok || got.PC != b.PC {
				t.Errorf("%s: BranchAt(%d) = %v, %v", fn.Name, b.PC, got, ok)
			}
			if _, ok := fn.BranchAt(b.PC + 1); ok {
				t.Errorf("%s: BranchAt(%d) found a branch between instructions", fn.Name, b.PC+1)
			}
		}
	}
}
