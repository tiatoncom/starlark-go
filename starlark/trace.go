package starlark

// This file defines the trace hooks of the interpreter: a host can learn which
// branch of a condition a Starlark function took, which comparison decided
// it, and when a Starlark function was entered and left, without any change
// to the bytecode. A hook adds no instruction: Thread.Steps, the step budget
// and Funcode.Code are those of a thread without a Tracer.

import (
	"go.starlark.net/internal/compile"
	"go.starlark.net/syntax"
)

// A Branch describes one conditional jump of a function defined in Starlark:
// which condition, or operand of an and/or chain, the jump tests.
// See doc/trace.md for the exact meaning of its fields.
type Branch = compile.Branch

// A BranchKind says what construct a Branch belongs to.
type BranchKind = compile.BranchKind

const (
	BranchIf       = compile.BranchIf
	BranchElif     = compile.BranchElif
	BranchWhile    = compile.BranchWhile
	BranchCondExpr = compile.BranchCondExpr
	BranchCompIf   = compile.BranchCompIf
	BranchAnd      = compile.BranchAnd
	BranchOr       = compile.BranchOr
)

// A Tracer receives events of the interpreter. Every field may be nil.
//
// Hooks run on the interpreter's goroutine, inside the instruction that
// causes them; they must be quick and must not execute Starlark on the
// thread they observe: Call (and everything built on it) from inside a hook
// panics. Hooks do not count as steps. The Values passed to a hook are the
// interpreter's own: a hook must not mutate them.
//
// A Tracer is set with Thread.Trace; with a nil Trace the interpreter pays
// one comparison per conditional jump, comparison, call and return.
type Tracer struct {
	// OnBranch is called at each conditional jump that tests a condition
	// of the source (see Branch), before the jump is made. tested is the
	// value the jump tests (for an operand of and/or inside parentheses or
	// a longer chain, the value of the operand).
	//
	// taken is "the condition is true": tested.Truth(), the truth of the
	// expression itself whatever "not" the source puts around it. It is not
	// "the jump was made". Branch.Negated says that an odd number of "not"
	// (or "not in") lies between the construct and the expression, so the
	// side of the condition that the source follows is taken XOR Negated:
	// true is the "then" side (or, for an operand of a chain, the operand
	// counts as true for the chain), false the "else" side.
	//
	//	if x:            taken = x       Negated=false   then-side iff taken
	//	if not x:        taken = x       Negated=true    then-side iff !taken
	//	if x not in y:   taken = x in y  Negated=true    then-side iff !taken
	//	if not not x:    taken = x       Negated=false
	//
	// When "not" is evaluated as a value (a "not" in front of a chain that is
	// not the last operand: "not (a and b) and c") the jump sees the opposite
	// truth; the interpreter undoes that, taken stays the truth of the
	// expression and tested, whose value is gone, is Bool(taken).
	//
	// When an and/or operand short-circuits, the later tests of the same
	// chain that the interpreter makes on the same value are not reported
	// (echoes): the chain's last event is the one that decided it.
	OnBranch func(thread *Thread, fn *Function, b Branch, tested Value, taken bool)

	// OnCompare is called after a successful comparison (==, !=, <, <=,
	// >, >=) or membership test (in, not in; op is then syntax.IN, the
	// "not" is Branch.Negated and result the result of "in") whose result
	// is tested by a conditional jump that has a Branch (the table knows the
	// address of the comparison), with that Branch, before OnBranch of
	// the same jump. The place of the comparison among the operands of a
	// chain does not matter. A failed comparison raises an error and calls
	// no hook.
	OnCompare func(thread *Thread, fn *Function, b Branch, op syntax.Token, x, y Value, result bool)

	// OnEnter is called when a function defined in Starlark (or the
	// toplevel of a file: see Function.IsToplevel) starts, after its
	// arguments are bound to its parameters and before its first
	// instruction. A call that is refused before that (recursion, wrong
	// arguments) calls no hook. The args slice is valid until the hook
	// returns; a hook that keeps the arguments must copy it.
	OnEnter func(thread *Thread, fn *Function, call CallSite, args []Arg)

	// OnExit is called when the function returns, or fails with err (an
	// error in the body, the step limit, Cancel), with the result of
	// CallInternal (err is not yet wrapped in an EvalError). It is called
	// for every OnEnter, whichever way the function ends, and is not called
	// when the function is left by a panic.
	OnExit func(thread *Thread, fn *Function, result Value, err error)
}

// A CallSite is where a function defined in Starlark was called from.
type CallSite struct {
	// Pos is the current position of the calling frame: the call
	// expression, when the caller is Starlark code.
	Pos syntax.Position

	// Synthetic is true when the call does not come from a call
	// expression of Starlark code but from a built-in function (such as
	// sorted with a key function) or from Go (the host called Call, or
	// the toplevel of a file starts). Pos is then the position of the
	// built-in frame ("<builtin>") or the zero Position.
	Synthetic bool
}

// An Arg is a parameter of a function and the value bound to it.
type Arg struct {
	Name  string
	Value Value
}

// IsToplevel reports whether fn is the toplevel function of a file (the
// code that Program.Init and ExecFile run), not a def or lambda.
func (fn *Function) IsToplevel() bool { return fn.funcode == fn.funcode.Prog.Toplevel }

// enterHook marks the thread as running a hook, until leaveHook: a hook that
// calls Call panics (see Call).
func (thread *Thread) enterHook() { thread.inTrace = true }

// traceBranch reports the CJMP at pc to OnBranch. tested is the value on top
// of the stack and truth its truth. echo says that the previous traced CJMP
// short-circuited a chain that is compiled as a value, so that a CJMP flagged
// Echo only tests that outcome again. It returns echo for the next CJMP.
func (thread *Thread) traceBranch(fn *Function, pc uint32, tested Value, truth, echo bool) bool {
	tr := thread.Trace
	if tr.OnBranch == nil {
		return false
	}
	f := fn.funcode
	i, ok := f.BranchIndexAt(pc)
	if !ok {
		return false
	}
	b, m := &f.Branches[i], &f.BranchMech[i]
	if !(echo && m.Echo) {
		taken := truth != m.Flip
		if m.Flip {
			tested = Bool(taken)
		}
		thread.enterHook()
		defer thread.leaveHook()
		tr.OnBranch(thread, fn, *b, tested, taken)
	}
	return (m.Short == compile.ShortTruthy && truth) || (m.Short == compile.ShortFalsy && !truth)
}

func (thread *Thread) leaveHook() { thread.inTrace = false }

// traceCompare reports the comparison at pc (operands are its two operands,
// still on the stack just above sp) when a Branch records that the CJMP
// of the Funcode tests its result.
func (thread *Thread) traceCompare(fn *Function, pc uint32, op syntax.Token, operands []Value, result bool) {
	tr := thread.Trace
	if tr.OnCompare == nil {
		return
	}
	b, ok := fn.funcode.BranchForCompare(pc)
	if !ok {
		return
	}
	thread.enterHook()
	defer thread.leaveHook()
	tr.OnCompare(thread, fn, *b, op, operands[0], operands[1], result)
}

// traceEnter reports the start of fn. locals are its locals, parameters first.
func (thread *Thread) traceEnter(fn *Function, locals []Value) {
	tr := thread.Trace
	if tr.OnEnter == nil {
		return
	}
	f := fn.funcode
	// The slice lives in the thread and is reused by the next call: the hook
	// may not keep it (see Tracer.OnEnter).
	args := thread.traceArgs[:0]
	for i := 0; i < f.NumParams; i++ {
		args = append(args, Arg{Name: f.Locals[i].Name, Value: locals[i]})
	}
	thread.traceArgs = args
	var call CallSite
	switch n := len(thread.stack); {
	case n >= 2:
		caller := thread.stack[n-2]
		call.Pos = caller.Position()
		_, fromStarlark := caller.callable.(*Function)
		call.Synthetic = !fromStarlark
	default:
		call.Synthetic = true
	}
	thread.enterHook()
	defer thread.leaveHook()
	tr.OnEnter(thread, fn, call, args)
	clear(args) // do not keep the values alive
}

// traceExit reports the end of fn.
func (thread *Thread) traceExit(fn *Function, result Value, err error) {
	tr := thread.Trace
	if tr.OnExit == nil {
		return
	}
	thread.enterHook()
	defer thread.leaveHook()
	tr.OnExit(thread, fn, result, err)
}
