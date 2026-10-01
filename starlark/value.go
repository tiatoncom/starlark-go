// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package starlark provides a Starlark interpreter.
//
// Starlark values are represented by the Value interface.
// The following built-in Value types are known to the evaluator:
//
//	NoneType        -- NoneType
//	Bool            -- bool
//	Bytes           -- bytes
//	Int             -- int
//	Float           -- float
//	String          -- string
//	*List           -- list
//	Tuple           -- tuple
//	*Dict           -- dict
//	*Set            -- set
//	*Function       -- function (implemented in Starlark)
//	*Builtin        -- builtin_function_or_method (function or method implemented in Go)
//
// Client applications may define new data types that satisfy at least
// the Value interface.  Such types may provide additional operations by
// implementing any of these optional interfaces:
//
//	Callable        -- value is callable like a function
//	Comparable      -- value defines its own comparison operations
//	Container       -- value supports the 'in' operator
//	Iterable        -- value is iterable using 'for' loops
//	Sequence        -- value is iterable sequence of known length
//	Indexable       -- value is sequence with efficient random access
//	Mapping         -- value maps from keys to values, like a dictionary
//	HasBinary       -- value defines binary operations such as * and +
//	HasAttrs        -- value has readable fields or methods x.f
//	HasSetField     -- value has settable fields x.f
//	HasSetIndex     -- value supports element update using x[i]=y
//	HasSetKey       -- value supports map update using x[k]=v
//	HasUnary        -- value defines unary operations such as + and -
//
// Client applications may also define domain-specific functions in Go
// and make them available to Starlark programs.  Use NewBuiltin to
// construct a built-in value that wraps a Go function.  The
// implementation of the Go function may use UnpackArgs to make sense of
// the positional and keyword arguments provided by the caller.
//
// Starlark's None value is not equal to Go's nil. Go's nil is not a legal
// Starlark value, but the compiler will not stop you from converting nil
// to Value. Be careful to avoid allowing Go nil values to leak into
// Starlark data structures.
//
// The Compare operation requires two arguments of the same
// type, but this constraint cannot be expressed in Go's type system.
// (This is the classic "binary method problem".)
// So, each Value type's CompareSameType method is a partial function
// that compares a value only against others of the same type.
// Use the package's standalone Compare (or Equal) function to compare
// an arbitrary pair of values.
//
// To parse and evaluate a Starlark source file, use ExecFile.  The Eval
// function evaluates a single expression.  All evaluator functions
// require a Thread parameter which defines the "thread-local storage"
// of a Starlark thread and may be used to plumb application state
// through Starlark code and into callbacks.  When evaluation fails it
// returns an EvalError from which the application may obtain a
// backtrace of active Starlark calls.
package starlark // import "go.starlark.net/starlark"

// This file defines the data types of Starlark and their basic operations.

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.starlark.net/internal/compile"
	"go.starlark.net/syntax"
)

// Value is a value in the Starlark interpreter.
type Value interface {
	// String returns the string representation of the value.
	// Starlark string values are quoted as if by Python's repr.
	String() string

	// Type returns a short string describing the value's type.
	Type() string

	// Freeze causes the value, and all values transitively
	// reachable from it through collections and closures, to be
	// marked as frozen.  All subsequent mutations to the data
	// structure through this API will fail dynamically, making the
	// data structure immutable and safe for publishing to other
	// Starlark interpreters running concurrently.
	//
	// Implementations of Freeze must be defensive against
	// reference cycles; this can be achieved by first checking
	// the value's frozen state, then setting it, and only then
	// visiting any other values that it references.
	Freeze()

	// Truth returns the truth value of an object.
	Truth() Bool

	// Hash returns a function of x such that Equals(x, y) => Hash(x) == Hash(y).
	// Hash may fail if the value's type is not hashable, or if the value
	// contains a non-hashable value. The hash is used only by dictionaries and
	// is not exposed to the Starlark program.
	Hash() (uint32, error)
}

// A Comparable is a value that defines its own equivalence relation and
// perhaps ordered comparisons.
type Comparable interface {
	Value
	// CompareSameType compares one value to another of the same Type().
	// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
	// CompareSameType returns an error if an ordered comparison was
	// requested for a type that does not support it.
	//
	// Implementations that recursively compare subcomponents of
	// the value should use the CompareDepth function, not Compare, to
	// avoid infinite recursion on cyclic structures.
	//
	// The depth parameter is used to bound comparisons of cyclic
	// data structures.  Implementations should decrement depth
	// before calling CompareDepth and should return an error if depth
	// < 1.
	//
	// Client code should not call this method.  Instead, use the
	// standalone Compare or Equals functions, which are defined for
	// all pairs of operands.
	CompareSameType(op syntax.Token, y Value, depth int) (bool, error)
}

// A TotallyOrdered is a type whose values form a total order:
// if x and y are of the same TotallyOrdered type, then x must be less than y,
// greater than y, or equal to y.
//
// It is simpler than Comparable and should be preferred in new code,
// but if a type implements both interfaces, Comparable takes precedence.
type TotallyOrdered interface {
	Value
	// Cmp compares two values x and y of the same totally ordered type.
	// It returns negative if x < y, positive if x > y, and zero if the values are equal.
	//
	// Implementations that recursively compare subcomponents of
	// the value should use the CompareDepth function, not Cmp, to
	// avoid infinite recursion on cyclic structures.
	//
	// The depth parameter is used to bound comparisons of cyclic
	// data structures.  Implementations should decrement depth
	// before calling CompareDepth and should return an error if depth
	// < 1.
	//
	// Client code should not call this method.  Instead, use the
	// standalone Compare or Equals functions, which are defined for
	// all pairs of operands.
	Cmp(y Value, depth int) (int, error)
}

var (
	_ TotallyOrdered = Int{}
	_ TotallyOrdered = Float(0)
	_ Comparable     = False
	_ Comparable     = String("")
	_ Comparable     = (*Dict)(nil)
	_ Comparable     = (*List)(nil)
	_ Comparable     = Tuple(nil)
	_ Comparable     = (*Set)(nil)
)

// A Callable value f may be the operand of a function call, f(x).
//
// Clients should use the Call function, never the CallInternal method.
type Callable interface {
	Value
	Name() string
	CallInternal(thread *Thread, args Tuple, kwargs []Tuple) (Value, error)
}

type callableWithPosition interface {
	Callable
	Position() syntax.Position
}

var (
	_ Callable             = (*Builtin)(nil)
	_ Callable             = (*Function)(nil)
	_ callableWithPosition = (*Function)(nil)
)

// An Iterable abstracts a sequence of values.
// An iterable value may be iterated over by a 'for' loop or used where
// any other Starlark iterable is allowed.  Unlike a Sequence, the length
// of an Iterable is not necessarily known in advance of iteration.
type Iterable interface {
	Value
	Iterate() Iterator // must be followed by call to Iterator.Done
}

// A Sequence is a sequence of values of known length.
type Sequence interface {
	Iterable
	Len() int
}

var (
	_ Sequence = (*Dict)(nil)
	_ Sequence = (*Set)(nil)
)

// An Indexable is a sequence of known length that supports efficient random access.
// It is not necessarily iterable.
type Indexable interface {
	Value
	Index(i int) Value // requires 0 <= i < Len()
	Len() int
}

// A Sliceable is a sequence that can be cut into pieces with the slice operator (x[i:j:step]).
//
// All native indexable objects are sliceable.
// This is a separate interface for backwards-compatibility.
type Sliceable interface {
	Indexable
	// For positive strides (step > 0), 0 <= start <= end <= n.
	// For negative strides (step < 0), -1 <= end <= start < n.
	// The caller must ensure that the start and end indices are valid
	// and that step is non-zero.
	Slice(start, end, step int) Value
}

// A Container is a value that supports the 'in' operator.
type Container interface {
	Value
	// Has reports whether the value contains the specified element.
	Has(y Value) (bool, error)
}

// A HasSetIndex is an Indexable value whose elements may be assigned (x[i] = y).
//
// The implementation should not add Len to a negative index as the
// evaluator does this before the call.
type HasSetIndex interface {
	Indexable
	SetIndex(index int, v Value) error
}

var (
	_ HasSetIndex = (*List)(nil)
	_ Indexable   = Tuple(nil)
	_ Indexable   = String("")
	_ Sliceable   = Tuple(nil)
	_ Sliceable   = String("")
	_ Sliceable   = (*List)(nil)
	_ Container   = Tuple(nil)
	_ Container   = String("")
	_ Container   = (*List)(nil)
	_ Container   = (*Set)(nil)
)

// An Iterator provides a sequence of values to the caller.
//
// The caller must call Done when the iterator is no longer needed.
// Operations that modify a sequence will fail if it has active iterators.
//
// Example usage:
//
//	var seq Iterator = ...
//	iter := seq.Iterate()
//	defer iter.Done()
//	var elem Value
//	for iter.Next(&elem) {
//		...
//	}
//
// Or, using go1.23 iterators:
//
//	for elem := range Elements(seq) { ... }
type Iterator interface {
	// If the iterator is exhausted, Next returns false.
	// Otherwise it sets *p to the current element of the sequence,
	// advances the iterator, and returns true.
	Next(p *Value) bool
	Done()
}

// A Mapping is a mapping from keys to values, such as a dictionary.
//
// If a type satisfies both Mapping and Iterable, the iterator yields
// the keys of the mapping.
type Mapping interface {
	Value
	// Get returns the value corresponding to the specified key,
	// or !found if the mapping does not contain the key.
	//
	// Get also defines the behavior of "v in mapping".
	// The 'in' operator reports the 'found' component, ignoring errors.
	Get(Value) (v Value, found bool, err error)
}

// An IterableMapping is a mapping that supports key enumeration.
//
// See [Entries] for example use.
type IterableMapping interface {
	Mapping
	Iterate() Iterator // see Iterable interface
	Items() []Tuple    // a new slice containing all key/value pairs
}

var _ IterableMapping = (*Dict)(nil)

// A HasSetKey supports map update using x[k]=v syntax, like a dictionary.
type HasSetKey interface {
	Mapping
	SetKey(k, v Value) error
}

var _ HasSetKey = (*Dict)(nil)

// A HasBinary value may be used as either operand of these binary operators:
// +   -   *   /   //   %   in   not in   |   &   ^   <<   >>
//
// The Side argument indicates whether the receiver is the left or right operand.
//
// An implementation may decline to handle an operation by returning (nil, nil).
// For this reason, clients should always call the standalone Binary(op, x, y)
// function rather than calling the method directly.
type HasBinary interface {
	Value
	Binary(op syntax.Token, y Value, side Side) (Value, error)
}

type Side bool

const (
	Left  Side = false
	Right Side = true
)

// A HasUnary value may be used as the operand of these unary operators:
// +   -   ~
//
// An implementation may decline to handle an operation by returning (nil, nil).
// For this reason, clients should always call the standalone Unary(op, x)
// function rather than calling the method directly.
type HasUnary interface {
	Value
	Unary(op syntax.Token) (Value, error)
}

// A HasAttrs value has fields or methods that may be read by a dot expression (y = x.f).
// Attribute names may be listed using the built-in 'dir' function.
//
// For implementation convenience, a result of (nil, nil) from Attr is
// interpreted as a "no such field or method" error. Implementations are
// free to return a more precise error.
type HasAttrs interface {
	Value
	Attr(name string) (Value, error) // returns (nil, nil) if attribute not present
	AttrNames() []string             // callers must not modify the result.
}

var (
	_ HasAttrs = String("")
	_ HasAttrs = new(List)
	_ HasAttrs = new(Dict)
	_ HasAttrs = new(Set)
)

// A HasSetField value has fields that may be written by a dot expression (x.f = y).
//
// An implementation of SetField may return a NoSuchAttrError,
// in which case the runtime may augment the error message to
// warn of possible misspelling.
type HasSetField interface {
	HasAttrs
	SetField(name string, val Value) error
}

// A NoSuchAttrError may be returned by an implementation of
// HasAttrs.Attr or HasSetField.SetField to indicate that no such field
// exists. In that case the runtime may augment the error message to
// warn of possible misspelling.
type NoSuchAttrError string

func (e NoSuchAttrError) Error() string { return string(e) }

// NoneType is the type of None.  Its only legal value is None.
// (We represent it as a number, not struct{}, so that None may be constant.)
type NoneType byte

const None = NoneType(0)

func (NoneType) String() string        { return "None" }
func (NoneType) Type() string          { return "NoneType" }
func (NoneType) Freeze()               {} // immutable
func (NoneType) Truth() Bool           { return False }
func (NoneType) Hash() (uint32, error) { return 0, nil }

// Bool is the type of a Starlark bool.
type Bool bool

const (
	False Bool = false
	True  Bool = true
)

func (b Bool) String() string {
	if b {
		return "True"
	} else {
		return "False"
	}
}
func (b Bool) Type() string          { return "bool" }
func (b Bool) Freeze()               {} // immutable
func (b Bool) Truth() Bool           { return b }
func (b Bool) Hash() (uint32, error) { return uint32(b2i(bool(b))), nil }
func (x Bool) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Bool)
	return threeway(op, b2i(bool(x))-b2i(bool(y))), nil
}

// Float is the type of a Starlark float.
type Float float64

func (f Float) String() string {
	var tmp [64]byte
	return string(f.appendFormat(tmp[:0], 'g'))
}

func (f Float) format(buf io.StringWriter, conv byte) {
	var tmp [64]byte
	buf.WriteString(string(f.appendFormat(tmp[:0], conv)))
}

// appendFormat appends the form of f in the conversion conv (%g, %e, %f and
// their upper-case forms) to dst.
func (f Float) appendFormat(dst []byte, conv byte) []byte {
	ff := float64(f)
	if !isFinite(ff) {
		if math.IsInf(ff, +1) {
			return append(dst, "+inf"...)
		} else if math.IsInf(ff, -1) {
			return append(dst, "-inf"...)
		}
		return append(dst, "nan"...)
	}

	// %g is the default format used by str.
	// It uses the minimum precision to avoid ambiguity,
	// and always includes a '.' or an 'e' so that the value
	// is self-evidently a float, not an int.
	if conv == 'g' || conv == 'G' {
		n := len(dst)
		dst = strconv.AppendFloat(dst, ff, conv, -1, 64)
		// Ensure result always has a decimal point if no exponent.
		// "123" -> "123.0"
		if bytes.IndexByte(dst[n:], conv-'g'+'e') < 0 && bytes.IndexByte(dst[n:], '.') < 0 {
			dst = append(dst, ".0"...)
		}
		return dst
	}

	// %[eEfF] use 6-digit precision
	return strconv.AppendFloat(dst, ff, conv, 6, 64)
}

// appendFloatG appends f.String() to dst, without allocating if dst has room.
func appendFloatG(dst []byte, f Float) []byte {
	ff := float64(f)
	if !isFinite(ff) {
		if math.IsInf(ff, +1) {
			return append(dst, "+inf"...)
		} else if math.IsInf(ff, -1) {
			return append(dst, "-inf"...)
		}
		return append(dst, "nan"...)
	}
	n := len(dst)
	dst = strconv.AppendFloat(dst, ff, 'g', -1, 64)
	if bytes.IndexByte(dst[n:], 'e') < 0 && bytes.IndexByte(dst[n:], '.') < 0 {
		dst = append(dst, ".0"...)
	}
	return dst
}

func (f Float) Type() string { return "float" }
func (f Float) Freeze()      {} // immutable
func (f Float) Truth() Bool  { return f != 0.0 }
func (f Float) Hash() (uint32, error) {
	// Equal float and int values must yield the same hash.
	// TODO(adonovan): opt: if f is non-integral, and thus not equal
	// to any Int, we can avoid the Int conversion and use a cheaper hash.
	if isFinite(float64(f)) {
		return finiteFloatToInt(f).Hash()
	}
	return 1618033, nil // NaN, +/-Inf
}

func floor(f Float) Float { return Float(math.Floor(float64(f))) }

// isFinite reports whether f represents a finite rational value.
// It is equivalent to !math.IsNan(f) && !math.IsInf(f, 0).
func isFinite(f float64) bool {
	return math.Abs(f) <= math.MaxFloat64
}

// Cmp implements comparison of two Float values.
// Required by the TotallyOrdered interface.
func (f Float) Cmp(v Value, depth int) (int, error) {
	g := v.(Float)
	return floatCmp(f, g), nil
}

// floatCmp performs a three-valued comparison on floats,
// which are totally ordered with NaN > +Inf.
func floatCmp(x, y Float) int {
	if x > y {
		return +1
	} else if x < y {
		return -1
	} else if x == y {
		return 0
	}

	// At least one operand is NaN.
	if x == x {
		return -1 // y is NaN
	} else if y == y {
		return +1 // x is NaN
	}
	return 0 // both NaN
}

func (f Float) rational() *big.Rat { return new(big.Rat).SetFloat64(float64(f)) }

// AsFloat returns the float64 value closest to x.
// The f result is undefined if x is not a float or Int.
// The result may be infinite if x is a very large Int.
func AsFloat(x Value) (f float64, ok bool) {
	switch x := x.(type) {
	case Float:
		return float64(x), true
	case Int:
		return float64(x.Float()), true
	}
	return 0, false
}

func (x Float) Mod(y Float) Float {
	z := Float(math.Mod(float64(x), float64(y)))
	if (x < 0) != (y < 0) && z != 0 {
		z += y
	}
	return z
}

// Unary implements the operations +float and -float.
func (f Float) Unary(op syntax.Token) (Value, error) {
	switch op {
	case syntax.MINUS:
		return -f, nil
	case syntax.PLUS:
		return +f, nil
	}
	return nil, nil
}

// String is the type of a Starlark text string.
//
// A String encapsulates an immutable sequence of bytes,
// but strings are not directly iterable. Instead, iterate
// over the result of calling one of these four methods:
// codepoints, codepoint_ords, elems, elem_ords.
//
// Strings typically contain text; use Bytes for binary strings.
// The Starlark spec defines text strings as sequences of UTF-k
// codes that encode Unicode code points. In this Go implementation,
// k=8, whereas in a Java implementation, k=16. For portability,
// operations on strings should aim to avoid assumptions about
// the value of k.
//
// Warning: the contract of the Value interface's String method is that
// it returns the value printed in Starlark notation,
// so s.String() or fmt.Sprintf("%s", s) returns a quoted string.
// Use string(s) or s.GoString() or fmt.Sprintf("%#v", s) to obtain the raw contents
// of a Starlark string as a Go string.
type String string

func (s String) String() string        { return syntax.Quote(string(s), false) }
func (s String) GoString() string      { return string(s) }
func (s String) Type() string          { return "string" }
func (s String) Freeze()               {} // immutable
func (s String) Truth() Bool           { return len(s) > 0 }
func (s String) Hash() (uint32, error) { return hashString(string(s)), nil }
func (s String) Len() int              { return len(s) } // bytes
func (s String) Index(i int) Value     { return s[i : i+1] }

func (s String) Slice(start, end, step int) Value {
	if step == 1 {
		return s[start:end]
	}

	// The result is made once, of its exact size: no growth, and no copy to
	// make a string of the bytes.
	sign := signum(step)
	var str strings.Builder
	str.Grow(sliceLen(start, end, step))
	for i := start; signum(end-i) == sign; i += step {
		str.WriteByte(s[i])
	}
	return String(str.String())
}

func (s String) Attr(name string) (Value, error) { return builtinAttr(s, name, stringMethods) }
func (s String) AttrNames() []string             { return builtinAttrNames(stringMethods) }

func (x String) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(String)
	return threeway(op, strings.Compare(string(x), string(y))), nil
}

func (s String) Has(y Value) (bool, error) {
	needle, ok := y.(String)
	if !ok {
		return false, fmt.Errorf("'in <string>' requires string as left operand, not %s", y.Type())
	}
	return strings.Contains(string(s), string(needle)), nil
}

func AsString(x Value) (string, bool) { v, ok := x.(String); return string(v), ok }

// A stringElems is an iterable whose iterator yields a sequence of
// elements (bytes), either numerically or as successive substrings.
// It is an indexable sequence.
type stringElems struct {
	s    String
	ords bool
}

var (
	_ Iterable  = (*stringElems)(nil)
	_ Indexable = (*stringElems)(nil)
)

func (si stringElems) String() string {
	if si.ords {
		return si.s.String() + ".elem_ords()"
	} else {
		return si.s.String() + ".elems()"
	}
}
func (si stringElems) Type() string          { return "string.elems" }
func (si stringElems) Freeze()               {} // immutable
func (si stringElems) Truth() Bool           { return True }
func (si stringElems) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", si.Type()) }
func (si stringElems) Iterate() Iterator     { return &stringElemsIterator{si, 0} }
func (si stringElems) Len() int              { return len(si.s) }
func (si stringElems) Index(i int) Value {
	if si.ords {
		return MakeInt(int(si.s[i]))
	} else {
		// TODO(adonovan): opt: preallocate canonical 1-byte strings
		// to avoid interface allocation.
		return si.s[i : i+1]
	}
}

type stringElemsIterator struct {
	si stringElems
	i  int
}

func (it *stringElemsIterator) Next(p *Value) bool {
	if it.i == len(it.si.s) {
		return false
	}
	*p = it.si.Index(it.i)
	it.i++
	return true
}

func (*stringElemsIterator) Done() {}

// A stringCodepoints is an iterable whose iterator yields a sequence of
// Unicode code points, either numerically or as successive substrings.
// It is not indexable.
type stringCodepoints struct {
	s    String
	ords bool
}

var _ Iterable = (*stringCodepoints)(nil)

func (si stringCodepoints) String() string {
	if si.ords {
		return si.s.String() + ".codepoint_ords()"
	} else {
		return si.s.String() + ".codepoints()"
	}
}
func (si stringCodepoints) Type() string          { return "string.codepoints" }
func (si stringCodepoints) Freeze()               {} // immutable
func (si stringCodepoints) Truth() Bool           { return True }
func (si stringCodepoints) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", si.Type()) }
func (si stringCodepoints) Iterate() Iterator     { return &stringCodepointsIterator{si, 0} }

type stringCodepointsIterator struct {
	si stringCodepoints
	i  int
}

func (it *stringCodepointsIterator) Next(p *Value) bool {
	s := it.si.s[it.i:]
	if s == "" {
		return false
	}
	r, sz := utf8.DecodeRuneInString(string(s))
	if !it.si.ords {
		if r == utf8.RuneError {
			*p = String(r)
		} else {
			*p = s[:sz]
		}
	} else {
		*p = MakeInt(int(r))
	}
	it.i += sz
	return true
}

func (*stringCodepointsIterator) Done() {}

// A Function is a function defined by a Starlark def statement or lambda expression.
// The initialization behavior of a Starlark module is also represented by a Function.
type Function struct {
	funcode  *compile.Funcode
	module   *Module
	defaults Tuple
	freevars Tuple
}

// A Module represents an evaluated Starlark module.
// It is the dynamic counterpart to a [Program].
// All functions in the same program share a Module.
type Module struct {
	program     *Program
	predeclared StringDict
	globals     []Value
	constants   []Value
}

// Program returns the program from which this module was constructed.
func (m *Module) Program() *Program {
	return m.program
}

// Globals returns a new StringDict containing all global
// variables so far defined in the module.
func (m *Module) Globals() StringDict {
	r := make(StringDict, len(m.program.compiled.Globals))
	for i, id := range m.program.compiled.Globals {
		if v := m.globals[i]; v != nil {
			r[id.Name] = v
		}
	}
	return r
}

// Predeclared returns the predeclared environment used
// to construct this module.
func (m *Module) Predeclared() StringDict {
	return m.predeclared
}

func (fn *Function) Name() string          { return fn.funcode.Name } // "lambda" for anonymous functions
func (fn *Function) Doc() string           { return fn.funcode.Doc }
func (fn *Function) Hash() (uint32, error) { return hashString(fn.funcode.Name), nil }
func (fn *Function) Freeze()               { fn.defaults.Freeze(); fn.freevars.Freeze() }
func (fn *Function) String() string        { return toString(fn) }
func (fn *Function) Type() string          { return "function" }
func (fn *Function) Truth() Bool           { return true }
func (fn *Function) Module() *Module       { return fn.module }

// Globals returns a new StringDict containing all global
// variables so far defined in the function's module.
//
// fn.Globals() is equivalent to fn.Module().Globals().
func (fn *Function) Globals() StringDict { return fn.module.Globals() }

func (fn *Function) Position() syntax.Position { return fn.funcode.Pos }
func (fn *Function) NumParams() int            { return fn.funcode.NumParams }
func (fn *Function) NumKwonlyParams() int      { return fn.funcode.NumKwonlyParams }

// Param returns the name and position of the ith parameter,
// where 0 <= i < NumParams().
// The *args and **kwargs parameters are at the end
// even if there were optional parameters after *args.
func (fn *Function) Param(i int) (string, syntax.Position) {
	if i >= fn.NumParams() {
		panic(i)
	}
	id := fn.funcode.Locals[i]
	return id.Name, id.Pos
}

// ParamDefault returns the default value of the specified parameter
// (0 <= i < NumParams()), or nil if the parameter is not optional.
func (fn *Function) ParamDefault(i int) Value {
	if i < 0 || i >= fn.NumParams() {
		panic(i)
	}

	// fn.defaults omits all required params up to the first optional param. It
	// also does not include *args or **kwargs at the end.
	firstOptIdx := fn.NumParams() - len(fn.defaults)
	if fn.HasVarargs() {
		firstOptIdx--
	}
	if fn.HasKwargs() {
		firstOptIdx--
	}
	if i < firstOptIdx || i >= firstOptIdx+len(fn.defaults) {
		return nil
	}

	dflt := fn.defaults[i-firstOptIdx]
	if _, ok := dflt.(mandatory); ok {
		return nil
	}
	return dflt
}

func (fn *Function) HasVarargs() bool { return fn.funcode.HasVarargs }
func (fn *Function) HasKwargs() bool  { return fn.funcode.HasKwargs }

// NumFreeVars returns the number of free variables of this function.
func (fn *Function) NumFreeVars() int { return len(fn.funcode.FreeVars) }

// FreeVar returns the binding (name and binding position) and value
// of the i'th free variable of function fn.
func (fn *Function) FreeVar(i int) (Binding, Value) {
	return Binding(fn.funcode.FreeVars[i]), fn.freevars[i].(*cell).v
}

// A Builtin is a function implemented in Go.
type Builtin struct {
	name string
	fn   func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error)
	recv Value // for bound methods (e.g. "".startswith)

	// price is the work of the built-in in units (see prices.go), and whether
	// it accounts for its own memory: a built-in of this package has one, a
	// built-in of the host has none. Call charges the work of the price before
	// the call, and the shallow size of the result after it, unless the price
	// says that the built-in charged it itself or returned a value that
	// exists already (alloc.go). The host charges the work of its own
	// built-ins with ChargeWork and ChargeAlloc.
	price *price
}

func (b *Builtin) Name() string { return b.name }
func (b *Builtin) Freeze() {
	if b.recv != nil {
		b.recv.Freeze()
	}
}
func (b *Builtin) Hash() (uint32, error) {
	h := hashString(b.name)
	if b.recv != nil {
		h ^= 5521
	}
	return h, nil
}
func (b *Builtin) Receiver() Value { return b.recv }
func (b *Builtin) String() string  { return toString(b) }
func (b *Builtin) Type() string    { return "builtin_function_or_method" }
func (b *Builtin) CallInternal(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	return b.fn(thread, b, args, kwargs)
}
func (b *Builtin) Truth() Bool { return true }

// NewBuiltin returns a new 'builtin_function_or_method' value with the specified name
// and implementation.  It compares unequal with all other values.
func NewBuiltin(name string, fn func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error)) *Builtin {
	return &Builtin{name: name, fn: fn}
}

// BindReceiver returns a new Builtin value representing a method
// closure, that is, a built-in function bound to a receiver value.
//
// In the example below, the value of f is the string.index
// built-in method bound to the receiver value "abc":
//
//	f = "abc".index; f("a"); f("b")
//
// In the common case, the receiver is bound only during the call,
// but this still results in the creation of a temporary method closure:
//
//	"abc".index("a")
func (b *Builtin) BindReceiver(recv Value) *Builtin {
	return &Builtin{name: b.name, fn: b.fn, recv: recv, price: b.price}
}

// A *Dict represents a Starlark dictionary.
// The zero value of Dict is a valid empty dictionary.
// If you know the exact final number of entries,
// it is more efficient to call NewDict.
type Dict struct {
	ht hashtable
}

// NewDict returns a set with initial space for
// at least size insertions before rehashing.
func NewDict(size int) *Dict {
	dict := new(Dict)
	dict.ht.init(size)
	return dict
}

func (d *Dict) Clear() error                                    { return d.ht.clear() }
func (d *Dict) Delete(k Value) (v Value, found bool, err error) { return d.ht.delete(k) }
func (d *Dict) Get(k Value) (v Value, found bool, err error)    { return d.ht.lookup(k) }
func (d *Dict) Items() []Tuple                                  { return d.ht.items() }
func (d *Dict) Keys() []Value                                   { return d.ht.keys() }
func (d *Dict) Len() int                                        { return int(d.ht.len) }
func (d *Dict) Iterate() Iterator                               { return d.ht.iterate() }
func (d *Dict) SetKey(k, v Value) error                         { return d.ht.insert(k, v) }
func (d *Dict) String() string                                  { return toString(d) }
func (d *Dict) Type() string                                    { return "dict" }
func (d *Dict) Freeze()                                         { freezeTree(d) }
func (d *Dict) Truth() Bool                                     { return d.Len() > 0 }
func (d *Dict) Hash() (uint32, error)                           { return 0, fmt.Errorf("unhashable type: dict") }

func (x *Dict) Union(y *Dict) *Dict {
	z, _ := x.unionM(nil, y)
	return z
}

// unionM is Union, charging its work to m (see work.go).
func (x *Dict) unionM(m *meter, y *Dict) (*Dict, error) {
	z := new(Dict)
	z.ht.init(x.Len()) // a lower bound
	if err := z.ht.addAllM(m, &x.ht); err != nil {
		return nil, err
	}
	if err := z.ht.addAllM(m, &y.ht); err != nil {
		return nil, err
	}
	return z, nil
}

// SetKeyWork is SetKey, charging the work of the insertion (the hash of the key,
// the walk of the chain of its bucket, the entry) to the work of thread, as the
// built-ins of this package do: a built-in of the host that fills a dict from
// keys of the script calls it (lib/json does, for the objects that it decodes:
// the key is the script's, and the chain it walks is as long as the script made
// it). A nil thread is not charged. The error of a refusal is the error to
// return.
func (d *Dict) SetKeyWork(thread *Thread, k, v Value) error {
	m := thread.meter()
	if err := d.ht.insertM(&m, k, v); err != nil {
		return err
	}
	return m.flush()
}

// getM, setKeyM and deleteM are Get, SetKey and Delete, charging their work
// (the hash of the key, the chain of the bucket) to m.
func (d *Dict) getM(m *meter, k Value) (Value, bool, error) { return d.ht.lookupM(m, k) }
func (d *Dict) setKeyM(m *meter, k, v Value) error          { return d.ht.insertM(m, k, v) }
func (d *Dict) deleteM(m *meter, k Value) (Value, bool, error) {
	return d.ht.deleteM(m, k)
}

func (d *Dict) Attr(name string) (Value, error) { return builtinAttr(d, name, dictMethods) }
func (d *Dict) AttrNames() []string             { return builtinAttrNames(dictMethods) }

func (x *Dict) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(*Dict)
	switch op {
	case syntax.EQL:
		ok, err := dictsEqual(x, y, depth)
		return ok, err
	case syntax.NEQ:
		ok, err := dictsEqual(x, y, depth)
		return !ok, err
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

func dictsEqual(x, y *Dict, depth int) (bool, error) {
	if x.Len() != y.Len() {
		return false, nil
	}
	for e := x.ht.head; e != nil; e = e.next {
		key, xval := e.key, e.value

		if yval, found, _ := y.Get(key); !found {
			return false, nil
		} else if eq, err := EqualDepth(xval, yval, depth-1); err != nil {
			return false, err
		} else if !eq {
			return false, nil
		}
	}
	return true, nil
}

// A *List represents a Starlark list value.
type List struct {
	elems     []Value
	frozen    bool
	itercount uint32 // number of active iterators (ignored if frozen)
}

// NewList returns a list containing the specified elements.
// Callers should not subsequently modify elems.
func NewList(elems []Value) *List { return &List{elems: elems} }

func (l *List) Freeze() { freezeTree(l) }

// checkMutable reports an error if the list should not be mutated.
// verb+" list" should describe the operation.
func (l *List) checkMutable(verb string) error {
	if l.frozen {
		return fmt.Errorf("cannot %s frozen list", verb)
	}
	if l.itercount > 0 {
		return fmt.Errorf("cannot %s list during iteration", verb)
	}
	return nil
}

func (l *List) String() string        { return toString(l) }
func (l *List) Type() string          { return "list" }
func (l *List) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: list") }
func (l *List) Truth() Bool           { return l.Len() > 0 }
func (l *List) Len() int              { return len(l.elems) }
func (l *List) Index(i int) Value     { return l.elems[i] }

func (l *List) Slice(start, end, step int) Value {
	if step == 1 {
		elems := append([]Value{}, l.elems[start:end]...)
		return NewList(elems)
	}

	sign := signum(step)
	list := make([]Value, 0, sliceLen(start, end, step))
	for i := start; signum(end-i) == sign; i += step {
		list = append(list, l.elems[i])
	}
	return NewList(list)
}

func (l *List) Attr(name string) (Value, error) { return builtinAttr(l, name, listMethods) }
func (l *List) AttrNames() []string             { return builtinAttrNames(listMethods) }

func (l *List) Iterate() Iterator {
	if !l.frozen {
		l.itercount++
	}
	return &listIterator{l: l}
}

func (l *List) Has(y Value) (bool, error) {
	for _, x := range l.elems {
		if eq, err := Equal(x, y); err != nil {
			return false, err
		} else if eq {
			return true, nil
		}
	}
	return false, nil
}

func (x *List) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(*List)
	// It's tempting to check x == y as an optimization here,
	// but wrong because a list containing NaN is not equal to itself.
	return sliceCompareM(nil, op, x.elems, y.elems, depth)
}

func sliceCompare(op syntax.Token, x, y []Value, depth int) (bool, error) {
	// Fast path: check length.
	if len(x) != len(y) && (op == syntax.EQL || op == syntax.NEQ) {
		return op == syntax.NEQ, nil
	}

	// Find first element that is not equal in both lists.
	for i := 0; i < len(x) && i < len(y); i++ {
		if eq, err := EqualDepth(x[i], y[i], depth-1); err != nil {
			return false, err
		} else if !eq {
			switch op {
			case syntax.EQL:
				return false, nil
			case syntax.NEQ:
				return true, nil
			default:
				return CompareDepth(op, x[i], y[i], depth-1)
			}
		}
	}

	return threeway(op, len(x)-len(y)), nil
}

type listIterator struct {
	l *List
	i int
}

func (it *listIterator) Next(p *Value) bool {
	if it.i < it.l.Len() {
		*p = it.l.elems[it.i]
		it.i++
		return true
	}
	return false
}

func (it *listIterator) Done() {
	if !it.l.frozen {
		it.l.itercount--
	}
}

func (l *List) SetIndex(i int, v Value) error {
	if err := l.checkMutable("assign to element of"); err != nil {
		return err
	}
	l.elems[i] = v
	return nil
}

func (l *List) Append(v Value) error {
	if err := l.checkMutable("append to"); err != nil {
		return err
	}
	l.elems = append(l.elems, v)
	return nil
}

func (l *List) Clear() error {
	if err := l.checkMutable("clear"); err != nil {
		return err
	}
	for i := range l.elems {
		l.elems[i] = nil // aid GC
	}
	l.elems = l.elems[:0]
	return nil
}

// A Tuple represents a Starlark tuple value.
type Tuple []Value

func (t Tuple) Len() int          { return len(t) }
func (t Tuple) Index(i int) Value { return t[i] }

func (t Tuple) Slice(start, end, step int) Value {
	if step == 1 {
		return t[start:end]
	}

	sign := signum(step)
	tuple := make(Tuple, 0, sliceLen(start, end, step))
	for i := start; signum(end-i) == sign; i += step {
		tuple = append(tuple, t[i])
	}
	return tuple
}

func (t Tuple) Iterate() Iterator { return &tupleIterator{elems: t} }

func (t Tuple) Freeze()        { freezeTree(t) }
func (t Tuple) String() string { return toString(t) }
func (t Tuple) Type() string   { return "tuple" }
func (t Tuple) Truth() Bool    { return len(t) > 0 }

func (x Tuple) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Tuple)
	return sliceCompareM(nil, op, x, y, depth)
}

func (t Tuple) Has(y Value) (bool, error) {
	for _, x := range t {
		if eq, err := Equal(x, y); err != nil {
			return false, err
		} else if eq {
			return true, nil
		}
	}
	return false, nil
}

func (t Tuple) Hash() (uint32, error) { return t.hashM(nil, 0) }

type tupleIterator struct{ elems Tuple }

func (it *tupleIterator) Next(p *Value) bool {
	if len(it.elems) > 0 {
		*p = it.elems[0]
		it.elems = it.elems[1:]
		return true
	}
	return false
}

func (it *tupleIterator) Done() {}

// A Set represents a Starlark set value.
// The zero value of Set is a valid empty set.
// If you know the exact final number of elements,
// it is more efficient to call NewSet.
type Set struct {
	ht hashtable // values are all None
}

// NewSet returns a dictionary with initial space for
// at least size insertions before rehashing.
func NewSet(size int) *Set {
	set := new(Set)
	set.ht.init(size)
	return set
}

func (s *Set) Delete(k Value) (found bool, err error) { _, found, err = s.ht.delete(k); return }

// hasM, insertM and deleteM are Has, Insert and Delete, charging their work to m.
func (s *Set) hasM(m *meter, k Value) (found bool, err error) {
	_, found, err = s.ht.lookupM(m, k)
	return
}
func (s *Set) insertM(m *meter, k Value) error { return s.ht.insertM(m, k, None) }
func (s *Set) deleteM(m *meter, k Value) (found bool, err error) {
	_, found, err = s.ht.deleteM(m, k)
	return
}
func (s *Set) Clear() error                        { return s.ht.clear() }
func (s *Set) Has(k Value) (found bool, err error) { _, found, err = s.ht.lookup(k); return }
func (s *Set) Insert(k Value) error                { return s.ht.insert(k, None) }
func (s *Set) Len() int                            { return int(s.ht.len) }
func (s *Set) Iterate() Iterator                   { return s.ht.iterate() }
func (s *Set) String() string                      { return toString(s) }
func (s *Set) Type() string                        { return "set" }
func (s *Set) Freeze()                             { freezeTree(s) }
func (s *Set) Hash() (uint32, error)               { return 0, fmt.Errorf("unhashable type: set") }
func (s *Set) Truth() Bool                         { return s.Len() > 0 }

func (s *Set) Attr(name string) (Value, error) { return builtinAttr(s, name, setMethods) }
func (s *Set) AttrNames() []string             { return builtinAttrNames(setMethods) }

func (x *Set) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(*Set)
	switch op {
	case syntax.EQL:
		ok, err := setsEqual(x, y, depth)
		return ok, err
	case syntax.NEQ:
		ok, err := setsEqual(x, y, depth)
		return !ok, err
	case syntax.GE: // superset
		if x.Len() < y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.IsSuperset(iter)
	case syntax.LE: // subset
		if x.Len() > y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.IsSubset(iter)
	case syntax.GT: // proper superset
		if x.Len() <= y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.IsSuperset(iter)
	case syntax.LT: // proper subset
		if x.Len() >= y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.IsSubset(iter)
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

func setsEqual(x, y *Set, depth int) (bool, error) {
	if x.Len() != y.Len() {
		return false, nil
	}
	for e := x.ht.head; e != nil; e = e.next {
		if found, _ := y.Has(e.key); !found {
			return false, nil
		}
	}
	return true, nil
}

func setFromIterator(iter Iterator) (*Set, error) {
	var x Value
	set := new(Set)
	for iter.Next(&x) {
		err := set.Insert(x)
		if err != nil {
			return set, err
		}
	}
	return set, nil
}

func (s *Set) clone() *Set {
	set, _ := s.cloneM(nil)
	return set
}

func (s *Set) cloneM(m *meter) (*Set, error) {
	set := new(Set)
	for e := s.ht.head; e != nil; e = e.next {
		if err := set.insertM(m, e.key); err != nil { // (can't fail otherwise)
			return nil, err
		}
	}
	return set, nil
}

func (s *Set) Union(iter Iterator) (Value, error) { return s.unionM(nil, iter) }

func (s *Set) unionM(m *meter, iter Iterator) (Value, error) {
	set, err := s.cloneM(m)
	if err != nil {
		return nil, err
	}
	var x Value
	for iter.Next(&x) {
		if err := set.insertM(m, x); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func (s *Set) InsertAll(iter Iterator) error { return s.insertAllM(nil, iter) }

func (s *Set) insertAllM(m *meter, iter Iterator) error {
	var x Value
	for iter.Next(&x) {
		if err := s.insertM(m, x); err != nil {
			return err
		}
	}
	return nil
}

func (s *Set) Difference(other Iterator) (Value, error) { return s.differenceM(nil, other) }

func (s *Set) differenceM(m *meter, other Iterator) (Value, error) {
	diff, err := s.cloneM(m)
	if err != nil {
		return nil, err
	}
	var x Value
	for other.Next(&x) {
		if _, err := diff.deleteM(m, x); err != nil {
			return nil, err
		}
	}
	return diff, nil
}

func (s *Set) IsSuperset(other Iterator) (bool, error) { return s.isSupersetM(nil, other) }

func (s *Set) isSupersetM(m *meter, other Iterator) (bool, error) {
	var x Value
	for other.Next(&x) {
		found, err := s.hasM(m, x)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func (s *Set) IsSubset(other Iterator) (bool, error) { return s.isSubsetM(nil, other) }

func (s *Set) isSubsetM(m *meter, other Iterator) (bool, error) {
	if count, err := s.ht.countM(m, other); err != nil {
		return false, err
	} else {
		return count == s.Len(), nil
	}
}

func (s *Set) Intersection(other Iterator) (Value, error) { return s.intersectionM(nil, other) }

func (s *Set) intersectionM(m *meter, other Iterator) (Value, error) {
	intersect := new(Set)
	var x Value
	for other.Next(&x) {
		found, err := s.hasM(m, x)
		if err != nil {
			return nil, err
		}
		if found {
			err = intersect.insertM(m, x)
			if err != nil {
				return nil, err
			}
		}
	}
	return intersect, nil
}

func (s *Set) SymmetricDifference(other Iterator) (Value, error) {
	return s.symmetricDifferenceM(nil, other)
}

func (s *Set) symmetricDifferenceM(m *meter, other Iterator) (Value, error) {
	diff, err := s.cloneM(m)
	if err != nil {
		return nil, err
	}
	var x Value
	for other.Next(&x) {
		found, err := diff.deleteM(m, x)
		if err != nil {
			return nil, err
		}
		if !found {
			// (an element that cannot be inserted, one that is not hashable
			// when the receiver is empty, is left out, as in v0.2.0)
			if err := diff.insertM(m, x); err != nil && isResourceError(err) {
				return nil, err
			}
		}
	}
	return diff, nil
}

// A tupleKey identifies a tuple by its first element and its length.
type tupleKey struct {
	p *Value
	n int
}

// onlyImmutable reports whether every element of the tuple is a value of a
// type that has nothing to freeze.
func onlyImmutable(t Tuple) bool {
	for _, v := range t {
		switch v.(type) {
		case nil, NoneType, Bool, Int, Float, String, Bytes:
		default:
			return false
		}
	}
	return true
}

// freezeTree freezes root and everything reachable from it through lists,
// tuples, dicts and sets. It uses an explicit stack of the containers being
// walked, not recursion: a value nested 1.4 million deep (t = (t,) in a loop)
// would overflow the Go stack, which is fatal. The stack holds one frame per
// level, and allocates nothing for a flat container. Other values (a host
// type, a function, a cell) are frozen by their own Freeze method.
func freezeTree(root Value) {
	type frame struct {
		elems []Value // the rest of a list or tuple, or
		e     *entry  // the next entry of a dict or set,
		val   bool    // whose value is the next to visit
	}
	var stack []frame
	var seen map[tupleKey]struct{}
	visit := func(v Value) {
		switch v := v.(type) {
		case nil, NoneType, Bool, Int, Float, String, Bytes:
			// immutable
		case *List:
			if !v.frozen {
				v.frozen = true
				if len(v.elems) > 0 {
					stack = append(stack, frame{elems: v.elems})
				}
			}
		case Tuple:
			// A tuple has no frozen flag, so a shared tuple (t = (t, t) repeated)
			// would be walked once for each path to it: exponential. Walk each
			// distinct tuple once; one with only immutable elements needs no walk.
			if len(v) > 0 && !onlyImmutable(v) {
				key := tupleKey{&v[0], len(v)}
				if _, done := seen[key]; !done {
					if seen == nil {
						seen = make(map[tupleKey]struct{})
					}
					seen[key] = struct{}{}
					stack = append(stack, frame{elems: v})
				}
			}
		case *Dict:
			if !v.ht.frozen {
				v.ht.frozen = true
				if v.ht.head != nil {
					stack = append(stack, frame{e: v.ht.head})
				}
			}
		case *Set:
			if !v.ht.frozen {
				v.ht.frozen = true
				if v.ht.head != nil {
					stack = append(stack, frame{e: v.ht.head})
				}
			}
		default:
			v.Freeze()
		}
	}
	visit(root)
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		var next Value
		switch {
		case top.e == nil && len(top.elems) == 0:
			stack = stack[:len(stack)-1]
			continue
		case top.e != nil:
			if !top.val {
				next = top.e.key
				top.val = true
			} else {
				next = top.e.value
				top.val = false
				top.e = top.e.next
			}
		default:
			next = top.elems[0]
			top.elems = top.elems[1:]
		}
		visit(next) // may grow stack: top is not used after
	}
}

// writeValueOverflowMark ends a string form that hit the size bound.
const writeValueOverflowMark = "...<truncated at size limit>"

// writeValueDeepMark replaces a value nested deeper than MaxValueDepth.
const writeValueDeepMark = "...<nested too deeply>"

// MaxValueDepth is the deepest nesting of containers that the recursive
// operations of this package (the string form, Hash, Freeze, json) descend
// into. Go's stack is 1 GiB and these functions take ~700 bytes a level, so a
// value nested 1.4 million deep (t = (t,) in a loop: 6 steps a level, well
// within the step limit) is a "stack overflow", which is fatal and cannot be
// recovered. At 10000 levels (7 MiB of stack) no legitimate value is
// refused: a proto message, a JSON document or a state tree is a few dozen
// levels deep, and the engine's own limit on state depth is far lower.
const MaxValueDepth = 10000

// MaxIntDigits is the number of decimal digits of the largest integer that is
// converted to a string, or parsed from one: int(s), str(n), repr(n), %d, {},
// json. The conversion is quadratic in the digits, and one call of a built-in
// is a step or two: int("9" * 1000000) took 4 s and 12 steps. The integers of a
// program (amounts, identifiers, timestamps, counters) have a few dozen digits;
// Python's default limit is the same 4300. An integer longer than this is
// still an integer: it can be computed with, compared and hashed; only its
// conversion to and from a string is refused.
const MaxIntDigits = 4300

// MaxIntBits is the number of bits of MaxIntDigits decimal digits.
const MaxIntBits = 14285

const maxIntBits = MaxIntBits

// errValueLimit is the size of the form of a value (of a name, a literal) in
// the text of an error under which it is printed whole, as it was in v0.2.0: a
// key, a name, a literal of up to this many bytes is in the message as it is.
// A longer one is cut to this many bytes and marked, since the message must not
// be as large as the value, which can be megabytes (see errValue, errStr).
const errValueLimit = 256

// toString returns the string form of value v.
// It may be more efficient than v.String() for larger values.
func toString(v Value) string {
	var out sink
	w := valueWriter{out: &out, limit: maxAlloc}
	w.write(v, 0)
	return out.String()
}

// errValue returns the string form of v for an error message: the form itself
// if it is of at most errValueLimit bytes, and else its first errValueLimit
// bytes, marked. The text of an error that embeds a value of the script (a dict
// key, a string that fails to parse) must not be as large as the value, which
// can be megabytes.
func errValue(v Value) string {
	var out sink
	// (the writer refuses a form of the limit's size: allow one byte more)
	w := valueWriter{out: &out, limit: errValueLimit + 1, cut: true}
	w.write(v, 0)
	return out.String()
}

// errStr returns s cut to errValueLimit bytes and marked, for an error
// message that embeds a string of the script (a name, a literal that fails
// to parse).
func errStr(s string) string {
	if len(s) <= errValueLimit {
		return s
	}
	cut := errValueLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...<" + strconv.Itoa(len(s)) + " bytes>"
}

// The results of writeValueLimit.
const (
	writeOK     = iota // the whole form was written
	writeLimit         // the form reached the limit, or a leaf would have
	writeDeep          // the value is nested deeper than MaxValueDepth
	writeStop          // the thread has used up its steps (the error of the meter)
	writeBigInt        // a big integer too long to convert to a string (maxIntBits)
)

// writeValue writes x to out.
//
// path is used to detect cycles.
// It contains the list of *List and *Dict values we're currently printing.
// (These are the only potentially cyclic structures.)
// Callers should generally pass nil for path.
// It is safe to re-use the same path slice for multiple calls.
//
// The form is bounded by maxAlloc (and by MaxValueDepth): where it is cut it
// ends with a mark, since this function reports no error (it implements the
// String method of values).
func writeValue(out *strings.Builder, x Value, path []Value) {
	writeValueLimit(out, x, path, maxAlloc)
}

// writeValueLimit is writeValue with an explicit size limit. It stops
// descending once out holds limit bytes or more, and does not write a leaf
// (a string, bytes or big int) that would take it there, so that the form is
// never built larger than limit. A caller that can report errors treats a
// result other than writeOK as too large (writeLimit), or too deep.
// limit is at most maxAlloc, and a thread's stringLimit when the form is to
// be charged to its budget.
func writeValueLimit(out *strings.Builder, x Value, path []Value, limit int) int {
	s := sink{ext: out, n: out.Len()}
	w := valueWriter{out: &s, limit: limit}
	w.write(x, 0)
	return w.result
}

// writeValueMeter is writeValueLimit, charging the work of the form to m (a
// leaf by its bytes, a container by its elements, a big integer by its digits
// squared). The error is that of a thread whose steps are used up; the form is
// then incomplete.
func writeValueMeter(out *sink, x Value, limit int, m *meter) (int, error) {
	w := valueWriter{out: out, limit: limit, m: m}
	w.write(x, 0)
	return w.result, w.err
}

type valueWriter struct {
	out    *sink
	limit  int
	cut    bool // cut a leaf that does not fit, instead of refusing it
	result int
	m      *meter // charged with the work; nil: none
	err    error  // the error of m

	// The lists and dicts that are being written, to detect a cycle: a slice
	// for the first levels, and a set below them, so that a cycle check is not
	// linear in the depth (which would make the form of a deep value
	// quadratic).
	path    []Value
	pathSet map[Value]struct{}
}

// pathSetDepth is the depth from which the cycle check uses a set.
const pathSetDepth = 32

func (w *valueWriter) onPath(x Value) bool {
	if w.pathSet != nil {
		_, ok := w.pathSet[x]
		return ok
	}
	return pathContains(w.path, x)
}

func (w *valueWriter) push(x Value) {
	if len(w.path) >= pathSetDepth {
		w.work(8) // below the first levels the container is in a map
	}
	w.path = append(w.path, x)
	if len(w.path) == pathSetDepth {
		w.pathSet = make(map[Value]struct{}, 2*pathSetDepth)
		for _, v := range w.path {
			w.pathSet[v] = struct{}{}
		}
	} else if w.pathSet != nil {
		w.pathSet[x] = struct{}{}
	}
}

func (w *valueWriter) pop() {
	x := w.path[len(w.path)-1]
	w.path = w.path[:len(w.path)-1]
	if w.pathSet != nil {
		delete(w.pathSet, x)
	}
}

// work charges n units to the meter, and stops the form if the thread has
// used up its steps.
func (w *valueWriter) work(n uint64) {
	if w.m == nil || w.err != nil {
		return
	}
	if err := w.m.add(n); err != nil {
		w.err = err
		w.result = writeStop
	}
}

// fits reports whether n more bytes keep the form below the limit.
func (w *valueWriter) fits(n int) bool { return w.out.Len()+n < w.limit }

// full records that the limit was reached and marks the form (once).
func (w *valueWriter) full() {
	if w.result == writeOK {
		w.result = writeLimit
	}
	if !w.out.recentMark(writeValueOverflowMark) {
		w.out.writeMark(writeValueOverflowMark)
	}
}

// leaf writes the string form s of a leaf of known length n if it fits.
//
// exact says that n is the length of what emit writes, so that a pass that
// only counts need not write it.
func (w *valueWriter) leaf(n int, exact bool, emit func(), cut func(room int) string) {
	switch {
	case w.fits(n):
		w.work(workSlow(n))
		if exact {
			w.out.room(n)
			if w.out.counting {
				w.out.n += n
				return
			}
		}
		emit()
	case w.cut:
		if room := w.limit - 1 - w.out.Len(); room > 0 { // (the limit is one more than the form that is cut to)
			w.out.WriteString(cut(room))
		}
		w.full()
	default:
		w.full()
	}
}

func (w *valueWriter) write(x Value, depth int) {
	if w.result != writeOK && w.result != writeLimit {
		return
	}
	if w.out.Len() >= w.limit {
		w.full()
		return
	}
	if depth > MaxValueDepth {
		w.result = writeDeep
		w.out.WriteString(writeValueDeepMark)
		return
	}
	switch x := x.(type) {
	case nil:
		w.out.WriteString("<nil>") // indicates a bug

	// These four cases are duplicates of T.String(), for efficiency.
	case NoneType:
		w.out.WriteString("None")

	case Int:
		if _, big := x.get(); big != nil {
			if big.BitLen() > maxIntBits {
				w.result = writeBigInt
				w.out.WriteString("<int of " + strconv.Itoa(big.BitLen()) + " bits>")
				return
			}
			// The conversion is quadratic in the digits.
			d := uint64(big.BitLen()/3 + 1)
			w.work(d * d / 4096)
			// The decimal form has at most BitLen/3+1 digits.
			n := big.BitLen()/3 + 2
			w.leaf(n, false, func() { w.out.WriteString(x.String()) }, func(room int) string { return "<int of " + strconv.Itoa(big.BitLen()) + " bits>" })
		} else {
			var tmp [24]byte
			iSmall, _ := x.get()
			w.out.Write(strconv.AppendInt(tmp[:0], iSmall, 10))
		}

	case Float:
		var tmp [40]byte
		w.out.Write(appendFloatG(tmp[:0], x))

	case Bool:
		if x {
			w.out.WriteString("True")
		} else {
			w.out.WriteString("False")
		}

	case String:
		w.leaf(syntax.QuoteLen(string(x), false), true, func() { w.out.writeQuoted(string(x), false) },
			func(room int) string { return quotedPrefix(string(x), false, room) })

	case Bytes:
		w.leaf(syntax.QuoteLen(string(x), true), true, func() { w.out.writeQuoted(string(x), true) },
			func(room int) string { return quotedPrefix(string(x), true, room) })

	case *List:
		w.out.WriteByte('[')
		if w.onPath(x) {
			w.out.WriteString("...") // list contains itself
		} else {
			w.push(x)
			for i, elem := range x.elems {
				if i > 0 {
					w.out.WriteString(", ")
				}
				w.work(4)
				w.write(elem, depth+1)
				if w.result != writeOK {
					return
				}
			}
			w.pop()
		}
		w.out.WriteByte(']')

	case Tuple:
		w.out.WriteByte('(')
		for i, elem := range x {
			if i > 0 {
				w.out.WriteString(", ")
			}
			w.work(4)
			w.write(elem, depth+1)
			if w.result != writeOK {
				return
			}
		}
		if len(x) == 1 {
			w.out.WriteByte(',')
		}
		w.out.WriteByte(')')

	case *Function:
		w.out.WriteString("<function " + x.Name() + ">")

	case *Builtin:
		if x.recv != nil {
			w.out.WriteString("<built-in method " + x.Name() + " of " + x.recv.Type() + " value>")
		} else {
			w.out.WriteString("<built-in function " + x.Name() + ">")
		}

	case *Dict:
		w.out.WriteByte('{')
		if w.onPath(x) {
			w.out.WriteString("...") // dict contains itself
		} else {
			sep := ""
			w.push(x) // (the original pushed x for the values only; the keys are hashable)
			for e := x.ht.head; e != nil; e = e.next {
				k, v := e.key, e.value
				w.out.WriteString(sep)
				w.work(8)
				w.write(k, depth+1)
				w.out.WriteString(": ")
				w.write(v, depth+1)
				if w.result != writeOK {
					return
				}
				sep = ", "
			}
			w.pop()
		}
		w.out.WriteByte('}')

	case *Set:
		w.out.WriteString("set([")
		for e := x.ht.head; e != nil; e = e.next {
			if e != x.ht.head {
				w.out.WriteString(", ")
			}
			w.work(4)
			w.write(e.key, depth+1)
			if w.result != writeOK {
				return
			}
		}
		w.out.WriteString("])")

	default:
		w.out.WriteString(x.String())
	}
}

func pathContains(path []Value, x Value) bool {
	return slices.Contains(path, x)
}

// CompareLimit is the depth limit on recursive comparison operations such as == and <.
// Comparison of data structures deeper than this limit may fail.
var CompareLimit = 10

// Equal reports whether two Starlark values are equal.
func Equal(x, y Value) (bool, error) {
	if x, ok := x.(String); ok {
		return x == y, nil // fast path for an important special case
	}
	return EqualDepth(x, y, CompareLimit)
}

// EqualDepth reports whether two Starlark values are equal.
//
// Recursive comparisons by implementations of Value.CompareSameType
// should use EqualDepth to prevent infinite recursion.
func EqualDepth(x, y Value, depth int) (bool, error) {
	return CompareDepth(syntax.EQL, x, y, depth)
}

// Compare compares two Starlark values.
// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
// Compare returns an error if an ordered comparison was
// requested for a type that does not support it.
//
// Recursive comparisons by implementations of Value.CompareSameType
// should use CompareDepth to prevent infinite recursion.
func Compare(op syntax.Token, x, y Value) (bool, error) {
	return CompareDepth(op, x, y, CompareLimit)
}

// CompareDepth compares two Starlark values.
// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
// CompareDepth returns an error if an ordered comparison was
// requested for a pair of values that do not support it.
//
// The depth parameter limits the maximum depth of recursion
// in cyclic data structures.
func CompareDepth(op syntax.Token, x, y Value, depth int) (bool, error) {
	return compareM(nil, op, x, y, depth)
}

func sameType(x, y Value) bool {
	return reflect.TypeOf(x) == reflect.TypeOf(y) || x.Type() == y.Type()
}

// threeway interprets a three-way comparison value cmp (-1, 0, +1)
// as a boolean comparison (e.g. x < y).
func threeway(op syntax.Token, cmp int) bool {
	switch op {
	case syntax.EQL:
		return cmp == 0
	case syntax.NEQ:
		return cmp != 0
	case syntax.LE:
		return cmp <= 0
	case syntax.LT:
		return cmp < 0
	case syntax.GE:
		return cmp >= 0
	case syntax.GT:
		return cmp > 0
	}
	panic(op)
}

func b2i(b bool) int {
	if b {
		return 1
	} else {
		return 0
	}
}

// Len returns the length of a string or sequence value,
// and -1 for all others.
//
// Warning: Len(x) >= 0 does not imply Iterate(x) != nil.
// A string has a known length but is not directly iterable.
func Len(x Value) int {
	switch x := x.(type) {
	case String:
		return x.Len()
	case Indexable:
		return x.Len()
	case Sequence:
		return x.Len()
	}
	return -1
}

// Iterate return a new iterator for the value if iterable, nil otherwise.
// If the result is non-nil, the caller must call Done when finished with it.
//
// Warning: Iterate(x) != nil does not imply Len(x) >= 0.
// Some iterables may have unknown length.
func Iterate(x Value) Iterator {
	if x, ok := x.(Iterable); ok {
		return x.Iterate()
	}
	return nil
}

// Bytes is the type of a Starlark binary string.
//
// A Bytes encapsulates an immutable sequence of bytes.
// It is comparable, indexable, and sliceable, but not directly iterable;
// use bytes.elems() for an iterable view.
//
// In this Go implementation, the elements of 'string' and 'bytes' are
// both bytes, but in other implementations, notably Java, the elements
// of a 'string' are UTF-16 codes (Java chars). The spec abstracts text
// strings as sequences of UTF-k codes that encode Unicode code points,
// and operations that convert from text to binary incur UTF-k-to-UTF-8
// transcoding; conversely, conversion from binary to text incurs
// UTF-8-to-UTF-k transcoding. Because k=8 for Go, these operations
// are the identity function, at least for valid encodings of text.
type Bytes string

var (
	_ Comparable = Bytes("")
	_ Sliceable  = Bytes("")
	_ Indexable  = Bytes("")
	_ Container  = Bytes("")
)

func (b Bytes) String() string        { return syntax.Quote(string(b), true) }
func (b Bytes) Type() string          { return "bytes" }
func (b Bytes) Freeze()               {} // immutable
func (b Bytes) Truth() Bool           { return len(b) > 0 }
func (b Bytes) Hash() (uint32, error) { return String(b).Hash() }
func (b Bytes) Len() int              { return len(b) }
func (b Bytes) Index(i int) Value     { return b[i : i+1] }

func (b Bytes) Attr(name string) (Value, error) { return builtinAttr(b, name, bytesMethods) }
func (b Bytes) AttrNames() []string             { return builtinAttrNames(bytesMethods) }

func (b Bytes) Slice(start, end, step int) Value {
	if step == 1 {
		return b[start:end]
	}

	sign := signum(step)
	var str strings.Builder
	str.Grow(sliceLen(start, end, step))
	for i := start; signum(end-i) == sign; i += step {
		str.WriteByte(b[i])
	}
	return Bytes(str.String())
}

func (x Bytes) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Bytes)
	return threeway(op, strings.Compare(string(x), string(y))), nil
}

func (b Bytes) Has(y Value) (bool, error) {
	switch needle := y.(type) {
	case Bytes:
		return strings.Contains(string(b), string(needle)), nil
	case Int:
		var by byte
		if err := AsInt(needle, &by); err != nil {
			return false, fmt.Errorf("int in bytes: %s", err)
		}
		return strings.IndexByte(string(b), by) >= 0, nil
	default:
		return false, fmt.Errorf("'in bytes' requires bytes or int as left operand, not %s", y.Type())
	}
}

// quotedPrefix is the first room bytes (or fewer, to end at the start of a
// character) of syntax.Quote(s, bytes), made without quoting the whole of s.
func quotedPrefix(s string, bytes bool, room int) string {
	buf := make([]byte, 0, min(room, len(s)*4)+16)
	if bytes {
		buf = append(buf, 'b')
	}
	buf = append(buf, '"')
	for len(s) > 0 && len(buf) <= room {
		_, w := utf8.DecodeRuneInString(s)
		buf = syntax.AppendQuoted(buf, s[:w])
		s = s[w:]
	}
	if len(buf) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(buf[cut]) {
			cut--
		}
		buf = buf[:cut]
	}
	return string(buf)
}
