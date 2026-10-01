// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package starlark

import (
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"math/bits"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"go.starlark.net/internal/compile"
	"go.starlark.net/internal/spell"
	"go.starlark.net/resolve"
	"go.starlark.net/syntax"
)

// A Thread contains the state of a Starlark thread,
// such as its call stack and thread-local storage.
// The Thread is threaded throughout the evaluator.
type Thread struct {
	// Name is an optional name that describes the thread, for debugging.
	Name string

	// stack is the stack of (internal) call frames.
	stack []*frame

	// Print is the client-supplied implementation of the Starlark
	// 'print' function. If nil, fmt.Fprintln(os.Stderr, msg) is
	// used instead.
	Print func(thread *Thread, msg string)

	// Load is the client-supplied implementation of module loading.
	// Repeated calls with the same module name must return the same
	// module environment or error.
	// The error message need not include the module name.
	//
	// See example_test.go for some example implementations of Load.
	Load func(thread *Thread, module string) (StringDict, error)

	// OnMaxSteps is called when the thread reaches the limit set by SetMaxExecutionSteps.
	// The default behavior is to call thread.Cancel("too many steps").
	OnMaxSteps func(thread *Thread)

	// Steps a count of abstract computation steps executed
	// by this thread. It is incremented by the interpreter. It may be used
	// as a measure of the approximate cost of Starlark execution, by
	// computing the difference in its value before and after a computation.
	//
	// The precise meaning of "step" is not specified and may change.
	//
	// The steps are those of the interpreter alone, as they were: the time of a
	// built-in or an operator is not counted in them but in the work of the
	// thread (work.go).
	Steps, maxSteps uint64

	// userMaxSteps is the limit that SetMaxExecutionSteps set (0: none), and
	// maxSteps the step at which the interpreter looks: the smaller of it and
	// the step at which the work limit is reached (work.go).
	userMaxSteps uint64

	// callDepth is the limit of the depth of the stack of calls set by
	// SetMaxCallStackDepth (0: DefaultMaxCallStackDepth), and active the
	// number of active calls of each function when the stack is deep (see
	// enterFunction).
	callDepth int
	active    map[*compile.Funcode]int32
	scanned   uint64 // frames looked at by enterFunction (for a test)

	// extraWork is the work charged beyond the steps (each of which is a unit
	// of work), and maxWork its limit, 0 if none. See work.go.
	extraWork, maxWork uint64
	workErr            *WorkBudgetError

	// allocated is the number of bytes charged to this thread and
	// maxAllocBytes its budget, 0 if none. See alloc.go.
	allocated, maxAllocBytes uint64

	// cancelReason records the reason from the first call to Cancel.
	cancelReason *string

	// locals holds arbitrary "thread-local" Go values belonging to the client.
	// They are accessible to the client but not to any Starlark program.
	locals map[string]any

	// proftime holds the accumulated execution time since the last profile event.
	proftime time.Duration
}

// ExecutionSteps returns the current value of Steps.
func (thread *Thread) ExecutionSteps() uint64 {
	return thread.Steps
}

// SetMaxExecutionSteps sets a limit on the number of Starlark
// computation steps that may be executed by this thread. If the
// thread's step counter exceeds this limit, the interpreter calls
// the optional OnMaxSteps function or the default behavior
// of calling thread.Cancel("too many steps").
func (thread *Thread) SetMaxExecutionSteps(max uint64) {
	thread.userMaxSteps = max
	thread.regate()
}

// Uncancel resets the cancellation state.
//
// Unlike most methods of Thread, it is safe to call Uncancel from any
// goroutine, even if the thread is actively executing.
func (thread *Thread) Uncancel() {
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(&thread.cancelReason)), nil)
}

// Cancel causes execution of Starlark code in the specified thread to
// promptly fail with an EvalError that includes the specified reason.
// There may be a delay before the interpreter observes the cancellation
// if the thread is currently in a call to a built-in function.
//
// Call [Uncancel] to reset the cancellation state.
//
// Unlike most methods of Thread, it is safe to call Cancel from any
// goroutine, even if the thread is actively executing.
func (thread *Thread) Cancel(reason string) {
	// Atomically set cancelReason, preserving earlier reason if any.
	atomic.CompareAndSwapPointer((*unsafe.Pointer)(unsafe.Pointer(&thread.cancelReason)), nil, unsafe.Pointer(&reason))
}

// SetLocal sets the thread-local value associated with the specified key.
// It must not be called after execution begins.
func (thread *Thread) SetLocal(key string, value any) {
	if thread.locals == nil {
		thread.locals = make(map[string]any)
	}
	thread.locals[key] = value
}

// Local returns the thread-local value associated with the specified key.
func (thread *Thread) Local(key string) any {
	return thread.locals[key]
}

// CallFrame returns a copy of the specified frame of the callstack.
// It should only be used in built-ins called from Starlark code.
// Depth 0 means the frame of the built-in itself, 1 is its caller, and so on.
//
// It is equivalent to CallStack().At(depth), but more efficient.
func (thread *Thread) CallFrame(depth int) CallFrame {
	return thread.frameAt(depth).asCallFrame()
}

func (thread *Thread) frameAt(depth int) *frame {
	return thread.stack[len(thread.stack)-1-depth]
}

// CallStack returns a new slice containing the thread's stack of call frames.
func (thread *Thread) CallStack() CallStack {
	frames := make([]CallFrame, len(thread.stack))
	for i, fr := range thread.stack {
		frames[i] = fr.asCallFrame()
	}
	return frames
}

// CallStackDepth returns the number of frames in the current call stack.
func (thread *Thread) CallStackDepth() int { return len(thread.stack) }

// A StringDict is a mapping from names to values, and represents
// an environment such as the global variables of a module.
// It is not a true starlark.Value.
type StringDict map[string]Value

// Keys returns a new sorted slice of d's keys.
func (d StringDict) Keys() []string {
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (d StringDict) String() string {
	buf := new(strings.Builder)
	buf.WriteByte('{')
	sep := ""
	for _, name := range d.Keys() {
		buf.WriteString(sep)
		buf.WriteString(name)
		buf.WriteString(": ")
		writeValue(buf, d[name], nil)
		sep = ", "
	}
	buf.WriteByte('}')
	return buf.String()
}

func (d StringDict) Freeze() {
	for _, v := range d {
		v.Freeze()
	}
}

// Has reports whether the dictionary contains the specified key.
func (d StringDict) Has(key string) bool { _, ok := d[key]; return ok }

// A frame records a call to a Starlark function (including module toplevel)
// or a built-in function or method.
type frame struct {
	callable  Callable // current function (or toplevel) or built-in
	pc        uint32   // program counter (Starlark frames only)
	locals    []Value  // local variables (Starlark frames only)
	spanStart int64    // start time of current profiler span
}

// Position returns the source position of the current point of execution in this frame.
func (fr *frame) Position() syntax.Position {
	switch c := fr.callable.(type) {
	case *Function:
		// Starlark function
		return c.funcode.Position(fr.pc)
	case callableWithPosition:
		// If a built-in Callable defines
		// a Position method, use it.
		return c.Position()
	}
	return syntax.MakePosition(&builtinFilename, 0, 0)
}

var builtinFilename = "<builtin>"

// Function returns the frame's function or built-in.
func (fr *frame) Callable() Callable { return fr.callable }

// A CallStack is a stack of call frames, outermost first.
type CallStack []CallFrame

// At returns a copy of the frame at depth i.
// At(0) returns the topmost frame.
func (stack CallStack) At(i int) CallFrame { return stack[len(stack)-1-i] }

// Pop removes and returns the topmost frame.
func (stack *CallStack) Pop() CallFrame {
	last := len(*stack) - 1
	top := (*stack)[last]
	*stack = (*stack)[:last]
	return top
}

// String returns a user-friendly description of the stack.
func (stack CallStack) String() string {
	out := new(strings.Builder)
	if len(stack) > 0 {
		fmt.Fprintf(out, "Traceback (most recent call last):\n")
	}
	for _, fr := range stack {
		fmt.Fprintf(out, "  %s: in %s\n", fr.Pos, fr.Name)
	}
	return out.String()
}

// An EvalError is a Starlark evaluation error and
// a copy of the thread's stack at the moment of the error.
type EvalError struct {
	Msg       string
	CallStack CallStack
	cause     error
}

// A CallFrame represents the function name and current
// position of execution of an enclosing call frame.
type CallFrame struct {
	Name string
	Pos  syntax.Position
}

func (fr *frame) asCallFrame() CallFrame {
	return CallFrame{
		Name: fr.Callable().Name(),
		Pos:  fr.Position(),
	}
}

func (thread *Thread) evalError(err error) *EvalError {
	return &EvalError{
		Msg:       err.Error(),
		CallStack: thread.CallStack(),
		cause:     err,
	}
}

func (e *EvalError) Error() string { return e.Msg }

// Backtrace returns a user-friendly error message describing the stack
// of calls that led to this error.
func (e *EvalError) Backtrace() string {
	// If the topmost stack frame is a built-in function,
	// remove it from the stack and add print "Error in fn:".
	stack := e.CallStack
	suffix := ""
	if last := len(stack) - 1; last >= 0 && stack[last].Pos.Filename() == builtinFilename {
		suffix = " in " + stack[last].Name
		stack = stack[:last]
	}
	return fmt.Sprintf("%sError%s: %s", stack, suffix, e.Msg)
}

func (e *EvalError) Unwrap() error { return e.cause }

// A Program is a compiled Starlark program.
//
// Programs are immutable, and contain no Values.
// A Program may be created by parsing a source file (see SourceProgram)
// or by loading a previously saved compiled program (see CompiledProgram).
type Program struct {
	compiled *compile.Program
}

// CompilerVersion is the version number of the protocol for compiled
// files. Applications must not run programs compiled by one version
// with an interpreter at another version, and should thus incorporate
// the compiler version into the cache key when reusing compiled code.
const CompilerVersion = compile.Version

// Filename returns the name of the file from which this program was loaded.
func (prog *Program) Filename() string { return prog.compiled.Toplevel.Pos.Filename() }

func (prog *Program) String() string { return prog.Filename() }

// NumLoads returns the number of load statements in the compiled program.
func (prog *Program) NumLoads() int { return len(prog.compiled.Loads) }

// Load(i) returns the name and position of the i'th module directly
// loaded by this one, where 0 <= i < NumLoads().
// The name is unresolved---exactly as it appears in the source.
func (prog *Program) Load(i int) (string, syntax.Position) {
	id := prog.compiled.Loads[i]
	return id.Name, id.Pos
}

// WriteTo writes the compiled module to the specified output stream.
func (prog *Program) Write(out io.Writer) error {
	data := prog.compiled.Encode()
	_, err := out.Write(data)
	return err
}

// ExecFile calls [ExecFileOptions] using [syntax.LegacyFileOptions].
//
// Deprecated: use [ExecFileOptions] with [syntax.FileOptions] instead,
// because this function relies on legacy global variables.
func ExecFile(thread *Thread, filename string, src any, predeclared StringDict) (StringDict, error) {
	return ExecFileOptions(syntax.LegacyFileOptions(), thread, filename, src, predeclared)
}

// ExecFileOptions parses, resolves, and executes a Starlark file in the
// specified global environment, which may be modified during execution.
//
// Thread is the state associated with the Starlark thread.
//
// The filename and src parameters are as for syntax.Parse:
// filename is the name of the file to execute,
// and the name that appears in error messages;
// src is an optional source of bytes to use
// instead of filename.
//
// predeclared defines the predeclared names specific to this module.
// Execution does not modify this dictionary, though it may mutate
// its values.
//
// If ExecFileOptions fails during evaluation, it returns an *EvalError
// containing a backtrace.
func ExecFileOptions(opts *syntax.FileOptions, thread *Thread, filename string, src any, predeclared StringDict) (StringDict, error) {
	// Parse, resolve, and compile a Starlark source file.
	_, mod, err := SourceProgramOptions(opts, filename, src, predeclared.Has)
	if err != nil {
		return nil, err
	}

	g, err := mod.Init(thread, predeclared)
	g.Freeze()
	return g, err
}

// SourceProgram calls [SourceProgramOptions] using [syntax.LegacyFileOptions].
//
// Deprecated: use [SourceProgramOptions] with [syntax.FileOptions] instead,
// because this function relies on legacy global variables.
func SourceProgram(filename string, src any, isPredeclared func(string) bool) (*syntax.File, *Program, error) {
	return SourceProgramOptions(syntax.LegacyFileOptions(), filename, src, isPredeclared)
}

// SourceProgramOptions produces a new program by parsing, resolving,
// and compiling a Starlark source file.
// On success, it returns the parsed file and the compiled program.
// The filename and src parameters are as for syntax.Parse.
//
// The isPredeclared predicate reports whether a name is
// a pre-declared identifier of the current module.
// Its typical value is predeclared.Has,
// where predeclared is a StringDict of pre-declared values.
func SourceProgramOptions(opts *syntax.FileOptions, filename string, src any, isPredeclared func(string) bool) (*syntax.File, *Program, error) {
	f, err := opts.Parse(filename, src, 0)
	if err != nil {
		return nil, nil, err
	}
	prog, err := FileProgram(f, isPredeclared)
	return f, prog, err
}

// FileProgram produces a new program by resolving,
// and compiling the Starlark source file syntax tree.
// On success, it returns the compiled program.
//
// Resolving a syntax tree mutates it.
// Do not call FileProgram more than once on the same file.
//
// The isPredeclared predicate reports whether a name is
// a pre-declared identifier of the current module.
// Its typical value is predeclared.Has,
// where predeclared is a StringDict of pre-declared values.
func FileProgram(f *syntax.File, isPredeclared func(string) bool) (*Program, error) {
	if err := resolve.File(f, isPredeclared, Universe.Has); err != nil {
		return nil, err
	}

	var pos syntax.Position
	if len(f.Stmts) > 0 {
		pos = syntax.Start(f.Stmts[0])
	} else {
		pos = syntax.MakePosition(&f.Path, 1, 1)
	}

	module := f.Module.(*resolve.Module)
	compiled := compile.File(f.Options, f.Stmts, pos, "<toplevel>", module.Locals, module.Globals)

	return &Program{compiled}, nil
}

// CompiledProgram produces a new program from the representation
// of a compiled program previously saved by Program.Write.
func CompiledProgram(in io.Reader) (*Program, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	compiled, err := compile.DecodeProgram(data)
	if err != nil {
		return nil, err
	}
	return &Program{compiled}, nil
}

// Init creates a set of global variables for the program,
// executes the toplevel code of the specified program,
// and returns a new, unfrozen dictionary of the globals.
func (prog *Program) Init(thread *Thread, predeclared StringDict) (StringDict, error) {
	toplevel := makeToplevelFunction(prog, predeclared)

	_, err := Call(thread, toplevel, nil, nil)

	// Convert the global environment to a map.
	// We return a (partial) map even in case of error.
	return toplevel.Globals(), err
}

// ExecREPLChunk compiles and executes file f in the specified thread
// and global environment. This is a variant of ExecFile specialized to
// the needs of a REPL, in which a sequence of input chunks, each
// syntactically a File, manipulates the same set of module globals,
// which are not frozen after execution.
//
// This function is intended to support only go.starlark.net/repl.
// Its API stability is not guaranteed.
func ExecREPLChunk(f *syntax.File, thread *Thread, globals StringDict) error {
	var predeclared StringDict

	// -- variant of FileProgram --

	if err := resolve.REPLChunk(f, globals.Has, predeclared.Has, Universe.Has); err != nil {
		return err
	}

	var pos syntax.Position
	if len(f.Stmts) > 0 {
		pos = syntax.Start(f.Stmts[0])
	} else {
		pos = syntax.MakePosition(&f.Path, 1, 1)
	}

	module := f.Module.(*resolve.Module)
	compiled := compile.File(f.Options, f.Stmts, pos, "<toplevel>", module.Locals, module.Globals)
	prog := &Program{compiled}

	// -- variant of Program.Init --

	toplevel := makeToplevelFunction(prog, predeclared)

	// Initialize module globals from parameter.
	for i, id := range prog.compiled.Globals {
		if v := globals[id.Name]; v != nil {
			toplevel.module.globals[i] = v
		}
	}

	_, err := Call(thread, toplevel, nil, nil)

	// Reflect changes to globals back to parameter, even after an error.
	for i, id := range prog.compiled.Globals {
		if v := toplevel.module.globals[i]; v != nil {
			globals[id.Name] = v
		}
	}

	return err
}

func makeToplevelFunction(prog *Program, predeclared StringDict) *Function {
	// Create the Starlark value denoted by each program constant c.
	constants := make([]Value, len(prog.compiled.Constants))
	for i, c := range prog.compiled.Constants {
		var v Value
		switch c := c.(type) {
		case int64:
			v = MakeInt64(c)
		case *big.Int:
			v = MakeBigInt(c)
		case string:
			v = String(c)
		case compile.Bytes:
			v = Bytes(c)
		case float64:
			v = Float(c)
		default:
			log.Panicf("unexpected constant %T: %v", c, c)
		}
		constants[i] = v
	}

	return &Function{
		funcode: prog.compiled.Toplevel,
		module: &Module{
			program:     prog,
			predeclared: predeclared,
			globals:     make([]Value, len(prog.compiled.Globals)),
			constants:   constants,
		},
	}
}

// Eval calls [EvalOptions] using [syntax.LegacyFileOptions].
//
// Deprecated: use [EvalOptions] with [syntax.FileOptions] instead,
// because this function relies on legacy global variables.
func Eval(thread *Thread, filename string, src any, env StringDict) (Value, error) {
	return EvalOptions(syntax.LegacyFileOptions(), thread, filename, src, env)
}

// EvalOptions parses, resolves, and evaluates an expression within the
// specified (predeclared) environment.
//
// Evaluation cannot mutate the environment dictionary itself,
// though it may modify variables reachable from the dictionary.
//
// The filename and src parameters are as for syntax.Parse.
//
// If EvalOptions fails during evaluation, it returns an *EvalError
// containing a backtrace.
func EvalOptions(opts *syntax.FileOptions, thread *Thread, filename string, src any, env StringDict) (Value, error) {
	expr, err := opts.ParseExpr(filename, src, 0)
	if err != nil {
		return nil, err
	}
	f, err := makeExprFunc(opts, expr, env)
	if err != nil {
		return nil, err
	}
	return Call(thread, f, nil, nil)
}

// EvalExpr calls [EvalExprOptions] using [syntax.LegacyFileOptions].
//
// Deprecated: use [EvalExprOptions] with [syntax.FileOptions] instead,
// because this function relies on legacy global variables.
func EvalExpr(thread *Thread, expr syntax.Expr, env StringDict) (Value, error) {
	return EvalExprOptions(syntax.LegacyFileOptions(), thread, expr, env)
}

// EvalExprOptions resolves and evaluates an expression within the
// specified (predeclared) environment.
// Evaluating a comma-separated list of expressions yields a tuple value.
//
// Resolving an expression mutates it.
// Do not call EvalExprOptions more than once for the same expression.
//
// Evaluation cannot mutate the environment dictionary itself,
// though it may modify variables reachable from the dictionary.
//
// If EvalExprOptions fails during evaluation, it returns an *EvalError
// containing a backtrace.
func EvalExprOptions(opts *syntax.FileOptions, thread *Thread, expr syntax.Expr, env StringDict) (Value, error) {
	fn, err := makeExprFunc(opts, expr, env)
	if err != nil {
		return nil, err
	}
	return Call(thread, fn, nil, nil)
}

// ExprFunc calls [ExprFuncOptions] using [syntax.LegacyFileOptions].
//
// Deprecated: use [ExprFuncOptions] with [syntax.FileOptions] instead,
// because this function relies on legacy global variables.
func ExprFunc(filename string, src any, env StringDict) (*Function, error) {
	return ExprFuncOptions(syntax.LegacyFileOptions(), filename, src, env)
}

// ExprFunc returns a no-argument function
// that evaluates the expression whose source is src.
func ExprFuncOptions(options *syntax.FileOptions, filename string, src any, env StringDict) (*Function, error) {
	expr, err := options.ParseExpr(filename, src, 0)
	if err != nil {
		return nil, err
	}
	return makeExprFunc(options, expr, env)
}

// makeExprFunc returns a no-argument function whose body is expr.
// The options must be consistent with those used when parsing expr.
func makeExprFunc(opts *syntax.FileOptions, expr syntax.Expr, env StringDict) (*Function, error) {
	locals, err := resolve.ExprOptions(opts, expr, env.Has, Universe.Has)
	if err != nil {
		return nil, err
	}

	prog := compile.Expr(opts, expr, "<expr>", locals)
	return makeToplevelFunction(&Program{prog}, env), nil
}

// The following functions are primitive operations of the byte code interpreter.

// list += iterable
//
// The elements added are charged to the thread's allocation budget, and the
// list may not grow to maxAlloc elements: repeated x += x doubles the list
// in a single step.
func listExtend(thread *Thread, x *List, y Iterable) error {
	if n := Len(y); n >= 0 {
		// Known length (fast path for list += list): check before growing.
		if err := thread.charge(listBytes(len(x.elems)+n), satMul(uint64(n), allocBytesPerValue)); err != nil {
			return excess(err, "excessive list extension (%d + %d elements)", len(x.elems), n)
		}
		if ylist, ok := y.(*List); ok {
			x.elems = append(x.elems, ylist.elems...)
			return nil
		}
	}
	// Unknown length (or a known length that is not a list): charge as the
	// elements are produced, so that an endless iterator cannot outrun the
	// limits. A known length was charged above.
	known := Len(y) >= 0
	iter := y.Iterate()
	defer iter.Done()
	m := thread.meter() // one unit for each element whose number was not known
	var z Value
	for iter.Next(&z) {
		if !known {
			if err := m.add(1); err != nil {
				return err
			}
			if err := thread.chargeOne(len(x.elems), allocBytesPerNewValue); err != nil {
				return excess(err, "excessive list extension (over %d elements)", maxAlloc)
			}
		}
		x.elems = append(x.elems, z)
	}
	return m.flush()
}

// getAttr implements x.dot.
func getAttr(x Value, name string) (Value, error) {
	hasAttr, ok := x.(HasAttrs)
	if !ok {
		return nil, fmt.Errorf("%s has no .%s field or method", x.Type(), errStr(name))
	}

	var errmsg string
	v, err := hasAttr.Attr(name)
	if err == nil {
		if v != nil {
			return v, nil // success
		}
		// (nil, nil) => generic error
		errmsg = fmt.Sprintf("%s has no .%s field or method", x.Type(), errStr(name))
	} else if nsa, ok := err.(NoSuchAttrError); ok {
		errmsg = string(nsa)
	} else {
		return nil, err // return error as is
	}

	// add spelling hint
	if n := spell.Nearest(errStr(name), hasAttr.AttrNames()); n != "" {
		errmsg = fmt.Sprintf("%s (did you mean .%s?)", errmsg, n)
	}

	return nil, fmt.Errorf("%s", errmsg)
}

// setField implements x.name = y.
func setField(x Value, name string, y Value) error {
	if x, ok := x.(HasSetField); ok {
		err := x.SetField(name, y)
		if _, ok := err.(NoSuchAttrError); ok {
			// No such field: check spelling.
			if n := spell.Nearest(errStr(name), x.AttrNames()); n != "" {
				err = fmt.Errorf("%s (did you mean .%s?)", err, n)
			}
		}
		return err
	}

	return fmt.Errorf("can't assign to .%s field of %s", name, x.Type())
}

// getIndex implements x[y].
func getIndex(thread *Thread, x, y Value) (Value, error) {
	switch x := x.(type) {
	case Mapping: // dict
		var z Value
		var found bool
		var err error
		if d, ok := x.(*Dict); ok {
			m := thread.meter() // the hash of the key, the chain of the bucket
			z, found, err = d.getM(&m, y)
			if err == nil {
				err = m.flush()
			}
		} else {
			z, found, err = x.Get(y)
		}
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("key %s not in %s", errValue(y), x.Type())
		}
		return z, nil

	case Indexable: // string, list, tuple
		n := x.Len()
		i, err := AsInt32(y)
		if err != nil {
			return nil, fmt.Errorf("%s index: %s", x.Type(), err)
		}
		origI := i
		if i < 0 {
			i += n
		}
		if i < 0 || i >= n {
			return nil, outOfRange(origI, n, x)
		}
		return x.Index(i), nil
	}
	return nil, fmt.Errorf("unhandled index operation %s[%s]", x.Type(), y.Type())
}

func outOfRange(i, n int, x Value) error {
	if n == 0 {
		return fmt.Errorf("index %d out of range: empty %s", i, x.Type())
	} else {
		return fmt.Errorf("%s index %d out of range [%d:%d]", x.Type(), i, -n, n-1)
	}
}

// setIndex implements x[y] = z.
func setIndex(x, y, z Value) error {
	switch x := x.(type) {
	case HasSetKey:
		if err := x.SetKey(y, z); err != nil {
			return err
		}

	case HasSetIndex:
		n := x.Len()
		i, err := AsInt32(y)
		if err != nil {
			return err
		}
		origI := i
		if i < 0 {
			i += n
		}
		if i < 0 || i >= n {
			return outOfRange(origI, n, x)
		}
		return x.SetIndex(i, z)

	default:
		return fmt.Errorf("%s value does not support item assignment", x.Type())
	}
	return nil
}

// Unary applies a unary operator (+, -, ~, not) to its operand.
func Unary(op syntax.Token, x Value) (Value, error) {
	// The NOT operator is not customizable.
	if op == syntax.NOT {
		return !x.Truth(), nil
	}

	// Int, Float, and user-defined types
	if x, ok := x.(HasUnary); ok {
		// (nil, nil) => unhandled
		y, err := x.Unary(op)
		if y != nil || err != nil {
			return y, err
		}
	}

	return nil, fmt.Errorf("unknown unary op: %s %s", op, x.Type())
}

// hasElems reports whether x is equal to an element of elems, charging the
// comparisons to m a chunk at a time, before the chunk is scanned (x in l is
// linear in l, and stops at the first match).
func hasElems(m *meter, elems []Value, x Value) (bool, error) {
	const chunk = 256
	for i := 0; i < len(elems); {
		end := min(i+chunk, len(elems))
		if err := m.add(uint64(end - i)); err != nil {
			return false, err
		}
		for ; i < end; i++ {
			eq, err := equalFast(m, elems[i], x, CompareLimit)
			if err != nil {
				return false, err
			}
			if eq {
				return true, m.flush()
			}
		}
	}
	return false, m.flush()
}

// Binary applies a strict binary operator (not AND or OR) to its operands.
// For equality tests or ordered comparisons, use Compare instead.
//
// Binary has no thread: the size of its result is bounded by the ceiling of
// one operation (maxAlloc) but is not charged to any budget. The interpreter
// uses binaryOp, which charges the thread it runs on.
func Binary(op syntax.Token, x, y Value) (Value, error) {
	return binaryOp(nil, op, x, y)
}

// binaryOp implements Binary, charging the results that allocate to thread
// (which may be nil: then only the ceiling applies).
func binaryOp(thread *Thread, op syntax.Token, x, y Value) (Value, error) {
	switch op {
	case syntax.PLUS:
		switch x := x.(type) {
		case String:
			if y, ok := y.(String); ok {
				// Bound concatenation like repeat (maxAlloc). Repeated
				// s = s + s doubles the string in a single step.
				if err := thread.chargeBytes(len(x) + len(y)); err != nil {
					return nil, excess(err, "excessive string concatenation (%d + %d bytes)", len(x), len(y))
				}
				return x + y, nil
			}
		case Int:
			switch y := y.(type) {
			case Int:
				if err := thread.chargeIntLinear(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(wordsAdd(x, y)); err != nil {
					return nil, excess(err, "excessive integer addition")
				}
				return thread.intDone(x.Add(y))
			case Float:
				xf, err := x.finiteFloat()
				if err != nil {
					return nil, err
				}
				return xf + y, nil
			}
		case Float:
			switch y := y.(type) {
			case Float:
				return x + y, nil
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				return x + yf, nil
			}
		case *List:
			if y, ok := y.(*List); ok {
				// Bound concatenation like repeat (maxAlloc). Repeated
				// x = x + x doubles the list in a single step.
				if err := thread.chargeValues(x.Len() + y.Len()); err != nil {
					return nil, excess(err, "excessive list concatenation (%d + %d elements)", x.Len(), y.Len())
				}
				z := make([]Value, 0, x.Len()+y.Len())
				z = append(z, x.elems...)
				z = append(z, y.elems...)
				return NewList(z), nil
			}
		case Tuple:
			if y, ok := y.(Tuple); ok {
				// Bound concatenation like repeat (maxAlloc).
				if err := thread.chargeTuple(len(x) + len(y)); err != nil {
					return nil, excess(err, "excessive tuple concatenation (%d + %d elements)", len(x), len(y))
				}
				z := make(Tuple, 0, len(x)+len(y))
				z = append(z, x...)
				z = append(z, y...)
				return z, nil
			}
		}

	case syntax.MINUS:
		switch x := x.(type) {
		case Int:
			switch y := y.(type) {
			case Int:
				if err := thread.chargeIntLinear(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(wordsAdd(x, y)); err != nil {
					return nil, excess(err, "excessive integer subtraction")
				}
				return thread.intDone(x.Sub(y))
			case Float:
				xf, err := x.finiteFloat()
				if err != nil {
					return nil, err
				}
				return xf - y, nil
			}
		case Float:
			switch y := y.(type) {
			case Float:
				return x - y, nil
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				return x - yf, nil
			}
		case *Set: // difference
			if y, ok := y.(*Set); ok {
				if err := thread.roomEntries(x.Len()); err != nil {
					return nil, excess(err, "excessive set difference (%d elements)", x.Len())
				}
				if err := thread.chargeEntries(0); err != nil {
					return nil, excess(err, "excessive set difference (%d elements)", x.Len())
				}
				iter := y.Iterate()
				defer iter.Done()
				m := thread.meter()
				z, err := x.differenceM(&m, iter)
				return z, thread.finishGrowth(0, &m, err)
			}
		}

	case syntax.STAR:
		switch x := x.(type) {
		case Int:
			switch y := y.(type) {
			case Int:
				// A product of big integers is as long as both together.
				if err := thread.intRoom(wordsMul(x, y)); err != nil {
					return nil, excess(err, "excessive integer multiplication")
				}
				if err := thread.chargeIntQuadratic(x, y); err != nil {
					return nil, err
				}
				return thread.intDone(x.Mul(y))
			case Float:
				xf, err := x.finiteFloat()
				if err != nil {
					return nil, err
				}
				return xf * y, nil
			case String:
				return stringRepeat(thread, y, x)
			case Bytes:
				return bytesRepeat(thread, y, x)
			case *List:
				elems, err := tupleRepeat(thread, Tuple(y.elems), x, true)
				if err != nil {
					return nil, err
				}
				return NewList(elems), nil
			case Tuple:
				return tupleRepeat(thread, y, x, false)
			}
		case Float:
			switch y := y.(type) {
			case Float:
				return x * y, nil
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				return x * yf, nil
			}
		case String:
			if y, ok := y.(Int); ok {
				return stringRepeat(thread, x, y)
			}
		case Bytes:
			if y, ok := y.(Int); ok {
				return bytesRepeat(thread, x, y)
			}
		case *List:
			if y, ok := y.(Int); ok {
				elems, err := tupleRepeat(thread, Tuple(x.elems), y, true)
				if err != nil {
					return nil, err
				}
				return NewList(elems), nil
			}
		case Tuple:
			if y, ok := y.(Int); ok {
				return tupleRepeat(thread, x, y, false)
			}

		}

	case syntax.SLASH:
		switch x := x.(type) {
		case Int:
			xf, err := x.finiteFloat()
			if err != nil {
				return nil, err
			}
			switch y := y.(type) {
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				if yf == 0.0 {
					return nil, fmt.Errorf("floating-point division by zero")
				}
				return xf / yf, nil
			case Float:
				if y == 0.0 {
					return nil, fmt.Errorf("floating-point division by zero")
				}
				return xf / y, nil
			}
		case Float:
			switch y := y.(type) {
			case Float:
				if y == 0.0 {
					return nil, fmt.Errorf("floating-point division by zero")
				}
				return x / y, nil
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				if yf == 0.0 {
					return nil, fmt.Errorf("floating-point division by zero")
				}
				return x / yf, nil
			}
		}

	case syntax.SLASHSLASH:
		switch x := x.(type) {
		case Int:
			switch y := y.(type) {
			case Int:
				if y.Sign() == 0 {
					return nil, fmt.Errorf("floored division by zero")
				}
				if err := thread.chargeIntQuadratic(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(bigWords(x)); err != nil { // the quotient is not longer than x
					return nil, excess(err, "excessive integer division")
				}
				return thread.intDone(x.Div(y))
			case Float:
				xf, err := x.finiteFloat()
				if err != nil {
					return nil, err
				}
				if y == 0.0 {
					return nil, fmt.Errorf("floored division by zero")
				}
				return floor(xf / y), nil
			}
		case Float:
			switch y := y.(type) {
			case Float:
				if y == 0.0 {
					return nil, fmt.Errorf("floored division by zero")
				}
				return floor(x / y), nil
			case Int:
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				if yf == 0.0 {
					return nil, fmt.Errorf("floored division by zero")
				}
				return floor(x / yf), nil
			}
		}

	case syntax.PERCENT:
		switch x := x.(type) {
		case Int:
			switch y := y.(type) {
			case Int:
				if y.Sign() == 0 {
					return nil, fmt.Errorf("integer modulo by zero")
				}
				if err := thread.chargeIntQuadratic(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(bigWords(y)); err != nil { // the remainder is shorter than y
					return nil, excess(err, "excessive integer modulo")
				}
				return thread.intDone(x.Mod(y))
			case Float:
				xf, err := x.finiteFloat()
				if err != nil {
					return nil, err
				}
				if y == 0 {
					return nil, fmt.Errorf("floating-point modulo by zero")
				}
				return xf.Mod(y), nil
			}
		case Float:
			switch y := y.(type) {
			case Float:
				if y == 0.0 {
					return nil, fmt.Errorf("floating-point modulo by zero")
				}
				return x.Mod(y), nil
			case Int:
				if y.Sign() == 0 {
					return nil, fmt.Errorf("floating-point modulo by zero")
				}
				yf, err := y.finiteFloat()
				if err != nil {
					return nil, err
				}
				return x.Mod(yf), nil
			}
		case String:
			return interpolate(thread, string(x), y)
		}

	case syntax.NOT_IN:
		z, err := binaryOp(thread, syntax.IN, x, y)
		if err != nil {
			return nil, err
		}
		return !z.Truth(), nil

	case syntax.IN:
		m := thread.meter()
		switch y := y.(type) {
		case *List:
			found, err := hasElems(&m, y.elems, x)
			return Bool(found), err
		case Tuple:
			found, err := hasElems(&m, y, x)
			return Bool(found), err
		case String:
			if err := thread.chargeWork(workFast(len(y))); err != nil {
				return nil, err
			}
		case Bytes:
			if err := thread.chargeWork(workFast(len(y))); err != nil {
				return nil, err
			}
		case *Set:
			found, err := y.hasM(&m, x)
			if err == nil {
				err = m.flush()
			}
			return Bool(found), err
		case *Dict:
			// Ignore error from Get as we cannot distinguish true
			// errors (value cycle, type error) from "key not found".
			_, found, _ := y.getM(&m, x)
			if err := m.flush(); err != nil {
				return nil, err
			}
			return Bool(found), nil
		}
		switch y := y.(type) {
		case Container: // List, Tuple, Set, String, Bytes, rangeValue etc.
			found, err := y.Has(x)
			return Bool(found), err
		case Mapping: // e.g. dict
			// Ignore error from Get as we cannot distinguish true
			// errors (value cycle, type error) from "key not found".
			_, found, _ := y.Get(x)
			return Bool(found), nil
		}

	case syntax.PIPE:
		switch x := x.(type) {
		case Int:
			if y, ok := y.(Int); ok {
				if err := thread.chargeIntLinear(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(wordsAdd(x, y)); err != nil {
					return nil, excess(err, "excessive integer operation")
				}
				return thread.intDone(x.Or(y))
			}

		case *Dict: // union
			if y, ok := y.(*Dict); ok {
				if err := thread.roomEntries(x.Len() + y.Len()); err != nil {
					return nil, excess(err, "excessive dict union (%d + %d entries)", x.Len(), y.Len())
				}
				if err := thread.chargeEntries(0); err != nil {
					return nil, excess(err, "excessive dict union (%d + %d entries)", x.Len(), y.Len())
				}
				m := thread.meter()
				z, err := x.unionM(&m, y)
				if err = thread.finishGrowth(0, &m, err); err != nil {
					return nil, err
				}
				return z, nil
			}

		case *Set: // union
			if y, ok := y.(*Set); ok {
				if err := thread.roomEntries(x.Len() + y.Len()); err != nil {
					return nil, excess(err, "excessive set union (%d + %d elements)", x.Len(), y.Len())
				}
				if err := thread.chargeEntries(0); err != nil {
					return nil, excess(err, "excessive set union (%d + %d elements)", x.Len(), y.Len())
				}
				iter := Iterate(y)
				defer iter.Done()
				m := thread.meter()
				z, err := x.unionM(&m, iter)
				return z, thread.finishGrowth(0, &m, err)
			}
		}

	case syntax.AMP:
		switch x := x.(type) {
		case Int:
			if y, ok := y.(Int); ok {
				if err := thread.chargeIntLinear(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(wordsAdd(x, y)); err != nil {
					return nil, excess(err, "excessive integer operation")
				}
				return thread.intDone(x.And(y))
			}
		case *Set: // intersection
			if y, ok := y.(*Set); ok {
				if err := thread.roomEntries(min(x.Len(), y.Len())); err != nil {
					return nil, excess(err, "excessive set intersection (%d, %d elements)", x.Len(), y.Len())
				}
				if err := thread.chargeEntries(0); err != nil {
					return nil, excess(err, "excessive set intersection (%d, %d elements)", x.Len(), y.Len())
				}
				iter := y.Iterate()
				defer iter.Done()
				m := thread.meter()
				z, err := x.intersectionM(&m, iter)
				return z, thread.finishGrowth(0, &m, err)
			}
		}

	case syntax.CIRCUMFLEX:
		switch x := x.(type) {
		case Int:
			if y, ok := y.(Int); ok {
				if err := thread.chargeIntLinear(x, y); err != nil {
					return nil, err
				}
				if err := thread.intRoom(wordsAdd(x, y)); err != nil {
					return nil, excess(err, "excessive integer operation")
				}
				return thread.intDone(x.Xor(y))
			}
		case *Set: // symmetric difference
			if y, ok := y.(*Set); ok {
				if err := thread.roomEntries(x.Len() + y.Len()); err != nil {
					return nil, excess(err, "excessive set symmetric difference (%d + %d elements)", x.Len(), y.Len())
				}
				if err := thread.chargeEntries(0); err != nil {
					return nil, excess(err, "excessive set symmetric difference (%d + %d elements)", x.Len(), y.Len())
				}
				iter := y.Iterate()
				defer iter.Done()
				m := thread.meter()
				z, err := x.symmetricDifferenceM(&m, iter)
				return z, thread.finishGrowth(0, &m, err)
			}
		}

	case syntax.LTLT, syntax.GTGT:
		if x, ok := x.(Int); ok {
			y, err := AsInt32(y)
			if err != nil {
				return nil, err
			}
			if y < 0 {
				return nil, fmt.Errorf("negative shift count: %v", y)
			}
			if op == syntax.LTLT {
				if y >= 512 {
					return nil, fmt.Errorf("shift count too large: %v", y)
				}
				if err := thread.chargeIntLinear(x, x); err != nil {
					return nil, err
				}
				if w := bigWords(x); w != 0 {
					if err := thread.intRoom(w + uint64(y)/64 + 1); err != nil {
						return nil, excess(err, "excessive integer shift")
					}
				}
				return thread.intDone(x.Lsh(uint(y)))
			} else {
				if err := thread.chargeIntLinear(x, x); err != nil {
					return nil, err
				}
				if err := thread.intRoom(bigWords(x)); err != nil {
					return nil, excess(err, "excessive integer shift")
				}
				return thread.intDone(x.Rsh(uint(y)))
			}
		}

	default:
		// unknown operator
		goto unknown
	}

	// user-defined types
	// (nil, nil) => unhandled
	if x, ok := x.(HasBinary); ok {
		z, err := x.Binary(op, y, Left)
		if z != nil || err != nil {
			return z, err
		}
	}
	if y, ok := y.(HasBinary); ok {
		z, err := y.Binary(op, x, Right)
		if z != nil || err != nil {
			return z, err
		}
	}

	// unsupported operand types
unknown:
	return nil, fmt.Errorf("unknown binary op: %s %s %s", x.Type(), op, y.Type())
}

// It's always possible to overeat in small bites but we'll
// try to stop someone swallowing the world in one gulp.
// maxAlloc is the ceiling of the result of one operation, whether or not the
// thread has an allocation budget (see alloc.go): an operation whose result
// would be this large is refused before it allocates. Its unit is the
// element for lists, tuples, dicts and sets, and the byte for strings and
// bytes. It is a variable so package tests can lower the limit.
var maxAlloc = 1 << 30

func tupleRepeat(thread *Thread, elems Tuple, n Int, list bool) (Tuple, error) {
	if len(elems) == 0 {
		return nil, nil
	}
	i, err := AsInt32(n)
	if err != nil {
		return nil, fmt.Errorf("repeat count %s too large", n)
	}
	if i < 1 {
		return nil, nil
	}
	// Inv: i > 0, len > 0
	of, sz := bits.Mul(uint(len(elems)), uint(i))
	if of != 0 { // overflow
		sz = math.MaxUint
	}
	count := int(min(sz, math.MaxInt))
	var cerr error
	if list {
		cerr = thread.chargeValues(count)
	} else {
		cerr = thread.chargeTuple(count)
	}
	if cerr != nil {
		// Don't print sz.
		return nil, excess(cerr, "excessive repeat (%d * %d elements)", len(elems), i)
	}
	res := make([]Value, sz)
	// copy elems into res, doubling each time
	x := copy(res, elems)
	for x < len(res) {
		copy(res[x:], res[:x])
		x *= 2
	}
	return res, nil
}

func bytesRepeat(thread *Thread, b Bytes, n Int) (Bytes, error) {
	res, err := stringRepeat(thread, String(b), n)
	return Bytes(res), err
}

func stringRepeat(thread *Thread, s String, n Int) (String, error) {
	if s == "" {
		return "", nil
	}
	i, err := AsInt32(n)
	if err != nil {
		return "", fmt.Errorf("repeat count %s too large", n)
	}
	if i < 1 {
		return "", nil
	}
	// Inv: i > 0, len > 0
	if i == 1 {
		return s, nil // the same string: nothing is allocated
	}
	of, sz := bits.Mul(uint(len(s)), uint(i))
	if of != 0 { // overflow
		sz = math.MaxUint
	}
	if err := thread.chargeBytes(int(min(sz, math.MaxInt))); err != nil {
		// Don't print sz.
		return "", excess(err, "excessive repeat (%d * %d elements)", len(s), i)
	}
	return String(strings.Repeat(string(s), i)), nil
}

// Call calls the function fn with the specified positional and keyword arguments.
//
// When fn is a *Builtin, Call charges the thread's allocation budget for the
// string, bytes, list, tuple, dict or set it returns (by the shallow size
// formula of alloc.go), less whatever the built-in itself charged during the
// call. So a built-in that checks its result size before allocating (and
// charges it) is not charged twice, and one that does not charge at all
// (including one implemented by the host) is still counted. If the budget is
// exceeded the call fails with an *AllocBudgetError.
func Call(thread *Thread, fn Value, args Tuple, kwargs []Tuple) (Value, error) {
	c, ok := fn.(Callable)
	if !ok {
		return nil, fmt.Errorf("invalid call of non-function (%s)", fn.Type())
	}

	// Allocate and push a new frame.
	var fr *frame
	// Optimization: use slack portion of thread.stack
	// slice as a freelist of empty frames.
	if n := len(thread.stack); n < cap(thread.stack) {
		fr = thread.stack[n : n+1][0]
	}
	if fr == nil {
		fr = new(frame)
	}

	if thread.stack == nil {
		// one-time initialization of thread
		if thread.maxSteps == 0 {
			thread.maxSteps-- // (MaxUint64)
		}
	}

	thread.stack = append(thread.stack, fr) // push

	fr.callable = c

	thread.beginProfSpan()

	// Use defer to ensure that panics from built-ins
	// pass through the interpreter without leaving
	// it in a bad state.
	defer func() {
		thread.endProfSpan()

		// clear out any references
		// TODO(adonovan): opt: zero fr.Locals and
		// reuse it if it is large enough.
		*fr = frame{}

		thread.stack = thread.stack[:len(thread.stack)-1] // pop
	}()

	builtin, isBuiltin := c.(*Builtin)
	var workErr error
	if isBuiltin && builtin.price != nil && builtin.price.work != nil {
		// The work of the built-in, from the sizes of its operands, before it
		// is done (see prices.go): a call that does not fit in the steps that
		// are left is refused, not run.
		workErr = thread.chargeWork(builtin.price.work(builtin.recv, args, kwargs))
	}
	isBuiltin = isBuiltin && !(builtin.price != nil && builtin.price.accounts)
	var allocBefore uint64
	if isBuiltin {
		allocBefore = thread.allocated
	}

	var result Value
	var err error
	if workErr != nil {
		err = workErr
	} else {
		result, err = c.CallInternal(thread, args, kwargs)
	}

	// Charge what the built-in returned and did not charge itself.
	if isBuiltin && err == nil && result != nil {
		if n := shallowSize(result); n > 0 {
			if charged := thread.allocated - allocBefore; n > charged {
				if err = thread.chargeBudget(n - charged); err != nil {
					result = nil
				}
			}
		}
	}

	// Sanity check: nil is not a valid Starlark value.
	if result == nil && err == nil {
		err = fmt.Errorf("internal error: nil (not None) returned from %s", fn)
	}

	// Always return an EvalError with an accurate frame.
	if err != nil {
		if _, ok := err.(*EvalError); !ok {
			err = thread.evalError(err)
		}
	}

	return result, err
}

func slice(thread *Thread, x, lo, hi, step_ Value) (Value, error) {
	sliceable, ok := x.(Sliceable)
	if !ok {
		return nil, fmt.Errorf("invalid slice operand %s", x.Type())
	}

	n := sliceable.Len()
	step := 1
	if step_ != None {
		var err error
		step, err = AsInt32(step_)
		if err != nil {
			return nil, fmt.Errorf("invalid slice step: %s", err)
		}
		if step == 0 {
			return nil, fmt.Errorf("zero is not a valid slice step")
		}
	}

	// TODO(adonovan): opt: preallocate result array.

	var start, end int
	if step > 0 {
		// positive stride
		// default indices are [0:n].
		var err error
		start, end, err = indices(lo, hi, n)
		if err != nil {
			return nil, err
		}

		if end < start {
			end = start // => empty result
		}
	} else {
		// negative stride
		// default indices are effectively [n-1:-1], though to
		// get this effect using explicit indices requires
		// [n-1:-1-n:-1] because of the treatment of -ve values.
		start = n - 1
		if err := asIndex(lo, n, &start); err != nil {
			return nil, fmt.Errorf("invalid start index: %s", err)
		}
		if start >= n {
			start = n - 1
		}

		end = -1
		if err := asIndex(hi, n, &end); err != nil {
			return nil, fmt.Errorf("invalid end index: %s", err)
		}
		if end < -1 {
			end = -1
		}

		if start < end {
			start = end // => empty result
		}
	}

	// A slice of a list, or of a string, bytes or tuple with a step, is a new
	// array of known length. (A string, bytes or tuple slice with step 1
	// shares the operand's memory; a range slice is lazy.) Check the size
	// before the copy is made.
	switch x := x.(type) {
	case rangeValue:
		return x.slice(start, end, step) // lazy: nothing to charge
	case *List:
		if err := thread.chargeValues(sliceLen(start, end, step)); err != nil {
			return nil, excess(err, "excessive slice (%d elements)", sliceLen(start, end, step))
		}
	case Tuple:
		if step != 1 {
			if err := thread.chargeTuple(sliceLen(start, end, step)); err != nil {
				return nil, excess(err, "excessive slice (%d elements)", sliceLen(start, end, step))
			}
		}
	case String, Bytes:
		if step != 1 {
			if err := thread.chargeBytes(sliceLen(start, end, step)); err != nil {
				return nil, excess(err, "excessive slice (%d bytes)", sliceLen(start, end, step))
			}
		}
	}

	return sliceable.Slice(start, end, step), nil
}

// From Hacker's Delight, section 2.8.
func signum64(x int64) int { return int(uint64(x>>63) | uint64(-x)>>63) }
func signum(x int) int     { return signum64(int64(x)) }

// indices converts start_ and end_ to indices in the range [0:len].
// The start index defaults to 0 and the end index defaults to len.
// An index -len < i < 0 is treated like i+len.
// All other indices outside the range are clamped to the nearest value in the range.
// Beware: start may be greater than end.
// This function is suitable only for slices with positive strides.
func indices(start_, end_ Value, len int) (start, end int, err error) {
	start = 0
	if err := asIndex(start_, len, &start); err != nil {
		return 0, 0, fmt.Errorf("invalid start index: %s", err)
	}
	// Clamp to [0:len].
	if start < 0 {
		start = 0
	} else if start > len {
		start = len
	}

	end = len
	if err := asIndex(end_, len, &end); err != nil {
		return 0, 0, fmt.Errorf("invalid end index: %s", err)
	}
	// Clamp to [0:len].
	if end < 0 {
		end = 0
	} else if end > len {
		end = len
	}

	return start, end, nil
}

// asIndex sets *result to the integer value of v, adding len to it
// if it is negative.  If v is nil or None, *result is unchanged.
func asIndex(v Value, len int, result *int) error {
	if v != nil && v != None {
		var err error
		*result, err = AsInt32(v)
		if err != nil {
			return err
		}
		if *result < 0 {
			*result += len
		}
	}
	return nil
}

// setArgs sets the values of the formal parameters of function fn in
// based on the actual parameter values in args and kwargs.
func setArgs(thread *Thread, locals []Value, fn *Function, args Tuple, kwargs []Tuple) error {

	// This is the general schema of a function:
	//
	//   def f(p1, p2=dp2, p3=dp3, *args, k1, k2=dk2, k3, **kwargs)
	//
	// The p parameters are non-kwonly, and may be specified positionally.
	// The k parameters are kwonly, and must be specified by name.
	// The defaults tuple is (dp2, dp3, mandatory, dk2, mandatory).
	//
	// Arguments are processed as follows:
	// - positional arguments are bound to a prefix of [p1, p2, p3].
	// - surplus positional arguments are bound to *args.
	// - keyword arguments are bound to any of {p1, p2, p3, k1, k2, k3};
	//   duplicate bindings are rejected.
	// - surplus keyword arguments are bound to **kwargs.
	// - defaults are bound to each parameter from p2 to k3 if no value was set.
	//   default values come from the tuple above.
	//   It is an error if the tuple entry for an unset parameter is 'mandatory'.

	// Nullary function?
	if fn.NumParams() == 0 {
		if nactual := len(args) + len(kwargs); nactual > 0 {
			return fmt.Errorf("function %s accepts no arguments (%d given)", fn.Name(), nactual)
		}
		return nil
	}

	// Each keyword argument is looked up among the parameters.
	if len(kwargs) > 0 {
		if err := thread.chargeWork(3 * uint64(len(kwargs))); err != nil {
			return err
		}
	}

	cond := func(x bool, y, z any) any {
		if x {
			return y
		}
		return z
	}

	// nparams is the number of ordinary parameters (sans *args and **kwargs).
	nparams := fn.NumParams()
	var kwdict *Dict
	if fn.HasKwargs() {
		nparams--
		if err := thread.chargeEntries(0); err != nil {
			return err
		}
		kwdict = new(Dict)
		locals[nparams] = kwdict
	}
	if fn.HasVarargs() {
		nparams--
	}

	// nonkwonly is the number of non-kwonly parameters.
	nonkwonly := nparams - fn.NumKwonlyParams()

	// Too many positional args?
	n := len(args)
	if len(args) > nonkwonly {
		if !fn.HasVarargs() {
			return fmt.Errorf("function %s accepts %s%d positional argument%s (%d given)",
				fn.Name(),
				cond(len(fn.defaults) > fn.NumKwonlyParams(), "at most ", ""),
				nonkwonly,
				cond(nonkwonly == 1, "", "s"),
				len(args))
		}
		n = nonkwonly
	}

	// Bind positional arguments to non-kwonly parameters.
	for i := 0; i < n; i++ {
		locals[i] = args[i]
	}

	// Bind surplus positional arguments to *args parameter.
	if fn.HasVarargs() {
		// The surplus arguments are copied into a new tuple.
		if err := thread.chargeTuple(len(args) - n); err != nil {
			return err
		}
		tuple := make(Tuple, len(args)-n)
		for i := n; i < len(args); i++ {
			tuple[i-n] = args[i]
		}
		locals[nparams] = tuple
	}

	// Bind keyword arguments to parameters.
	paramIdents := fn.funcode.Locals[:nparams]
	var paramIndex map[string]int // for many parameters and many keyword arguments
	if len(kwargs) > 8 && nparams > 8 {
		paramIndex = fn.funcode.ParamIndex()
	}
	for _, pair := range kwargs {
		k, v := pair[0].(String), pair[1]
		i := -1
		if paramIndex != nil {
			if j, ok := paramIndex[string(k)]; ok {
				i = j
			}
		} else {
			i = findParam(paramIdents, string(k))
		}
		if i >= 0 {
			if locals[i] != nil {
				return fmt.Errorf("function %s got multiple values for parameter %s", fn.Name(), errValue(k))
			}
			locals[i] = v
			continue
		}
		if kwdict == nil {
			return fmt.Errorf("function %s got an unexpected keyword argument %s", fn.Name(), errValue(k))
		}
		oldlen := kwdict.Len()
		kwdict.SetKey(k, v)
		if err := thread.chargeNewEntry(kwdict, oldlen); err != nil {
			return err
		}
		if kwdict.Len() == oldlen {
			return fmt.Errorf("function %s got multiple values for parameter %s", fn.Name(), errValue(k))
		}
	}

	// Are defaults required?
	if n < nparams || fn.NumKwonlyParams() > 0 {
		m := nparams - len(fn.defaults) // first default

		// Report errors for missing required arguments.
		var missing []string
		var i int
		for i = n; i < m; i++ {
			if locals[i] == nil {
				missing = append(missing, paramIdents[i].Name)
			}
		}

		// Bind default values to parameters.
		for ; i < nparams; i++ {
			if locals[i] == nil {
				dflt := fn.defaults[i-m]
				if _, ok := dflt.(mandatory); ok {
					missing = append(missing, paramIdents[i].Name)
					continue
				}
				locals[i] = dflt
			}
		}

		if missing != nil {
			return fmt.Errorf("function %s missing %d argument%s (%s)",
				fn.Name(), len(missing), cond(len(missing) > 1, "s", ""), strings.Join(missing, ", "))
		}
	}
	return nil
}

func findParam(params []compile.Binding, name string) int {
	for i, param := range params {
		if param.Name == name {
			return i
		}
	}
	return -1
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string-interpolation
//
// The result is bounded by the ceiling and the thread's budget as it is built
// (a %s of a shared subgraph can expand exponentially), then charged.
func interpolate(thread *Thread, format string, x Value) (Value, error) {
	limit := thread.stringLimit()
	s, err := thread.buildForm(func(th *Thread, buf *sink, m *meter) error {
		return interpolateTo(th, buf, m, limit, format, x)
	}, func(n int) error {
		if err := thread.chargeBytes(n); err != nil {
			return excess(err, "excessive string interpolation (over %d bytes)", maxAlloc)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return String(s), nil
}

// interpolateTo writes the interpolation of format with x to buf (see
// Thread.buildForm: it is run twice for a large result).
func interpolateTo(thread *Thread, buf *sink, m *meter, limit int, format string, x Value) error {
	index := 0
	nargs := 1
	if tuple, ok := x.(Tuple); ok {
		nargs = len(tuple)
	}
	for {
		// Bound the interpolation result like repeat.
		if buf.Len() >= limit {
			return thread.refuseBytes(buf.Len(), "excessive string interpolation (over %d bytes)", maxAlloc)
		}
		i := strings.IndexByte(format, '%')
		if i < 0 {
			buf.WriteString(format)
			break
		}
		buf.WriteString(format[:i])
		format = format[i+1:]

		if format != "" && format[0] == '%' {
			buf.WriteByte('%')
			format = format[1:]
			continue
		}

		var arg Value
		if format != "" && format[0] == '(' {
			// keyword argument: %(name)s.
			format = format[1:]
			j := strings.IndexByte(format, ')')
			if j < 0 {
				return fmt.Errorf("incomplete format key")
			}
			key := format[:j]
			if dict, ok := x.(Mapping); !ok {
				return fmt.Errorf("format requires a mapping")
			} else if v, found, _ := dict.Get(String(key)); found {
				arg = v
			} else {
				return fmt.Errorf("key not found: %s", errStr(key))
			}
			format = format[j+1:]
		} else {
			// positional argument: %s.
			if index >= nargs {
				return fmt.Errorf("not enough arguments for format string")
			}
			if tuple, ok := x.(Tuple); ok {
				arg = tuple[index]
			} else {
				arg = x
			}
		}

		// NOTE: Starlark does not support any of these optional Python features:
		// - optional conversion flags: [#0- +], etc.
		// - optional minimum field width (number or *).
		// - optional precision (.123 or *)
		// - optional length modifier

		// conversion type
		if format == "" {
			return fmt.Errorf("incomplete format")
		}
		switch c := format[0]; c {
		case 's', 'r':
			if str, ok := AsString(arg); ok && c == 's' {
				buf.WriteString(str)
			} else if code, werr := writeValueMeter(buf, arg, limit, m); code != writeOK {
				return thread.formErr(code, werr, limit, "%", "excessive string interpolation (over %d bytes)")
			}
		case 'd', 'i', 'o', 'x', 'X':
			i, err := NumberToInt(arg)
			if err != nil {
				return fmt.Errorf("%%%c format requires integer: %v", c, err)
			}
			if err := thread.chargeIntToString(i, c == 'd' || c == 'i'); err != nil {
				return err
			}
			switch c {
			case 'd', 'i':
				fmt.Fprintf(buf, "%d", i)
			case 'o':
				fmt.Fprintf(buf, "%o", i)
			case 'x':
				fmt.Fprintf(buf, "%x", i)
			case 'X':
				fmt.Fprintf(buf, "%X", i)
			}
		case 'e', 'f', 'g', 'E', 'F', 'G':
			f, ok := AsFloat(arg)
			if !ok {
				return fmt.Errorf("%%%c format requires float, not %s", c, arg.Type())
			}
			Float(f).format(buf, c)
		case 'c':
			switch arg := arg.(type) {
			case Int:
				// chr(int)
				r, err := AsInt32(arg)
				if err != nil || r < 0 || r > unicode.MaxRune {
					return fmt.Errorf("%%c format requires a valid Unicode code point, got %s", errValue(arg))
				}
				buf.writeRune(rune(r))
			case String:
				r, size := utf8.DecodeRuneInString(string(arg))
				if size != len(arg) || len(arg) == 0 {
					return fmt.Errorf("%%c format requires a single-character string")
				}
				buf.writeRune(r)
			default:
				return fmt.Errorf("%%c format requires int or single-character string, not %s", arg.Type())
			}
		case '%':
			buf.WriteByte('%')
		default:
			return fmt.Errorf("unknown conversion %%%c", c)
		}
		format = format[1:]
		index++
	}

	if index < nargs && !is[Mapping](x) {
		return fmt.Errorf("too many arguments for format string")
	}
	return nil
}

func is[T any](x any) bool {
	_, ok := x.(T)
	return ok
}

// DefaultMaxCallStackDepth is the depth of the stack of calls that a thread
// allows if SetMaxCallStackDepth was not called: the limit that a program with
// recursion had, and that a program without it did not (a chain of different
// functions as long as the limit takes the same Go stack: about 100 MiB).
const DefaultMaxCallStackDepth = 100_000

// SetMaxCallStackDepth sets the depth of the stack of calls that this thread
// allows, in frames (a call of a function or a built-in is a frame), with or
// without recursion. A call that would be deeper fails with the error
// "Starlark stack overflow", as a Starlark error, not a crash of Go. n <= 0
// restores DefaultMaxCallStackDepth.
//
// Each level of the stack of calls takes the stack of Go that the interpreter
// uses for it: 1.4-2.7 KiB for a call of a function, and more for a call
// through a built-in (sorted(key=f), max(key=f)); a host that sets the limit
// must keep it far below the limit of the stack of Go (1 GiB by default).
func (thread *Thread) SetMaxCallStackDepth(n int) {
	thread.callDepth = n
}

func (thread *Thread) maxCallDepth() int {
	if thread.callDepth > 0 {
		return thread.callDepth
	}
	return DefaultMaxCallStackDepth
}

// recursionScanDepth is the depth up to which a call looks at the frames to
// see if its function is active; deeper, the number of active calls of each
// function is kept in a map, so that a call is not as slow as the stack is
// deep (a chain of a hundred thousand functions made it quadratic).
const recursionScanDepth = 32

// enterFunction reports an error if fn is active: called, and not returned
// from. The funcode is compared, not the function value, otherwise the user
// could defeat the check by writing the Y combinator.
func (thread *Thread) enterFunction(fn *Function, f *compile.Funcode) error {
	below := thread.stack[:len(thread.stack)-1]
	if thread.active == nil {
		if len(below) <= recursionScanDepth {
			thread.scanned += uint64(len(below))
			for _, fr := range below {
				if frfn, ok := fr.Callable().(*Function); ok && frfn.funcode == f {
					return fmt.Errorf("function %s called recursively", fn.Name())
				}
			}
			return nil
		}
		// The stack is deep now: count the active calls once, and keep the
		// count from here on.
		thread.active = make(map[*compile.Funcode]int32)
		thread.scanned += uint64(len(below))
		for _, fr := range below {
			if frfn, ok := fr.Callable().(*Function); ok {
				thread.active[frfn.funcode]++
			}
		}
	}
	if thread.active[f] > 0 {
		return fmt.Errorf("function %s called recursively", fn.Name())
	}
	thread.active[f]++
	return nil
}

// leaveFunction undoes enterFunction, for a call that did not fail with it.
func (thread *Thread) leaveFunction(f *compile.Funcode) {
	if thread.active != nil {
		if thread.active[f]--; thread.active[f] == 0 {
			delete(thread.active, f)
		}
		if len(thread.stack)-1 <= recursionScanDepth {
			thread.active = nil
		}
	}
}
