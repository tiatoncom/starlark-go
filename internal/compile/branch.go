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
// and a leading "not" included. ChainID and ParentChain are -1.
//
// A condition that is an and/or chain has one Branch per operand that is
// tested by a CJMP of its own. The chain is flat: "a and b and c" has the
// operands a, b, c (numbered from 0, left to right) although the tree is left
// associative. Kind is BranchAnd or BranchOr, Operand is the operand's number,
// Cond is syntax.Start of the operand (its own "not" included), Chain is
// syntax.Start of the whole chain, and Keyword is the position of the
// "and" / "or" that joins the operand to the previous one. Operand 0 has no
// such operator: its Keyword is the zero Position (IsValid reports false).
// The keyword of the construct (if, while ...) belongs to the Branch of the
// construct itself and is not repeated on the operands.
//
// A chain of the other operator, or a chain in parentheses, standing as an
// operand is a chain of its own: operands numbered from 0, its own Chain.
// Chain is the start of the inner expression as it stands as an operand,
// parentheses included, i.e. the Cond of the operand of the outer chain, and
// after a "not" it is the expression after the "not" (parentheses included).
// In the outer chain such an inner chain is one operand and has no Branch of
// its own: its outcome is told by the Branches of the inner chain, the one of
// its last operand being the test that the outer chain makes. Examples:
//
//	a and (b or c)  and: a#0, "(b or c)"#1 (no Branch); or (Chain "("): b#0, c#1
//	not (a and b)   and (Chain "(", Negated): a#0, b#1
//	a or b and c    or: a#0, "b and c"#1 (no Branch); and (Chain "b"): b#0, c#1
//
// Parentheses and "not" are looked through wherever they stand: the Branches
// of "not (a and b) and c", of "(not (a and b)) and c" and of
// "not not not (a and b) and c" are the same but for their positions.
//
// ChainID numbers the chains of the Funcode in the order in which they start
// in the source (a pre-order walk of the syntax tree, an outer chain before
// the chain that starts at the same place inside it), from 0; ParentChain is
// the ChainID of the chain in which this Branch's chain stands as an operand,
// or -1. (Chain, Operand) alone does not tell the operands of two chains that
// start at the same place: "(a and b) and c" has two chains with the same
// Kind and Chain.
//
// The operands of and/or outside a condition (x = a and b) have no Branch.
//
// Negated is true when an odd number of "not" / "not in" lies between the
// construct and the tested expression, wherever the "not" stands (swapped by
// the compiler or evaluated as a value), i.e. when the source reads the jump
// as taking the false side. The hook is given the truth of the expression
// itself: taken = tested.Truth(), and the side of the condition that the
// source follows is taken XOR Negated. The machine may see the tested value
// after one "not"; Flip says so and the interpreter undoes it (see below).
//
// The interpreter's own flags (Echo, Short, Flip, CmpPC; see BranchMech)
// are not part of Branch: they live in Funcode.BranchMech, at the index of
// the Branch in Funcode.Branches, out of the sight of a host.
type Branch struct {
	PC          uint32 // address of the CJMP instruction
	Kind        BranchKind
	Cond        syntax.Position
	Chain       syntax.Position
	Keyword     syntax.Position
	Operand     int // number in the flat chain, or -1
	Negated     bool
	ChainID     int // number of the chain in the Funcode, or -1
	ParentChain int // ChainID of the enclosing chain, or -1
}

// A BranchMech is what the interpreter reads about a Branch instead of
// looking at the code. Funcode.BranchMech[i] belongs to Funcode.Branches[i].
//
//   - Echo: the CJMP tests the value of a chain that was compiled as a value
//     (the instruction after its operands' tests). When a chain is
//     short-circuited its operand's CJMP has just reported the very value
//     this CJMP tests again; the interpreter calls no hook for it then. When
//     the last operand of the chain was evaluated, the CJMP is that
//     operand's only test and is reported.
//   - Short: the CJMP tests an operand that was duplicated (DUP) because its
//     chain is compiled as a value; the outcome that short-circuits the chain
//     (a truthy value for "or", a falsy one for "and") makes the next CJMP an
//     echo.
//   - Flip: an odd number of value-compiled "not" lies between the tested
//     expression and the jump, so the jump sees the opposite truth. The
//     hooks get the truth of the expression, and, as the value of the
//     expression is gone, tested is Bool(taken).
//   - CmpPC: the address of the comparison (==, !=, <, <=, >, >=, in; "not
//     in" is an "in") whose result this CJMP tests, or -1. The interpreter
//     asks BranchForCompare after a comparison.
type BranchMech struct {
	Echo  bool
	Short ShortCircuit
	Flip  bool
	CmpPC int // address of the comparison this CJMP tests, or -1
}

// A ShortCircuit says which value of a duplicated operand short-circuits
// the chain that is compiled as a value.
type ShortCircuit uint8

const (
	ShortNone   ShortCircuit = iota // not such an operand
	ShortTruthy                     // "or": a truthy value ends the chain
	ShortFalsy                      // "and": a falsy value ends the chain
)

// BranchIndexAt returns the index in Branches (and BranchMech) of the CJMP
// at pc, if there is one.
func (fn *Funcode) BranchIndexAt(pc uint32) (int, bool) {
	i := sort.Search(len(fn.Branches), func(i int) bool { return fn.Branches[i].PC >= pc })
	if i < len(fn.Branches) && fn.Branches[i].PC == pc {
		return i, true
	}
	return 0, false
}

// BranchAt returns the Branch of the CJMP at pc, if there is one.
func (fn *Funcode) BranchAt(pc uint32) (*Branch, bool) {
	if i, ok := fn.BranchIndexAt(pc); ok {
		return &fn.Branches[i], true
	}
	return nil, false
}

// BranchForCompare returns the Branch whose CJMP tests the result of the
// comparison at pc, if there is one.
func (fn *Funcode) BranchForCompare(pc uint32) (*Branch, bool) {
	idx := fn.cmpIndex
	i := sort.Search(len(idx), func(i int) bool { return idx[i].cmp >= pc })
	if i < len(idx) && idx[i].cmp == pc {
		return &fn.Branches[idx[i].branch], true
	}
	return nil, false
}

// A cmpRef is an entry of the index of Funcode.Branches by CmpPC.
type cmpRef struct {
	cmp    uint32 // CmpPC
	branch int    // index in Branches
}

// indexBranches builds the index by CmpPC from Branches. The compiler and
// the decoder call it once the table is complete.
func (fn *Funcode) indexBranches() {
	fn.cmpIndex = nil
	for i, b := range fn.BranchMech {
		if b.CmpPC >= 0 {
			fn.cmpIndex = append(fn.cmpIndex, cmpRef{uint32(b.CmpPC), i})
		}
	}
	sort.Slice(fn.cmpIndex, func(i, j int) bool { return fn.cmpIndex[i].cmp < fn.cmpIndex[j].cmp })
}

// branchState is the part of fcomp that builds the branch table.
type branchState struct {
	cond    *condState        // the condition being compiled by ifelse, or nil
	elifAt  syntax.Position   // ElsePos of the IfStmt whose else block is being compiled
	table   []tableEntry      // branches of the instructions emitted so far; insn.br is index+1
	pending uint32            // index+1 in table for the CJMP emitted next, or 0
	chains  []chainInfo       // the chains met so far; Branch.ChainID is an index until the end
	cmp     cmpMark           // the last comparison compiled under a condition
	cmpAt   map[uint32]uint32 // table index+1 -> address of the comparison that feeds it
	emitted []uint32          // table index+1 of each entry of fn.Branches
}

// A tableEntry is a branch and its mechanics before they are split into
// Funcode.Branches and Funcode.BranchMech.
type tableEntry struct {
	br   Branch
	mech BranchMech
}

type chainInfo struct {
	start syntax.Position
	depth int // number of chains around this one
}

// A cmpMark remembers where the last comparison was emitted: the operands
// x, y identify its expression (the copy of "not in" that ifelse makes keeps
// them), blk and idx its instruction.
type cmpMark struct {
	x, y syntax.Expr
	blk  *block
	idx  int
}

// A condState knows which expressions of one condition are the operands
// (or the whole) tested by a CJMP.
type condState struct {
	leaves []leaf
}

type leaf struct {
	key syntax.Expr // the expression whose value a CJMP tests, without "not" and parentheses
	br  Branch
}

// ifelseAt is ifelse for a condition that the table describes.
func (fcomp *fcomp) ifelseAt(kind BranchKind, keyword syntax.Position, cond syntax.Expr, t, f *block) {
	saved := fcomp.cond
	fcomp.cond = fcomp.newCondState(kind, keyword, cond)
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

// markCond notes that the CJMP emitted next tests the value of v.
func (fcomp *fcomp) markCond(v syntax.Expr) { fcomp.mark(v, false, ShortNone) }

// markCondNot is markCond for the "x not in y" that ifelse swaps: the CJMP
// tests the result of "x in y".
func (fcomp *fcomp) markCondNot(v syntax.Expr) { fcomp.mark(v, true, ShortNone) }

// markCondDup is markCond for the operand of the chain "x op y" that is
// compiled as a value: the CJMP follows a DUP.
func (fcomp *fcomp) markCondDup(v syntax.Expr, op syntax.Token) {
	short := ShortFalsy
	if op == syntax.OR {
		short = ShortTruthy
	}
	fcomp.mark(v, false, short)
}

// mark looks through what the compiler evaluates for v to the expression
// the CJMP really tests, and counts the "not" instructions on the way (Flip).
// swapped says that ifelse swapped the successors for a top-level "not in" v:
// the jump sees the result of "in".
func (fcomp *fcomp) mark(v syntax.Expr, swapped bool, short ShortCircuit) {
	cs := fcomp.cond
	if cs == nil {
		return
	}
	var flip, echo bool
	key := v
walk:
	for first := true; ; first = false {
		switch x := key.(type) {
		case *syntax.ParenExpr:
			key = x.X
		case *syntax.UnaryExpr:
			if x.Op != syntax.NOT {
				break walk
			}
			flip = !flip
			key = x.X
		case *syntax.BinaryExpr:
			switch x.Op {
			case syntax.AND, syntax.OR:
				// evaluated as a value: the value of its last operand,
				// possibly the value of an earlier one that short-circuited
				echo = true
				key = x.Y
				continue
			case syntax.NOT_IN:
				if !(first && swapped) {
					flip = !flip
				}
			}
			break walk
		default:
			break walk
		}
	}
	br := cs.find(key)
	if br == nil {
		return
	}
	fcomp.table = append(fcomp.table, tableEntry{
		br:   *br,
		mech: BranchMech{Echo: echo, Short: short, Flip: flip, CmpPC: -1},
	})
	fcomp.pending = uint32(len(fcomp.table))
	if bin, ok := key.(*syntax.BinaryExpr); ok && isComparison(bin.Op) {
		if m := fcomp.cmp; m.blk != nil && m.x == bin.X && m.y == bin.Y {
			m.blk.insns[m.idx].cmp = fcomp.pending
		}
	}
}

func isComparison(op syntax.Token) bool {
	switch op {
	case syntax.EQL, syntax.NEQ, syntax.LT, syntax.LE, syntax.GT, syntax.GE, syntax.IN, syntax.NOT_IN:
		return true
	}
	return false
}

// noteCompare remembers the comparison e, whose instruction is the next one
// emitted to the current block.
func (fcomp *fcomp) noteCompare(e *syntax.BinaryExpr) {
	if fcomp.cond != nil && isComparison(e.Op) {
		fcomp.cmp = cmpMark{e.X, e.Y, fcomp.block, len(fcomp.block.insns)}
	}
}

// attachBranch gives the CJMP just emitted its pending branch.
func (fcomp *fcomp) attachBranch() {
	if fcomp.pending != 0 {
		insns := fcomp.block.insns
		insns[len(insns)-1].br = fcomp.pending
		fcomp.pending = 0
	}
}

// noteCompareAddr is called by generate for the instruction at pc.
func (fcomp *fcomp) noteCompareAddr(br uint32, pc uint32) {
	if fcomp.cmpAt == nil {
		fcomp.cmpAt = make(map[uint32]uint32)
	}
	fcomp.cmpAt[br] = pc
}

// recordBranch adds the branch of the instruction at pc to the table of fn.
// Instructions are generated in address order, so the table stays sorted.
func (fcomp *fcomp) recordBranch(br uint32, pc uint32) {
	e := fcomp.table[br-1]
	e.br.PC = pc
	fcomp.fn.Branches = append(fcomp.fn.Branches, e.br)
	fcomp.fn.BranchMech = append(fcomp.fn.BranchMech, e.mech)
	fcomp.emitted = append(fcomp.emitted, br)
}

// finishBranches gives each branch the address of its comparison (the block
// of the comparison can be laid out after the block of the jump), numbers the
// chains of the function by where they start in the source and builds the
// index by CmpPC. Chains are met in the order in
// which the compiler visits the conditions, which is not the order of the
// source when a conditional expression has conditions of its own in its
// branches.
func (fcomp *fcomp) finishBranches() {
	fn := fcomp.fn
	for i := range fn.Branches {
		if cmp, ok := fcomp.cmpAt[fcomp.emitted[i]]; ok {
			fn.BranchMech[i].CmpPC = int(cmp)
		}
	}
	order := make([]int, len(fcomp.chains))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := fcomp.chains[order[i]], fcomp.chains[order[j]]
		if a.start.Line != b.start.Line {
			return a.start.Line < b.start.Line
		}
		if a.start.Col != b.start.Col {
			return a.start.Col < b.start.Col
		}
		return a.depth < b.depth
	})
	number := make([]int, len(order))
	for rank, id := range order {
		number[id] = rank
	}
	for i := range fn.Branches {
		b := &fn.Branches[i]
		if b.ChainID >= 0 {
			b.ChainID = number[b.ChainID]
		}
		if b.ParentChain >= 0 {
			b.ParentChain = number[b.ParentChain]
		}
	}
	fn.indexBranches()
}

func (fcomp *fcomp) newCondState(kind BranchKind, keyword syntax.Position, cond syntax.Expr) *condState {
	cs := new(condState)
	inner, stands, neg := peel(cond)
	if chain, ok := inner.(*syntax.BinaryExpr); ok && isChainOp(chain.Op) {
		cs.chain(fcomp, stands, chain, neg, -1, 0)
	} else {
		start := syntax.Start(cond)
		cs.leaves = append(cs.leaves, leaf{inner, Branch{
			Kind: kind, Cond: start, Chain: start, Keyword: keyword,
			Operand: -1, Negated: neg != isNotIn(inner), ChainID: -1, ParentChain: -1,
		}})
	}
	return cs
}

func isChainOp(op syntax.Token) bool { return op == syntax.AND || op == syntax.OR }

func isNotIn(e syntax.Expr) bool {
	b, ok := e.(*syntax.BinaryExpr)
	return ok && b.Op == syntax.NOT_IN
}

// peel strips parentheses and "not" off e. It returns the expression left,
// the expression as it stands after the last "not" (e itself if there is
// none), parentheses included, and whether the "not" are odd in number.
func peel(e syntax.Expr) (inner, stands syntax.Expr, odd bool) {
	stands = e
	for {
		switch x := e.(type) {
		case *syntax.ParenExpr:
			e = x.X
			continue
		case *syntax.UnaryExpr:
			if x.Op == syntax.NOT {
				e = x.X
				stands = x.X
				odd = !odd
				continue
			}
		}
		return e, stands, odd
	}
}

// chain registers the operands of the chain e, which stands as asStands
// under an odd number of "not" if neg, inside the chain parent (-1 for none)
// that is depth chains deep.
func (cs *condState) chain(fcomp *fcomp, asStands syntax.Expr, e *syntax.BinaryExpr, neg bool, parent, depth int) {
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
		break
	}
	n := len(operands)
	start := syntax.Start(asStands)
	kind := BranchAnd
	if e.Op == syntax.OR {
		kind = BranchOr
	}
	id := len(fcomp.chains)
	fcomp.chains = append(fcomp.chains, chainInfo{start, depth})
	for i := 0; i < n; i++ {
		// operands and opPos are in reverse order; operand 0 has no operator
		o := operands[n-1-i]
		var keyword syntax.Position
		if i > 0 {
			keyword = opPos[n-1-i]
		}
		inner, stands, odd := peel(o)
		odd = odd != neg
		if c, ok := inner.(*syntax.BinaryExpr); ok && isChainOp(c.Op) {
			cs.chain(fcomp, stands, c, odd, id, depth+1)
			continue
		}
		cs.leaves = append(cs.leaves, leaf{inner, Branch{
			Kind: kind, Cond: syntax.Start(o), Chain: start, Keyword: keyword,
			Operand: i, Negated: odd != isNotIn(inner), ChainID: id, ParentChain: parent,
		}})
	}
}

// find returns the branch of the leaf key.
func (cs *condState) find(key syntax.Expr) *Branch {
	for i := range cs.leaves {
		if cs.leaves[i].key == key {
			return &cs.leaves[i].br
		}
	}
	return nil
}
