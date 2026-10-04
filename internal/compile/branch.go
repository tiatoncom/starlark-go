package compile

// This file builds the branch table of a Funcode: for every conditional jump
// (CJMP) that tests a source-level condition, the position of that condition
// in the source. The table is side data: it adds no instruction and changes
// no byte of Funcode.Code, so Thread.Steps and every budget stay as they were.

import (
	"sort"

	"go.starlark.net/syntax"
)

// A BranchKind says what construct a Branch belongs to.
type BranchKind uint8

const (
	BranchIf       BranchKind = iota // the condition of an if statement
	BranchElif                       // the condition of an elif clause (an IfStmt whose If is the parent's ElsePos); "else: if" is BranchIf
	BranchWhile                      // the condition of a while statement
	BranchCondExpr                   // the condition of a conditional expression: x if cond else y
	BranchCompIf                     // the condition of an if clause of a comprehension
	BranchAnd                        // an operand of an "and" chain inside a condition
	BranchOr                         // an operand of an "or" chain inside a condition
)

var branchKindNames = [...]string{"if", "elif", "while", "cond_expr", "comp_if", "and", "or"}

func (k BranchKind) String() string {
	if int(k) < len(branchKindNames) {
		return branchKindNames[k]
	}
	return "illegal branch kind"
}

// A Branch describes one CJMP of a Funcode: the instruction at PC tests the
// value of a condition, or of one operand of an and/or chain inside one.
//
// A condition that is not an and/or chain has one Branch: Kind is the
// construct's (BranchIf ... BranchCompIf), Operand is -1, Chain equals Cond,
// Keyword is the position of its keyword (if, elif, while, the "if" of a
// conditional expression or of a comprehension clause), and Cond is
// syntax.Start of the very node the compiler hands to ifelse, parentheses
// and a leading "not" included.
//
// A condition that is an and/or chain has one Branch per operand that is
// evaluated by a CJMP of its own. The chain is flat: "a and b and c" has the
// operands a, b, c (numbered from 0, left to right) although the tree is left
// associative. Kind is BranchAnd or BranchOr, Operand is the operand's number,
// Cond is syntax.Start of the operand, Chain is syntax.Start of the whole
// chain and Keyword is the position of the operator that joins the operand
// to the previous one (the first operator for operand 0).
//
// A chain of the other operator, or a chain in parentheses, standing as an
// operand is a chain of its own: operands numbered from 0, its own Chain.
// Chain is the start of the inner expression as it stands as an operand,
// parentheses included, i.e. the Cond of the operand of the outer chain. In
// the outer chain such an inner chain is one operand and has no Branch of its
// own: its outcome is told by the Branches of the inner chain, the one of its
// last operand being the test that the outer chain makes. Under "not" the
// chain starts at the expression after the "not" (parentheses included).
// Examples:
//
//	a and (b or c)  and: a#0, "(b or c)"#1 (no Branch); or (Chain "("): b#0, c#1
//	not (a and b)   and (Chain "(", Negated): a#0, b#1
//	a or b and c    or: a#0, "b and c"#1 (no Branch); and (Chain "b"): b#0, c#1
//
// The operands of and/or outside a condition (x = a and b) have no Branch.
//
// Negated is true when an odd number of "not" / "not in" swaps lies between
// the construct and the CJMP: the jump is then taken to the side that the
// source reads as false. The CJMP jumps when the tested value is true.
type Branch struct {
	PC      uint32 // address of the CJMP instruction
	Kind    BranchKind
	Cond    syntax.Position
	Chain   syntax.Position
	Keyword syntax.Position
	Operand int // number in the flat chain, or -1
	Negated bool
}

// BranchAt returns the Branch of the CJMP at pc, if there is one.
func (fn *Funcode) BranchAt(pc uint32) (*Branch, bool) {
	i := sort.Search(len(fn.Branches), func(i int) bool { return fn.Branches[i].PC >= pc })
	if i < len(fn.Branches) && fn.Branches[i].PC == pc {
		return &fn.Branches[i], true
	}
	return nil, false
}

// branchState is the part of fcomp that builds the branch table.
type branchState struct {
	cond    *condState      // the condition being compiled by ifelse, or nil
	elifAt  syntax.Position // ElsePos of the IfStmt whose else block is being compiled
	table   []Branch        // branches of the instructions emitted so far; insn.br is index+1
	pending uint32          // index+1 in table for the CJMP emitted next, or 0
}

// A condState knows which expressions of one condition are the operands
// (or the whole) tested by a CJMP.
type condState struct {
	leaves []leaf
	neg    bool // parity of "not" / "not in" swaps of ifelse at this point
}

type leaf struct {
	key syntax.Expr // the expression whose value a CJMP tests
	br  Branch
}

// ifelseAt is ifelse for a condition that the table describes.
func (fcomp *fcomp) ifelseAt(kind BranchKind, keyword syntax.Position, cond syntax.Expr, t, f *block) {
	saved := fcomp.cond
	fcomp.cond = newCondState(kind, keyword, cond)
	fcomp.ifelse(cond, t, f)
	fcomp.cond = saved
}

// ifKind returns the kind of the branch of an if statement.
func (fcomp *fcomp) ifKind(stmt *syntax.IfStmt) BranchKind {
	if stmt.If == fcomp.elifAt {
		return BranchElif
	}
	return BranchIf
}

// negate records that ifelse swaps its successors (for "not", "not in").
func (fcomp *fcomp) negate() {
	if fcomp.cond != nil {
		fcomp.cond.neg = !fcomp.cond.neg
	}
}

// markCond notes that the CJMP emitted next tests the value of v.
func (fcomp *fcomp) markCond(v syntax.Expr) {
	cs := fcomp.cond
	if cs == nil {
		return
	}
	if br := cs.find(v); br != nil {
		b := *br
		b.Negated = cs.neg
		fcomp.table = append(fcomp.table, b)
		fcomp.pending = uint32(len(fcomp.table))
	}
}

// markCondNot is markCond for a test whose successors are swapped ("not in").
func (fcomp *fcomp) markCondNot(v syntax.Expr) {
	fcomp.negate()
	fcomp.markCond(v)
	fcomp.negate()
}

// attachBranch gives the CJMP just emitted its pending branch.
func (fcomp *fcomp) attachBranch() {
	if fcomp.pending != 0 {
		insns := fcomp.block.insns
		insns[len(insns)-1].br = fcomp.pending
		fcomp.pending = 0
	}
}

// recordBranch adds the branch of the instruction at pc to the table of fn.
// Instructions are generated in address order, so the table stays sorted.
func (fcomp *fcomp) recordBranch(br uint32, pc uint32) {
	b := fcomp.table[br-1]
	b.PC = pc
	fcomp.fn.Branches = append(fcomp.fn.Branches, b)
}

func newCondState(kind BranchKind, keyword syntax.Position, cond syntax.Expr) *condState {
	cs := new(condState)
	stripped := cond
	for {
		u, ok := stripped.(*syntax.UnaryExpr)
		if !ok || u.Op != syntax.NOT {
			break
		}
		stripped = u.X
	}
	if chain := chainOf(stripped); chain != nil {
		cs.chain(stripped, chain, syntax.Expr(chain) == stripped)
	} else {
		start := syntax.Start(cond)
		cs.leaves = append(cs.leaves, leaf{stripped, Branch{Kind: kind, Cond: start, Chain: start, Keyword: keyword, Operand: -1}})
	}
	return cs
}

// chainOf returns e, with parentheses peeled, if it is an and/or expression.
func chainOf(e syntax.Expr) *syntax.BinaryExpr {
	for {
		switch x := e.(type) {
		case *syntax.ParenExpr:
			e = x.X
			continue
		case *syntax.BinaryExpr:
			if x.Op == syntax.AND || x.Op == syntax.OR {
				return x
			}
		}
		return nil
	}
}

// chain registers the operands of the chain e, which stands as asStands.
// ifelseLevel says whether ifelse itself recurses into the last operand
// (it does not for a chain in parentheses: that is compiled as a value).
func (cs *condState) chain(asStands syntax.Expr, e *syntax.BinaryExpr, ifelseLevel bool) {
	// Flatten the left spine of one operator: ((a and b) and c).
	var operands []syntax.Expr
	var opPos []syntax.Position
	for x := e; ; {
		operands = append(operands, x.Y)
		opPos = append(opPos, x.OpPos)
		if l, ok := x.X.(*syntax.BinaryExpr); ok && l.Op == e.Op {
			x = l
			continue
		}
		operands = append(operands, x.X)
		opPos = append(opPos, x.OpPos)
		break
	}
	n := len(operands)
	start := syntax.Start(asStands)
	kind := BranchAnd
	if e.Op == syntax.OR {
		kind = BranchOr
	}
	for i := 0; i < n; i++ {
		// operands and opPos are in reverse order
		o, pos := operands[n-1-i], opPos[n-1-i]
		last := ifelseLevel && i == n-1
		key := o
		if last { // ifelse strips "not" off the last operand
			for {
				u, ok := key.(*syntax.UnaryExpr)
				if !ok || u.Op != syntax.NOT {
					break
				}
				key = u.X
			}
		}
		if inner := chainOf(key); inner != nil {
			cs.chain(key, inner, last && syntax.Expr(inner) == key)
			continue
		}
		cs.leaves = append(cs.leaves, leaf{key, Branch{Kind: kind, Cond: syntax.Start(o), Chain: start, Keyword: pos, Operand: i}})
	}
}

// find returns the branch of the CJMP that tests the value of v: v itself
// if it is an operand, else the last operand of the chain that v evaluates to.
func (cs *condState) find(v syntax.Expr) *Branch {
	for {
		for i := range cs.leaves {
			if cs.leaves[i].key == v {
				return &cs.leaves[i].br
			}
		}
		switch x := v.(type) {
		case *syntax.ParenExpr:
			v = x.X
		case *syntax.BinaryExpr:
			if x.Op != syntax.AND && x.Op != syntax.OR {
				return nil
			}
			v = x.Y
		default:
			return nil
		}
	}
}
