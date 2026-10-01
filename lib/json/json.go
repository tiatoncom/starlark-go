// Copyright 2020 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package json defines utilities for converting Starlark values
// to/from JSON strings. The most recent IETF standard for JSON is
// https://www.ietf.org/rfc/rfc7159.txt.
package json // import "go.starlark.net/lib/json"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// Module json is a Starlark module of JSON-related functions.
//
//	json = module(
//	   encode,
//	   encode_indent,
//	   decode,
//	   indent,
//	)
//
// def encode(x):
//
// The encode function accepts one required positional argument,
// which it converts to JSON by cases:
//   - A Starlark value that implements Go's standard json.Marshal
//     interface defines its own JSON encoding.
//   - None, True, and False are converted to null, true, and false, respectively.
//   - Starlark int values, no matter how large, are encoded as decimal integers.
//     Some decoders may not be able to decode very large integers.
//   - Starlark float values are encoded using decimal point notation,
//     even if the value is an integer.
//     It is an error to encode a non-finite floating-point value.
//   - Starlark strings are encoded as JSON strings, using UTF-16 escapes.
//   - a Starlark IterableMapping (e.g. dict) is encoded as a JSON object.
//     It is an error if any key is not a string.
//   - any other Starlark Iterable (e.g. list, tuple) is encoded as a JSON array.
//   - a Starlark HasAttrs (e.g. struct) is encoded as a JSON object.
//
// It an application-defined type matches more than one the cases describe above,
// (e.g. it implements both Iterable and HasFields), the first case takes precedence.
// Encoding any other value yields an error.
//
// def encode_indent(x, *, prefix="", indent="\t"):
//
// Equivalent to json.indent(json.encode(x), prefix=prefix, indent=indent).
//
// def decode(x[, default]):
//
// The decode function has one required positional parameter, a JSON string.
// It returns the Starlark value that the string denotes.
//   - Numbers are parsed as int or float, depending on whether they
//     contain a decimal point.
//   - JSON objects are parsed as new unfrozen Starlark dicts.
//   - JSON arrays are parsed as new unfrozen Starlark lists.
//
// If x is not a valid JSON string, the behavior depends on the "default"
// parameter: if present, Decode returns its value; otherwise, Decode fails.
//
// def indent(str, *, prefix="", indent="\t"):
//
// The indent function pretty-prints a valid JSON encoding,
// and returns a string containing the indented form.
// It accepts one required positional parameter, the JSON string,
// and two optional keyword-only string parameters, prefix and indent,
// that specify a prefix of each new line, and the unit of indentation.
//
// Allocation: the string that encode, encode_indent and indent return, and the
// value that decode returns, are charged to the thread's allocation budget
// (starlark.Thread.SetMaxAllocBytes) before they are built, and each stops as
// soon as its result would exceed the budget or the limit of one operation:
// encode of a value with shared substructure (x = [x, x] repeated) has an
// output exponential in the number of repetitions, and indent with a long
// indent string has an output of depth times that string per line. The size
// of a string or number is computed before it is written. A decoded string is
// charged by its length, a list and a dict by the formulas of
// starlark.ListAllocBytes and DictAllocBytes (an empty one is not free), a big
// integer by its digits.
//
// Nesting: encode and decode refuse a value nested more than
// starlark.MaxValueDepth levels, with an error that is not turned into
// decode's default value: the recursion would overflow the Go stack, which is
// fatal.
var Module = &starlarkstruct.Module{
	Name: "json",
	Members: starlark.StringDict{
		"encode":        starlark.NewBuiltin("json.encode", encode),
		"encode_indent": starlark.NewBuiltin("json.encode_indent", encodeIndent),
		"decode":        starlark.NewBuiltin("json.decode", decode),
		"indent":        starlark.NewBuiltin("json.indent", indent),
	},
}

// A workMeter accumulates the work of one call of a function of this module,
// in units of work (see starlark.Thread.ChargeWork), and charges it to the
// thread as it grows, so that a long call is stopped near the limit. It is the
// meter of the starlark package, for a function that is not in it.
type workMeter struct {
	th      *starlark.Thread
	units   uint64
	charged uint64
}

const flushWork = 1024

func (m *workMeter) add(n uint64) error {
	if m == nil {
		return nil
	}
	m.units += n
	if m.units-m.charged >= flushWork {
		return m.flush()
	}
	return nil
}

func (m *workMeter) flush() error {
	if m == nil {
		return nil
	}
	n := m.units - m.charged
	m.charged = m.units
	return m.th.ChargeWork(n)
}

func encode(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var x starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	work := workMeter{th: thread} // one unit a node, a quarter a byte quoted

	// The output is written twice if it is large (see outBuf): the first
	// pass counts its bytes, charges them, and enforces the headroom, and
	// allocates nothing; the second writes into a buffer of the exact size.
	out := &outBuf{probe: formProbe}
	var garbage uint64 // bytes of the items of the dictionaries (see encodeTo)
	stop, err := encodeTo(thread, x, out, &work, thread.AllocHeadroom(), &garbage)
	if err != nil {
		if stop != nil {
			return nil, allocErr(b, stop)
		}
		return nil, fmt.Errorf("%s: %v", b.Name(), err)
	}
	n := out.n
	if err := thread.ChargeAlloc(uint64(n)); err != nil {
		return nil, allocErr(b, err)
	}
	if err := work.add(uint64(n) / 4); err != nil { // the quoting
		return nil, err
	}
	if err := work.flush(); err != nil {
		return nil, err
	}
	if !out.counting {
		return starlark.String(out.b.String()), nil
	}
	// The second pass makes the items again.
	if err := thread.ChargeAlloc(garbage); err != nil {
		return nil, allocErr(b, err)
	}
	w := &outBuf{}
	w.b.Grow(n)
	if _, err := encodeTo(thread, x, w, nil, math.MaxUint64, nil); err != nil {
		return nil, fmt.Errorf("%s: %v", b.Name(), err)
	}
	if w.n != n {
		return nil, fmt.Errorf("%s: internal error: %d bytes were counted as %d", b.Name(), w.n, n)
	}
	return starlark.String(w.b.String()), nil
}

// encodeTo writes the JSON encoding of x to buf. work is the meter of the
// first pass (nil in the second, whose work is the first's); headroom the
// size at which the output is refused (stop is then the error: the budget's);
// garbage is the first pass's, which charges the items of the dictionaries it
// makes (they are allocated, and garbage at once) and adds them up.
func encodeTo(thread *starlark.Thread, x starlark.Value, buf *outBuf, work *workMeter, headroom uint64, garbage *uint64) (stop error, _ error) {
	// The output of a value with shared substructure is exponential in the
	// depth of the sharing (cycles are detected, shared children are not):
	// stop as soon as it outgrows the headroom of the thread. Once it has,
	// ChargeAlloc of its length is refused, which gives the error.
	overflowed := func() bool {
		if uint64(buf.Len()) <= headroom {
			return false
		}
		stop = thread.ChargeAlloc(uint64(buf.Len()))
		if stop == nil {
			stop = errors.New("excessive size")
		}
		return true
	}

	// tooBig reports whether n more bytes would outgrow the headroom (and
	// records the refusal in stop). The size of a leaf is computed before it
	// is written: a string of 16 MiB of "\x00" quotes to 96 MiB.
	tooBig := func(n int) bool {
		if uint64(buf.Len())+uint64(n) <= headroom {
			return false
		}
		stop = thread.ChargeAlloc(uint64(buf.Len()) + uint64(n))
		if stop == nil {
			stop = errors.New("excessive size")
		}
		return true
	}

	quote := func(s string) bool {
		// The size of the quoted string is computed before it is written
		// (and a pass that only counts need not write it).
		ascii := isPrintableASCII(s)
		n := 0
		if ascii {
			n = quotedLenASCII(s)
		} else {
			n = quotedLen(s)
		}
		if tooBig(n) {
			return false
		}
		if buf.skip(n) {
			return true
		}
		buf.writeQuoted(s, ascii)
		return true
	}

	// The containers being written, to detect a cycle: a slice for the first
	// levels and a set below them, so that the check is not linear in the
	// depth (a deep value would take quadratic time).
	path := make([]unsafe.Pointer, 0, 8)
	var pathSet map[unsafe.Pointer]struct{}
	const pathSetDepth = 32

	var emit func(x starlark.Value, depth int) error
	var emitValue func(x starlark.Value, depth int) error
	// emit checks the limits and the cycles, and writes the node by emitValue
	// (the push and the pop of the path are not a defer: a defer in a function
	// with this many returns is allocated).
	emit = func(x starlark.Value, depth int) error {
		if overflowed() {
			return stop
		}
		if depth > starlark.MaxValueDepth {
			// A value this deep would overflow the Go stack, which is fatal.
			stop = fmt.Errorf("value is nested more than %d levels deep", starlark.MaxValueDepth)
			return stop
		}

		// It is only necessary to push/pop the item when it might contain
		// itself (i.e. the last three switch cases), but omitting it in the other
		// cases did not show significant improvement on the benchmarks.
		if err := work.add(1); err != nil {
			stop = err
			return err
		}
		ptr := pointer(x)
		if ptr != nil {
			var cycle bool
			if pathSet != nil {
				_, cycle = pathSet[ptr]
			} else {
				cycle = pathContains(path, ptr)
			}
			if cycle {
				return fmt.Errorf("cycle in JSON structure")
			}

			path = append(path, ptr)
			if pathSet != nil {
				pathSet[ptr] = struct{}{}
			} else if len(path) == pathSetDepth {
				pathSet = make(map[unsafe.Pointer]struct{}, 2*pathSetDepth)
				for _, p := range path {
					pathSet[p] = struct{}{}
				}
			}
		}
		err := emitValue(x, depth)
		if ptr != nil {
			if pathSet != nil {
				delete(pathSet, path[len(path)-1])
			}
			path = path[0 : len(path)-1]
		}
		return err
	}
	// emitSeq writes the elements of a list or a tuple.
	emitSeq := func(x starlark.Value, seq starlark.Indexable, depth int) error {
		buf.WriteByte('[')
		for i := 0; i < seq.Len(); i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := emit(seq.Index(i), depth+1); err != nil {
				if stop != nil {
					return stop
				}
				return fmt.Errorf("at %s index %d: %v", x.Type(), i, err)
			}
		}
		buf.WriteByte(']')
		return nil
	}
	emitValue = func(x starlark.Value, depth int) error {
		switch x := x.(type) {
		case json.Marshaler:
			// Application-defined starlark.Value types
			// may define their own JSON encoding.
			data, err := x.MarshalJSON()
			if err != nil {
				return err
			}
			if tooBig(len(data)) {
				return stop
			}
			buf.Write(data)

		case starlark.NoneType:
			buf.WriteString("null")

		case starlark.Bool:
			if x {
				buf.WriteString("true")
			} else {
				buf.WriteString("false")
			}

		case starlark.Int:
			if _, small := x.Int64(); !small {
				bits := x.BigInt().BitLen()
				if bits > starlark.MaxIntBits {
					stop = fmt.Errorf("an integer of more than %d decimal digits is not converted to a string", starlark.MaxIntDigits)
					return stop
				}
				// A big integer has at most BitLen/3+1 digits.
				if tooBig(bits/3 + 2) {
					return stop
				}
				// The conversion is quadratic in the digits.
				d := uint64(bits/3 + 1)
				if err := work.add(d * d / 4096); err != nil {
					stop = err
					return err
				}
			}
			if v, small := x.Int64(); small {
				var tmp [24]byte
				buf.Write(strconv.AppendInt(tmp[:0], v, 10))
			} else {
				buf.WriteString(x.String())
			}

		case starlark.Float:
			if !isFinite(float64(x)) {
				return fmt.Errorf("cannot encode non-finite float %v", x)
			}
			// Float.String always contains a decimal point. (%g does not!)
			var tmp [40]byte
			buf.Write(appendFloat(tmp[:0], float64(x)))

		case starlark.String:
			if !quote(string(x)) {
				return stop
			}

		case starlark.IterableMapping:
			// e.g. dict (must have string keys)
			buf.WriteByte('{')
			items := x.Items()
			if garbage != nil {
				// The items are allocated, so they are charged.
				n := satMul(uint64(len(items)), jsonItemBytes)
				if err := thread.ChargeAlloc(n); err != nil {
					stop = err
					return err
				}
				*garbage = satAdd(*garbage, n)
			}
			for _, item := range items {
				if _, ok := item[0].(starlark.String); !ok {
					return fmt.Errorf("%s has %s key, want string", x.Type(), item[0].Type())
				}
			}
			sort.Slice(items, func(i, j int) bool {
				return items[i][0].(starlark.String) < items[j][0].(starlark.String)
			})
			for i, item := range items {
				if i > 0 {
					buf.WriteByte(',')
				}
				k, _ := starlark.AsString(item[0])
				if !quote(k) {
					return stop
				}
				buf.WriteByte(':')
				if err := emit(item[1], depth+1); err != nil {
					if stop != nil {
						return stop // not wrapped: the message would grow with the depth
					}
					return fmt.Errorf("in %s key %s: %v", x.Type(), starlark.String(short(k)).String(), err)
				}
			}
			buf.WriteByte('}')

		case *starlark.List:
			return emitSeq(x, x, depth) // (no iterator to allocate)

		case starlark.Tuple:
			return emitSeq(x, x, depth)

		case starlark.Iterable:
			buf.WriteByte('[')
			iter := x.Iterate()
			var elem starlark.Value
			for i := 0; iter.Next(&elem); i++ {
				if i > 0 {
					buf.WriteByte(',')
				}
				if err := emit(elem, depth+1); err != nil {
					iter.Done()
					if stop != nil {
						return stop
					}
					return fmt.Errorf("at %s index %d: %v", x.Type(), i, err)
				}
			}
			iter.Done()
			buf.WriteByte(']')

		case starlark.HasAttrs:
			// e.g. struct
			buf.WriteByte('{')
			var names []string
			names = append(names, x.AttrNames()...)
			sort.Strings(names)
			for i, name := range names {
				v, err := x.Attr(name)
				if err != nil {
					return fmt.Errorf("cannot access attribute %s.%s: %w", x.Type(), name, err)
				}
				if v == nil {
					// x.AttrNames() returned name, but x.Attr(name) returned nil, stating
					// that the field doesn't exist.
					return fmt.Errorf("missing attribute %s.%s (despite %q appearing in dir()", x.Type(), name, name)
				}
				if i > 0 {
					buf.WriteByte(',')
				}
				if !quote(name) {
					return stop
				}
				buf.WriteByte(':')
				if err := emit(v, depth+1); err != nil {
					if stop != nil {
						return stop
					}
					return fmt.Errorf("in field .%s: %v", short(name), err)
				}
			}
			buf.WriteByte('}')

		default:
			return fmt.Errorf("cannot encode %s as JSON", x.Type())
		}
		return nil
	}

	if err := emit(x, 0); err != nil {
		return stop, err
	}
	return nil, nil
}

// short cuts a string that is embedded in an error message.
func short(s string) string {
	const max = 96
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...<" + strconv.Itoa(len(s)) + " bytes>"
}

// quotedLenASCII is len(strconv.AppendQuote(nil, s)) for a string of
// printable ASCII: the quotes, and a backslash before " and \.
func quotedLenASCII(s string) int {
	n := len(s) + 2
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			n++
		case 0x7f:
			n += 3 // strconv.Quote writes \x7f
		}
	}
	return n
}

// quotedLen is len(json.Marshal(s)): what encoding/json writes for a string
// (with HTML escaping, which Marshal applies).
func quotedLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\' || b == '\b' || b == '\f' || b == '\n' || b == '\r' || b == '\t':
				n += 2
			case b < 0x20 || b == '<' || b == '>' || b == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += 6 // \ufffd
		case r == '\u2028' || r == '\u2029':
			n += 6
		default:
			n += size
		}
		i += size
	}
	return n
}

// allocErr returns the error of a refused allocation of the built-in b: a
// budget error as it is (it is recognized by type, and its text is fixed),
// and any other with the name of b.
func allocErr(b *starlark.Builtin, err error) error {
	if _, ok := err.(*starlark.AllocBudgetError); ok {
		return err
	}
	if strings.HasPrefix(err.Error(), "Starlark computation cancelled") {
		return err // the step limit or the host: as it is
	}
	return fmt.Errorf("%s: %v", b.Name(), err)
}

func satAdd(a, b uint64) uint64 {
	s, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return math.MaxUint64
	}
	return s
}

func satMul(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return math.MaxUint64
	}
	return lo
}

// indentSize returns an upper bound of the length of json.Indent(s, prefix,
// indent): s itself, and for each element or bracket that starts a new line,
// the newline, the prefix and as many copies of indent as the nesting depth.
// The depth of a line is not bounded by anything but the length of s, so the
// output is quadratic in len(s) for a deeply nested document, and a long
// indent multiplies it.
func indentSize(s, prefix, indent string) uint64 {
	size := uint64(len(s))
	depth := uint64(0)
	newline := func() {
		size = satAdd(size, satAdd(1+uint64(len(prefix)), satMul(depth, uint64(len(indent)))))
	}
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if c == '\\' {
				i++ // skip the escaped byte
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			newline()
		case ',':
			newline()
		case '}', ']':
			if depth > 0 {
				depth--
			}
			newline()
		}
	}
	return size
}

func encodeIndent(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	prefix, indent := "", "\t" // keyword-only
	if err := starlark.UnpackArgs(b.Name(), nil, kwargs,
		"prefix?", &prefix,
		"indent?", &indent,
	); err != nil {
		return nil, err
	}
	// Rely on encode() to parse the positional parameter (since the signature matches); use our b so
	// error messages are attributed to json.encode_indent.
	str, err := encode(thread, b, args, nil)
	if err != nil {
		return nil, err
	}
	out, err := indentString(thread, b, string(str.(starlark.String)), prefix, indent)
	if err != nil {
		return nil, err
	}
	return starlark.String(out), nil
}

// indentString is json.Indent of str, charged and allocated once: the buffer
// is made of the size that is charged (the bound of the output, or what
// json.Indent grows it to), the input is not copied, and the output is not
// copied into a string.
func indentString(thread *starlark.Thread, b *starlark.Builtin, str, prefix, indent string) (string, error) {
	// json.Indent grows its buffer by twice the input before it writes.
	size := max(indentSize(str, prefix, indent), 2*uint64(len(str)))
	if err := thread.ChargeAlloc(size); err != nil {
		return "", allocErr(b, err)
	}
	// The scan and the indentation: ~2 ns a byte of the input.
	if err := chargeUnits(thread, uint64(len(str))/4); err != nil {
		return "", err
	}
	if len(str) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	buf.Grow(int(size))
	// (json.Indent reads src and does not keep it: no copy of str is needed.)
	src := unsafe.Slice(unsafe.StringData(str), len(str))
	if err := json.Indent(&buf, src, prefix, indent); err != nil {
		return "", fmt.Errorf("%s: %v", b.Name(), err)
	}
	if buf.Len() == 0 {
		return "", nil
	}
	return unsafe.String(&buf.Bytes()[0], buf.Len()), nil
}

func pointer(i any) unsafe.Pointer {
	v := reflect.ValueOf(i)
	switch v.Kind() {
	case reflect.Pointer, reflect.Chan, reflect.Map, reflect.UnsafePointer, reflect.Slice:
		// TODO(adonovan): use v.Pointer() when we drop go1.17.
		return unsafe.Pointer(v.Pointer())
	default:
		return nil
	}
}

func pathContains(path []unsafe.Pointer, item unsafe.Pointer) bool {
	return slices.Contains(path, item)
}

// isPrintableASCII reports whether s contains only printable ASCII.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b >= 0x80 {
			return false
		}
	}
	return true
}

// isFinite reports whether f represents a finite rational value.
// It is equivalent to !math.IsNan(f) && !math.IsInf(f, 0).
func isFinite(f float64) bool {
	return math.Abs(f) <= math.MaxFloat64
}

func indent(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	prefix, indent := "", "\t" // keyword-only
	if err := starlark.UnpackArgs(b.Name(), nil, kwargs,
		"prefix?", &prefix,
		"indent?", &indent,
	); err != nil {
		return nil, err
	}
	var str string // positional-only
	if err := starlark.UnpackPositionalArgs(b.Name(), args, nil, 1, &str); err != nil {
		return nil, err
	}

	out, err := indentString(thread, b, str, prefix, indent)
	if err != nil {
		return nil, err
	}
	return starlark.String(out), nil
}

func decode(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (v starlark.Value, err error) {
	var s string
	var d starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "x", &s, "default?", &d); err != nil {
		return nil, err
	}
	if len(args) < 1 {
		// "x" parameter is positional only; UnpackArgs does not allow us to
		// directly express "def decode(x, *, default)"
		return nil, fmt.Errorf("%s: unexpected keyword argument x", b.Name())
	}

	// The decoder necessarily makes certain representation choices
	// such as list vs tuple, struct vs dict, int vs float.
	// In principle, we could parameterize it to allow the caller to
	// control the returned types, but there's no compelling need yet.

	// Use panic/recover with a distinguished type (failure) for error handling.
	// If "default" is set, we only want to return it when encountering invalid
	// json - not for any other possible causes of panic.
	// In particular, if we ever extend the json.decode API to take a callback,
	// a distinguished, private failure type prevents the possibility of
	// json.decode with "default" becoming abused as a try-catch mechanism.
	type failure string
	fail := func(format string, args ...any) {
		panic(failure(fmt.Sprintf(format, args...)))
	}

	// A refused allocation is not a syntax error: it is returned as the
	// error even if there is a default.
	type refusal struct{ err error }
	charge := func(n uint64) {
		if err := thread.ChargeAlloc(n); err != nil {
			panic(refusal{allocErr(b, err)})
		}
	}
	// The nesting of arrays and objects is bounded: parse is recursive, and
	// a document nested 1.5 million deep would overflow the Go stack, which
	// is fatal. Like a refused allocation, it is an error, not a syntax error.
	meter := workMeter{th: thread} // one unit a value, a quarter a byte of a string
	spend := func(n uint64) {
		if err := meter.add(n); err != nil {
			panic(refusal{err})
		}
	}
	depth := 0
	enter := func() {
		depth++
		if depth > starlark.MaxValueDepth {
			panic(refusal{fmt.Errorf("%s: value is nested more than %d levels deep", b.Name(), starlark.MaxValueDepth)})
		}
	}

	i := 0

	// skipSpace consumes leading spaces, and reports whether there is more input.
	skipSpace := func() bool {
		for ; i < len(s); i++ {
			b := s[i]
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				return true
			}
		}
		return false
	}

	// next consumes leading spaces and returns the first non-space.
	// It panics if at EOF.
	next := func() byte {
		if skipSpace() {
			return s[i]
		}
		fail("unexpected end of file")
		panic("unreachable")
	}

	// parse returns the next JSON value from the input.
	// It consumes leading but not trailing whitespace.
	// It panics on error.
	var parse func() starlark.Value
	parse = func() starlark.Value {
		b := next()
		spend(1)
		switch b {
		case '"':
			// string

			// Find end of quotation.
			// Also, record whether trivial unquoting is safe.
			// Non-trivial unquoting is handled by Go's encoding/json.
			safe := true
			closed := false
			j := i + 1
			for ; j < len(s); j++ {
				b := s[j]
				if b == '\\' {
					safe = false
					j++ // skip x in \x
				} else if b == '"' {
					closed = true
					j++ // skip '"'
					break
				} else if b >= utf8.RuneSelf {
					safe = false
				}
			}
			if !closed {
				fail("unclosed string literal")
			}

			r := s[i:j]
			i = j

			// unquote
			if safe {
				r = r[1 : len(r)-1]
			} else if err := json.Unmarshal([]byte(r), &r); err != nil {
				fail("%s", err)
			}
			charge(uint64(len(r)))
			spend(uint64(len(r)) / 4)
			return starlark.String(r)

		case 'n':
			if strings.HasPrefix(s[i:], "null") {
				i += len("null")
				return starlark.None
			}

		case 't':
			if strings.HasPrefix(s[i:], "true") {
				i += len("true")
				return starlark.True
			}

		case 'f':
			if strings.HasPrefix(s[i:], "false") {
				i += len("false")
				return starlark.False
			}

		case '[':
			// array
			var elems []starlark.Value

			i++ // '['
			enter()
			charge(starlark.ListAllocBytes(0)) // the list, even if it is empty
			b = next()
			if b != ']' {
				for {
					elem := parse()
					// one list slot
					charge(starlark.ListAllocBytes(len(elems)+1) - starlark.ListAllocBytes(len(elems)))
					elems = append(elems, elem)
					b = next()
					if b != ',' {
						if b != ']' {
							fail("got %q, want ',' or ']'", b)
						}
						break
					}
					i++ // ','
				}
			}
			i++ // ']'
			depth--
			return starlark.NewList(elems)

		case '{':
			// object
			dict := new(starlark.Dict)

			i++ // '{'
			enter()
			charge(starlark.DictAllocBytes(0)) // the dict, even if it is empty
			b = next()
			if b != '}' {
				for {
					key := parse()
					if _, ok := key.(starlark.String); !ok {
						fail("got %s for object key, want string", key.Type())
					}
					b = next()
					if b != ':' {
						fail("after object key, got %q, want ':' ", b)
					}
					i++ // ':'
					value := parse()
					// one dict entry
					charge(starlark.DictAllocBytes(dict.Len()+1) - starlark.DictAllocBytes(dict.Len()))
					if err := dict.SetKeyWork(thread, key, value); err != nil { // (can't fail otherwise)
						panic(refusal{err})
					}
					b = next()
					if b != ',' {
						if b != '}' {
							fail("in object, got %q, want ',' or '}'", b)
						}
						break
					}
					i++ // ','
				}
			}
			i++ // '}'
			depth--
			return dict

		default:
			// number?
			if isdigit(b) || b == '-' {
				// scan literal. Allow [0-9+-eE.] for now.
				float := false
				var j int
				for j = i + 1; j < len(s); j++ {
					b = s[j]
					if isdigit(b) {
						// ok
					} else if b == '.' ||
						b == 'e' ||
						b == 'E' ||
						b == '+' ||
						b == '-' {
						float = true
					} else {
						break
					}
				}
				num := s[i:j]
				i = j

				// Unlike most C-like languages,
				// JSON disallows a leading zero before a digit.
				digits := num
				if num[0] == '-' {
					digits = num[1:]
				}
				if digits == "" || digits[0] == '0' && len(digits) > 1 && isdigit(digits[1]) {
					fail("invalid number: %s", short(num))
				}

				// parse literal
				if !float && len(digits) > starlark.MaxIntDigits {
					// The conversion is quadratic in the digits: a limit, not a
					// syntax error (the default is not returned for it).
					panic(refusal{fmt.Errorf("json.decode: number has more than %d digits", starlark.MaxIntDigits)})
				}
				spend(uint64(len(num)) * uint64(len(num)) / 4096)
				if float {
					x, err := strconv.ParseFloat(num, 64)
					if err != nil {
						fail("invalid number: %s", short(num))
					}
					return starlark.Float(x)
				} else {
					if len(digits) <= 9 {
						// fits in 32 bits: no big.Int
						v := 0
						for k := 0; k < len(digits); k++ {
							v = v*10 + int(digits[k]-'0')
						}
						if num[0] == '-' {
							v = -v
						}
						return starlark.MakeInt(v)
					}
					x, ok := new(big.Int).SetString(num, 10)
					if !ok {
						fail("invalid number: %s", short(num))
					}
					if x.BitLen() >= 32 { // a small integer is a word in the Value
						charge(uint64(x.BitLen()+7) / 8)
					}
					return starlark.MakeBigInt(x)
				}
			}
		}
		fail("unexpected character %q", b)
		panic("unreachable")
	}
	defer func() {
		x := recover()
		switch x := x.(type) {
		case refusal:
			v, err = nil, x.err
		case failure:
			if d != nil {
				v = d
			} else {
				err = fmt.Errorf("json.decode: at offset %d, %s", i, x)
			}
		case nil:
			// nop
		default:
			panic(x) // unexpected panic
		}
	}()
	v = parse()
	if skipSpace() {
		fail("unexpected character %q after value", s[i])
	}
	if err := meter.flush(); err != nil {
		return nil, err
	}
	return v, nil
}

func isdigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// chargeUnits charges units of work to the thread.
func chargeUnits(th *starlark.Thread, units uint64) error {
	return th.ChargeWork(units)
}

// jsonItemBytes is the size of an item of Items() (a pair of values and the
// slice that holds them), rounded up.
const jsonItemBytes = 64

// formProbe is the size up to which an output is made in one pass (the garbage
// of an output that outgrows it is at most 5 * formProbe bytes).
const formProbe = 4096

// An outBuf is where an output is written. It counts the bytes written, and
// keeps them, unless it is counting only (an output that outgrew its probe,
// which the second pass writes into a buffer of the exact size): a buffer that
// grows by append allocates five times what it holds, and copying it into a
// string once more.
type outBuf struct {
	b        strings.Builder
	n        int
	probe    int // if > 0, count only once n would exceed it
	counting bool
}

func (o *outBuf) Len() int { return o.n }

func (o *outBuf) room(n int) {
	if o.probe > 0 && !o.counting && o.n+n > o.probe {
		o.counting = true
		o.b = strings.Builder{}
	}
}

// skip counts n bytes that are not written if the buffer is counting only, and
// reports whether it did.
func (o *outBuf) skip(n int) bool {
	o.room(n)
	if o.counting {
		o.n += n
		return true
	}
	return false
}

func (o *outBuf) Write(p []byte) (int, error) {
	o.room(len(p))
	o.n += len(p)
	if !o.counting {
		o.b.Write(p)
	}
	return len(p), nil
}

func (o *outBuf) WriteString(s string) (int, error) {
	o.room(len(s))
	o.n += len(s)
	if !o.counting {
		o.b.WriteString(s)
	}
	return len(s), nil
}

func (o *outBuf) WriteByte(c byte) error {
	o.room(1)
	o.n++
	if !o.counting {
		o.b.WriteByte(c)
	}
	return nil
}

// writeQuoted writes the JSON string s in pieces, so that no copy of the
// quoted string is made. ascii says that s is printable ASCII.
func (o *outBuf) writeQuoted(s string, ascii bool) {
	var tmp [512]byte
	if ascii {
		o.WriteByte('"')
		for len(s) > 0 {
			n := min(len(s), 128)
			p := strconv.AppendQuote(tmp[:0], s[:n])
			o.Write(p[1 : len(p)-1])
			s = s[n:]
		}
		o.WriteByte('"')
		return
	}
	o.WriteByte('"')
	for len(s) > 0 {
		n := min(len(s), 64)
		for k := n; k > 0 && k < len(s) && !utf8.RuneStart(s[k]); k-- {
			n = k - 1 // not in the middle of a rune
		}
		if n == 0 {
			n = min(len(s), 64)
		}
		o.Write(appendJSONString(tmp[:0], s[:n]))
		s = s[n:]
	}
	o.WriteByte('"')
}

// appendJSONString appends s as encoding/json writes it between the quotes
// (with the escaping of HTML): see quotedLen.
func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\':
				dst = append(dst, '\\', b)
			case b == '\b':
				dst = append(dst, '\\', 'b')
			case b == '\f':
				dst = append(dst, '\\', 'f')
			case b == '\n':
				dst = append(dst, '\\', 'n')
			case b == '\r':
				dst = append(dst, '\\', 'r')
			case b == '\t':
				dst = append(dst, '\\', 't')
			case b < 0x20 || b == '<' || b == '>' || b == '&':
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xF])
			default:
				dst = append(dst, b)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(dst, `�`...)
		case r == ' ':
			dst = append(dst, ` `...)
		case r == ' ':
			dst = append(dst, ` `...)
		default:
			dst = append(dst, s[i:i+size]...)
		}
		i += size
	}
	return dst
}

// appendFloat appends the string form of a finite float: %g with the
// shortest representation, and a decimal point if there is no exponent.
func appendFloat(dst []byte, f float64) []byte {
	n := len(dst)
	dst = strconv.AppendFloat(dst, f, 'g', -1, 64)
	if bytes.IndexByte(dst[n:], 'e') < 0 && bytes.IndexByte(dst[n:], '.') < 0 {
		dst = append(dst, ".0"...)
	}
	return dst
}
