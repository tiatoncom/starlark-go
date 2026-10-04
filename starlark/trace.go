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
// See compile.Branch for the exact meaning of its fields.
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
	// a longer chain, the value of the operand), taken is tested.Truth():
	// whether the jump is taken. Branch.Negated says whether taking it
	// leads to what the source reads as the false side.
	//
	// When an and/or operand short-circuits, the later tests of the same
	// chain that the interpreter makes on the same value are not reported:
	// the chain's last event is the one that decided it.
	OnBranch func(thread *Thread, fn *Function, b Branch, tested Value, taken bool)

	// OnCompare is called after a successful comparison (==, !=, <, <=,
	// >, >=) or membership test (in, not in; op is then syntax.IN, the
	// "not" is Branch.Negated) whose result is tested by the conditional
	// jump that follows, with that jump's Branch, before OnBranch of the
	// same jump. A failed comparison raises an error and calls no hook.
	OnCompare func(thread *Thread, fn *Function, b Branch, op syntax.Token, x, y Value, result bool)

	// OnEnter is called when a function defined in Starlark (or the
	// toplevel of a file: see Function.IsToplevel) starts, after its
	// arguments are bound to its parameters and before its first
	// instruction. A call that is refused before that (recursion, wrong
	// arguments) calls no hook.
	OnEnter func(thread *Thread, fn *Function, call CallSite, args []Arg)

	// OnExit is called when the function returns, or fails with err,
	// with the result of CallInternal (err is not yet wrapped in an
	// EvalError). It is not called when the function is left by a panic.
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

// enterHook marks the thread as running a hook and returns the function that
// undoes it; a hook that calls Call panics (see Call).
func (thread *Thread) enterHook() { thread.inTrace = true }

// traceBranch reports the CJMP at pc to OnBranch. next is the pc after the
// instruction, arg its target. thread.traceSkip is the CJMP (as pc+1) that
// the previous call decided not to report, 0 for none.
func (thread *Thread) traceBranch(fn *Function, pc, next, arg uint32, tested Value, taken bool) {
	tr := thread.Trace
	skip := thread.traceSkip
	thread.traceSkip = 0
	if tr.OnBranch == nil {
		return
	}
	f := fn.funcode
	b, ok := f.BranchAt(pc)
	if !ok {
		return
	}
	if skip != pc+1 {
		thread.callBranch(tr, fn, *b, tested, taken)
	}
	// A short-circuit of a chain compiled as a value jumps (or falls) to
	// the code that tests the same value again: the test of the operand
	// that closes the chain, and of the chains around it. Those tests
	// repeat what was just reported; do not report them.
	if taken {
		thread.traceSkip = echoAfter(f.Code, arg)
	} else {
		thread.traceSkip = echoAfter(f.Code, next)
	}
}

func (thread *Thread) callBranch(tr *Tracer, fn *Function, b Branch, tested Value, taken bool) {
	thread.enterHook()
	defer thread.leaveHook()
	tr.OnBranch(thread, fn, b, tested, taken)
}

func (thread *Thread) leaveHook() { thread.inTrace = false }

// echoAfter returns, as pc+1, the CJMP that the code at pc reaches through
// JMP, DUP and the NOP padding only, that is the CJMP that tests again the value on top of the
// stack; 0 if the code at pc evaluates anything else first.
func echoAfter(code []byte, pc uint32) uint32 {
	for range 64 {
		switch compile.Opcode(code[pc]) {
		case compile.DUP, compile.NOP: // NOP: the padding of a jump's operand
			pc++
		case compile.JMP:
			pc++
			var target uint32
			for s := uint(0); ; s += 7 {
				b := code[pc]
				pc++
				target |= uint32(b&0x7f) << s
				if b < 0x80 {
					break
				}
			}
			pc = target
		case compile.CJMP:
			return pc + 1
		default:
			return 0
		}
	}
	return 0
}

// traceCompare reports a comparison to OnCompare (operands are its two operands,
// still on the stack just above sp) when the code at pc (the
// instruction after the comparison) is a conditional jump that has a Branch,
// possibly after the DUP that precedes the test of a non-last operand of
// and/or.
func (thread *Thread) traceCompare(fn *Function, pc uint32, op syntax.Token, operands []Value, result bool) {
	tr := thread.Trace
	if tr.OnCompare == nil {
		return
	}
	code := fn.funcode.Code
	if compile.Opcode(code[pc]) == compile.DUP {
		pc++
	}
	if compile.Opcode(code[pc]) != compile.CJMP {
		return
	}
	b, ok := fn.funcode.BranchAt(pc)
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
	var args []Arg
	if f.NumParams > 0 {
		args = make([]Arg, f.NumParams)
		for i := range args {
			args[i] = Arg{Name: f.Locals[i].Name, Value: locals[i]}
		}
	}
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
