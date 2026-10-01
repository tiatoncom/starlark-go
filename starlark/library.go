// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package starlark

// This file defines the library of built-ins.
//
// Built-ins must explicitly check the "frozen" flag before updating
// mutable types such as lists and dicts.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"go.starlark.net/syntax"
)

// Universe defines the set of universal built-ins, such as None, True, and len.
//
// The Go application may add or remove items from the
// universe dictionary before Starlark evaluation begins.
// All values in the dictionary must be immutable.
// Starlark programs cannot modify the dictionary.
var Universe StringDict

func init() {
	// https://github.com/google/starlark-go/blob/master/doc/spec.md#built-in-constants-and-functions
	Universe = StringDict{
		"None":      None,
		"True":      True,
		"False":     False,
		"abs":       NewBuiltin("abs", abs),
		"any":       NewBuiltin("any", any_),
		"all":       NewBuiltin("all", all),
		"bool":      NewBuiltin("bool", bool_),
		"bytes":     NewBuiltin("bytes", bytes_),
		"chr":       NewBuiltin("chr", chr),
		"dict":      NewBuiltin("dict", dict),
		"dir":       NewBuiltin("dir", dir),
		"enumerate": NewBuiltin("enumerate", enumerate),
		"fail":      NewBuiltin("fail", fail),
		"float":     NewBuiltin("float", float),
		"getattr":   NewBuiltin("getattr", getattr),
		"hasattr":   NewBuiltin("hasattr", hasattr),
		"hash":      NewBuiltin("hash", hash),
		"int":       NewBuiltin("int", int_),
		"len":       NewBuiltin("len", len_),
		"list":      NewBuiltin("list", list),
		"max":       NewBuiltin("max", minmax),
		"min":       NewBuiltin("min", minmax),
		"ord":       NewBuiltin("ord", ord),
		"print":     NewBuiltin("print", print),
		"range":     NewBuiltin("range", range_),
		"repr":      NewBuiltin("repr", repr),
		"reversed":  NewBuiltin("reversed", reversed),
		"set":       NewBuiltin("set", set),
		"sorted":    NewBuiltin("sorted", sorted),
		"str":       NewBuiltin("str", str),
		"tuple":     NewBuiltin("tuple", tuple),
		"type":      NewBuiltin("type", type_),
		"zip":       NewBuiltin("zip", zip),
	}
	attachPrices()
}

// methods of built-in types
// https://github.com/google/starlark-go/blob/master/doc/spec.md#built-in-methods
var (
	bytesMethods = map[string]*Builtin{
		"elems": NewBuiltin("elems", bytes_elems),
	}

	dictMethods = map[string]*Builtin{
		"clear":      NewBuiltin("clear", dict_clear),
		"get":        NewBuiltin("get", dict_get),
		"items":      NewBuiltin("items", dict_items),
		"keys":       NewBuiltin("keys", dict_keys),
		"pop":        NewBuiltin("pop", dict_pop),
		"popitem":    NewBuiltin("popitem", dict_popitem),
		"setdefault": NewBuiltin("setdefault", dict_setdefault),
		"update":     NewBuiltin("update", dict_update),
		"values":     NewBuiltin("values", dict_values),
	}

	listMethods = map[string]*Builtin{
		"append": NewBuiltin("append", list_append),
		"clear":  NewBuiltin("clear", list_clear),
		"extend": NewBuiltin("extend", list_extend),
		"index":  NewBuiltin("index", list_index),
		"insert": NewBuiltin("insert", list_insert),
		"pop":    NewBuiltin("pop", list_pop),
		"remove": NewBuiltin("remove", list_remove),
	}

	stringMethods = map[string]*Builtin{
		"capitalize":     NewBuiltin("capitalize", string_capitalize),
		"codepoint_ords": NewBuiltin("codepoint_ords", string_iterable),
		"codepoints":     NewBuiltin("codepoints", string_iterable), // sic
		"count":          NewBuiltin("count", string_count),
		"elem_ords":      NewBuiltin("elem_ords", string_iterable),
		"elems":          NewBuiltin("elems", string_iterable),      // sic
		"endswith":       NewBuiltin("endswith", string_startswith), // sic
		"find":           NewBuiltin("find", string_find),
		"format":         NewBuiltin("format", string_format),
		"index":          NewBuiltin("index", string_index),
		"isalnum":        NewBuiltin("isalnum", string_isalnum),
		"isalpha":        NewBuiltin("isalpha", string_isalpha),
		"isdigit":        NewBuiltin("isdigit", string_isdigit),
		"islower":        NewBuiltin("islower", string_islower),
		"isspace":        NewBuiltin("isspace", string_isspace),
		"istitle":        NewBuiltin("istitle", string_istitle),
		"isupper":        NewBuiltin("isupper", string_isupper),
		"join":           NewBuiltin("join", string_join),
		"lower":          NewBuiltin("lower", string_lower),
		"lstrip":         NewBuiltin("lstrip", string_strip), // sic
		"partition":      NewBuiltin("partition", string_partition),
		"removeprefix":   NewBuiltin("removeprefix", string_removefix),
		"removesuffix":   NewBuiltin("removesuffix", string_removefix),
		"replace":        NewBuiltin("replace", string_replace),
		"rfind":          NewBuiltin("rfind", string_rfind),
		"rindex":         NewBuiltin("rindex", string_rindex),
		"rpartition":     NewBuiltin("rpartition", string_partition), // sic
		"rsplit":         NewBuiltin("rsplit", string_split),         // sic
		"rstrip":         NewBuiltin("rstrip", string_strip),         // sic
		"split":          NewBuiltin("split", string_split),
		"splitlines":     NewBuiltin("splitlines", string_splitlines),
		"startswith":     NewBuiltin("startswith", string_startswith),
		"strip":          NewBuiltin("strip", string_strip),
		"title":          NewBuiltin("title", string_title),
		"upper":          NewBuiltin("upper", string_upper),
	}

	setMethods = map[string]*Builtin{
		"add":                  NewBuiltin("add", set_add),
		"clear":                NewBuiltin("clear", set_clear),
		"difference":           NewBuiltin("difference", set_difference),
		"discard":              NewBuiltin("discard", set_discard),
		"intersection":         NewBuiltin("intersection", set_intersection),
		"issubset":             NewBuiltin("issubset", set_issubset),
		"issuperset":           NewBuiltin("issuperset", set_issuperset),
		"pop":                  NewBuiltin("pop", set_pop),
		"remove":               NewBuiltin("remove", set_remove),
		"symmetric_difference": NewBuiltin("symmetric_difference", set_symmetric_difference),
		"union":                NewBuiltin("union", set_union),
		"update":               NewBuiltin("update", set_update),
	}
)

func builtinAttr(recv Value, name string, methods map[string]*Builtin) (Value, error) {
	b := methods[name]
	if b == nil {
		return nil, nil // no such method
	}
	return b.BindReceiver(recv), nil
}

func builtinAttrNames(methods map[string]*Builtin) []string {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---- built-in functions ----

// https://github.com/google/starlark-go/blob/master/doc/spec.md#abs
func abs(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("abs", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	switch x := x.(type) {
	case Float:
		return Float(math.Abs(float64(x))), nil
	case Int:
		if x.Sign() >= 0 {
			return x, nil
		}
		if err := thread.intRoom(bigWords(x)); err != nil {
			return nil, excess(err, "excessive integer negation")
		}
		return thread.intDone(zero.Sub(x))
	default:
		return nil, fmt.Errorf("got %s, want int or float", x.Type())
	}
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#all
func all(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("all", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	m := thread.meter()
	var x Value
	for iter.Next(&x) {
		if err := m.add(1); err != nil {
			return nil, err
		}
		if !x.Truth() {
			return False, m.flush()
		}
	}
	return True, m.flush()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#any
func any_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("any", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	m := thread.meter()
	var x Value
	for iter.Next(&x) {
		if err := m.add(1); err != nil {
			return nil, err
		}
		if x.Truth() {
			return True, m.flush()
		}
	}
	return False, m.flush()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#bool
func bool_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value = False
	if err := UnpackPositionalArgs("bool", args, kwargs, 0, &x); err != nil {
		return nil, err
	}
	return x.Truth(), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#bytes
func bytes_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("bytes does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("bytes: got %d arguments, want exactly 1", len(args))
	}
	switch x := args[0].(type) {
	case Bytes:
		return x, nil
	case String:
		// Invalid encodings are replaced by that of U+FFFD. A valid string is
		// returned as it is, which allocates nothing.
		if n := utf8TranscodedLen(string(x)); n != len(x) {
			if err := thread.chargeBytes(n); err != nil {
				return nil, excess(err, "bytes: excessive size (%d bytes)", n)
			}
		}
		return Bytes(utf8Transcode(string(x))), nil
	case Iterable:
		// iterable of numeric byte values
		var buf strings.Builder
		m := thread.meter()
		n := Len(x)
		if n >= 0 {
			// common case: known length
			if err := thread.chargeBytes(n); err != nil {
				return nil, excess(err, "bytes: excessive size (%d bytes)", n)
			}
			buf.Grow(n)
		}
		iter := x.Iterate()
		defer iter.Done()
		var elem Value
		var b byte
		for i := 0; iter.Next(&elem); i++ {
			if n < 0 {
				if err := m.add(iterWork(x)); err != nil {
					return nil, err
				}
				if err := thread.chargeOne(buf.Len(), 1); err != nil {
					return nil, excess(err, "bytes: excessive size (over %d bytes)", maxAlloc)
				}
			}
			if err := AsInt(elem, &b); err != nil {
				return nil, fmt.Errorf("bytes: at index %d, %s", i, err)
			}
			buf.WriteByte(b)
		}
		return Bytes(buf.String()), nil

	default:
		// Unlike string(foo), which stringifies it, bytes(foo) is an error.
		return nil, fmt.Errorf("bytes: got %s, want string, bytes, or iterable of ints", x.Type())
	}
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#chr
func chr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("chr does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("chr: got %d arguments, want 1", len(args))
	}
	i, err := AsInt32(args[0])
	if err != nil {
		return nil, fmt.Errorf("chr: %s", err)
	}
	if i < 0 {
		return nil, fmt.Errorf("chr: Unicode code point %d out of range (<0)", i)
	}
	if i > unicode.MaxRune {
		return nil, fmt.Errorf("chr: Unicode code point U+%X out of range (>0x10FFFF)", i)
	}
	return String(string(rune(i))), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict
func dict(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("dict: got %d arguments, want at most 1", len(args))
	}
	dict := new(Dict)
	// With an argument, updateDict charges the dict with its first entries;
	// dict() alone, which is an empty dict, is charged by Call.
	if err := updateDict(thread, dict, args, kwargs, true); err != nil {
		return nil, prefixErr("dict", err)
	}
	return dict, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dir
func dir(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("dir does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("dir: got %d arguments, want 1", len(args))
	}

	var names []string
	if x, ok := args[0].(HasAttrs); ok {
		names = x.AttrNames()
	}
	sort.Strings(names)
	elems := make([]Value, len(names))
	for i, name := range names {
		elems[i] = String(name)
	}
	return NewList(elems), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#enumerate
func enumerate(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	var start int
	if err := UnpackPositionalArgs("enumerate", args, kwargs, 1, &iterable, &start); err != nil {
		return nil, err
	}

	iter := iterable.Iterate()
	defer iter.Done()

	var pairs []Value
	var x Value

	if n := Len(iterable); n >= 0 {
		// common case: known length
		if err := thread.chargeItems(n); err != nil {
			return nil, excess(err, "enumerate: excessive size (%d elements)", n)
		}
		pairs = make([]Value, 0, n)
		array := make(Tuple, 2*n) // allocate a single backing array
		for i := 0; iter.Next(&x); i++ {
			pair := array[:2:2]
			array = array[2:]
			pair[0] = MakeInt(start + i)
			pair[1] = x
			pairs = append(pairs, pair)
		}
	} else {
		// non-sequence (unknown length): the list now, each pair as it comes
		if err := thread.chargeValues(0); err != nil {
			return nil, excess(err, "enumerate: excessive size")
		}
		m := thread.meter()
		for i := 0; iter.Next(&x); i++ {
			if err := m.add(iterWork(iterable)); err != nil {
				return nil, err
			}
			if err := thread.chargeOne(len(pairs), allocBytesPerItem); err != nil {
				return nil, excess(err, "enumerate: excessive size (over %d elements)", maxAlloc)
			}
			pair := Tuple{MakeInt(start + i), x}
			pairs = append(pairs, pair)
		}
	}

	return NewList(pairs), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#fail
func fail(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	sep := " "
	if err := UnpackArgs("fail", nil, kwargs, "sep?", &sep); err != nil {
		return nil, err
	}
	limit := thread.stringLimit()
	buf := new(strings.Builder)
	m := thread.meter()
	buf.WriteString("fail: ")
	for i, v := range args {
		if i > 0 {
			buf.WriteString(sep)
		}
		if buf.Len() >= limit {
			break // the message is bounded like any string form: the rest is dropped
		}
		if s, ok := AsString(v); ok {
			buf.WriteString(s)
		} else {
			// (the work of the form is charged: a message that is a list of a
			// million elements is a million units)
			out := sink{ext: buf, n: buf.Len()}
			if _, werr := writeValueMeter(&out, v, limit, &m); werr != nil {
				return nil, werr
			}
		}
	}
	if err := m.flush(); err != nil {
		return nil, err
	}

	return nil, errors.New(buf.String())
}

func float(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("float does not accept keyword arguments")
	}
	if len(args) == 0 {
		return Float(0.0), nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("float got %d arguments, wants 1", len(args))
	}
	switch x := args[0].(type) {
	case Bool:
		if x {
			return Float(1.0), nil
		} else {
			return Float(0.0), nil
		}
	case Int:
		return x.finiteFloat()
	case Float:
		return x, nil
	case String:
		if x == "" {
			return nil, fmt.Errorf("float: empty string")
		}
		// +/- NaN or Inf or Infinity (case insensitive)?
		s := string(x)
		switch x[len(x)-1] {
		case 'y', 'Y':
			if strings.EqualFold(s, "infinity") || strings.EqualFold(s, "+infinity") {
				return inf, nil
			} else if strings.EqualFold(s, "-infinity") {
				return neginf, nil
			}
		case 'f', 'F':
			if strings.EqualFold(s, "inf") || strings.EqualFold(s, "+inf") {
				return inf, nil
			} else if strings.EqualFold(s, "-inf") {
				return neginf, nil
			}
		case 'n', 'N':
			if strings.EqualFold(s, "nan") || strings.EqualFold(s, "+nan") || strings.EqualFold(s, "-nan") {
				return nan, nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if math.IsInf(f, 0) {
			return nil, fmt.Errorf("floating-point number too large")
		}
		if err != nil {
			return nil, fmt.Errorf("invalid float literal: %s", errStr(s))
		}
		return Float(f), nil
	default:
		return nil, fmt.Errorf("float got %s, want number or string", x.Type())
	}
}

var (
	inf    = Float(math.Inf(+1))
	neginf = Float(math.Inf(-1))
	nan    = Float(math.NaN())
)

// https://github.com/google/starlark-go/blob/master/doc/spec.md#getattr
func getattr(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var object, dflt Value
	var name string
	if err := UnpackPositionalArgs("getattr", args, kwargs, 2, &object, &name, &dflt); err != nil {
		return nil, err
	}
	if object, ok := object.(HasAttrs); ok {
		v, err := object.Attr(name)
		if err != nil {
			// An error could mean the field doesn't exist,
			// or it exists but could not be computed.
			if dflt != nil {
				return dflt, nil
			}
			return nil, nameErr(b, err)
		}
		if v != nil {
			return v, nil
		}
		// (nil, nil) => no such field
	}
	if dflt != nil {
		return dflt, nil
	}
	return nil, fmt.Errorf("getattr: %s has no .%s field or method", object.Type(), errStr(name))
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#hasattr
func hasattr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var object Value
	var name string
	if err := UnpackPositionalArgs("hasattr", args, kwargs, 2, &object, &name); err != nil {
		return nil, err
	}
	if object, ok := object.(HasAttrs); ok {
		v, err := object.Attr(name)
		if err == nil {
			return Bool(v != nil), nil
		}

		// An error does not conclusively indicate presence or
		// absence of a field: it could occur while computing
		// the value of a present attribute, or it could be a
		// "no such attribute" error with details.
		if slices.Contains(object.AttrNames(), name) {
			return True, nil
		}
	}
	return False, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#hash
func hash(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("hash", args, kwargs, 1, &x); err != nil {
		return nil, err
	}

	var h int64
	switch x := x.(type) {
	case String:
		// The Starlark spec requires that the hash function be
		// deterministic across all runs, motivated by the need
		// for reproducibility of builds. Thus we cannot call
		// String.Hash, which uses the fastest implementation
		// available, because as varies across process restarts,
		// and may evolve with the implementation.
		h = int64(javaStringHash(string(x)))
	case Bytes:
		h = int64(softHashString(string(x))) // FNV32
	default:
		return nil, fmt.Errorf("hash: got %s, want string or bytes", x.Type())
	}
	return MakeInt64(h), nil
}

// javaStringHash returns the same hash as would be produced by
// java.lang.String.hashCode. This requires transcoding the string to
// UTF-16; transcoding may introduce Unicode replacement characters
// U+FFFD if s does not contain valid UTF-8.
func javaStringHash(s string) (h int32) {
	for _, r := range s {
		if utf16.IsSurrogate(r) {
			c1, c2 := utf16.EncodeRune(r)
			h = 31*h + c1
			h = 31*h + c2
		} else {
			h = 31*h + r // r may be U+FFFD
		}
	}
	return h
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#int
func int_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value = zero
	var base Value
	if err := UnpackArgs("int", args, kwargs, "x", &x, "base?", &base); err != nil {
		return nil, err
	}

	if s, ok := AsString(x); ok {
		b := 10
		if base != nil {
			var err error
			b, err = AsInt32(base)
			if err != nil {
				return nil, fmt.Errorf("int: for base, got %s, want int", base.Type())
			}
			if b != 0 && (b < 2 || b > 36) {
				return nil, fmt.Errorf("int: base must be an integer >= 2 && <= 36")
			}
		}
		// The conversion is quadratic in the digits: refuse a string of more
		// than MaxIntDigits (or the same number of bits in another base), and
		// charge the work of the rest.
		if digits, base := intDigits(s, b); digits > 0 {
			// Decimal digits are limited to MaxIntDigits; another base to the
			// same number of bits (a power of two base is linear to convert, but
			// the integer it makes is as long).
			if base == 10 && digits > MaxIntDigits || base != 10 && digits*bits.Len(uint(base-1)) > 4*MaxIntDigits {
				return nil, fmt.Errorf("int: the string has more than %d digits: its conversion to an integer is not allowed", MaxIntDigits)
			}
			d := uint64(digits)
			if err := thread.chargeWork(d * d / 4096); err != nil {
				return nil, err
			}
		}
		res := parseInt(s, b)
		if res == nil {
			return nil, fmt.Errorf("int: invalid literal with base %d: %s", b, errStr(s))
		}
		return thread.intDone(res.(Int))
	}

	if base != nil {
		return nil, fmt.Errorf("int: can't convert non-string with explicit base")
	}

	if b, ok := x.(Bool); ok {
		if b {
			return one, nil
		} else {
			return zero, nil
		}
	}

	i, err := NumberToInt(x)
	if err != nil {
		return nil, fmt.Errorf("int: %s", err)
	}
	if _, isInt := x.(Int); isInt {
		return i, nil // the value itself, not a new one
	}
	return thread.intDone(i) // a float made a new integer
}

// parseInt defines the behavior of int(string, base=int). It returns nil on error.
// intDigits returns the number of digits of the integer that int(s, base) would
// parse, and its base: s without its sign and its base prefix.
func intDigits(s string, base int) (int, int) {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if len(s) > 2 && s[0] == '0' {
		prefix := 0
		switch s[1] {
		case 'o', 'O':
			prefix = 8
		case 'x', 'X':
			prefix = 16
		case 'b', 'B':
			prefix = 2
		}
		if prefix != 0 && (base == 0 || base == prefix) {
			s = s[2:]
			base = prefix
		}
	}
	if base == 0 {
		base = 10
	}
	if base < 2 {
		base = 2
	}
	return len(s), base
}

func parseInt(s string, base int) Value {
	// remove sign
	var neg bool
	if s != "" {
		if s[0] == '+' {
			s = s[1:]
		} else if s[0] == '-' {
			neg = true
			s = s[1:]
		}
	}

	// remove optional base prefix
	baseprefix := 0
	if len(s) > 1 && s[0] == '0' {
		if len(s) > 2 {
			switch s[1] {
			case 'o', 'O':
				baseprefix = 8
			case 'x', 'X':
				baseprefix = 16
			case 'b', 'B':
				baseprefix = 2
			}
		}
		if baseprefix != 0 {
			// Remove the base prefix if it matches
			// the explicit base, or if base=0.
			if base == 0 || baseprefix == base {
				base = baseprefix
				s = s[2:]
			}
		} else {
			// For automatic base detection,
			// a string starting with zero
			// must be all zeros.
			// Thus we reject int("0755", 0).
			if base == 0 {
				for i := 1; i < len(s); i++ {
					if s[i] != '0' {
						return nil
					}
				}
				return zero
			}
		}
	}
	if base == 0 {
		base = 10
	}

	// we explicitly handled sign above.
	// if a sign remains, it is invalid.
	if s != "" && (s[0] == '-' || s[0] == '+') {
		return nil
	}

	// s has no sign or base prefix.
	if i, ok := new(big.Int).SetString(s, base); ok {
		res := MakeBigInt(i)
		if neg {
			res = zero.Sub(res)
		}
		return res
	}

	return nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#len
func len_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("len", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	len := Len(x)
	if len < 0 {
		return nil, fmt.Errorf("len: value of type %s has no len", x.Type())
	}
	return MakeInt(len), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list
func list(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("list", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	var elems []Value
	n := 0
	if iterable != nil {
		n = Len(iterable)
	}
	// The list itself, and its elements if their number is known (else per
	// element, below).
	if err := thread.chargeListOf(n, iterable); err != nil {
		return nil, excess(err, "list: excessive size (%d elements)", n)
	}
	if iterable != nil {
		iter := iterable.Iterate()
		defer iter.Done()
		if n > 0 {
			elems = make([]Value, 0, n) // preallocate if length known
		}
		m := thread.meter()
		var x Value
		for iter.Next(&x) {
			if n < 0 {
				if err := m.add(iterWork(iterable)); err != nil {
					return nil, err
				}
				if err := thread.chargeOne(len(elems), allocBytesPerNewValue); err != nil {
					return nil, excess(err, "list: excessive size (over %d elements)", maxAlloc)
				}
			}
			elems = append(elems, x)
		}
	}
	return NewList(elems), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#min
func minmax(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s requires at least one positional argument", b.Name())
	}
	var keyFunc Callable
	if err := UnpackArgs(b.Name(), nil, kwargs, "key?", &keyFunc); err != nil {
		return nil, err
	}
	var op syntax.Token
	if b.Name() == "max" {
		op = syntax.GT
	} else {
		op = syntax.LT
	}
	var iterable Value
	if len(args) == 1 {
		iterable = args[0]
	} else {
		iterable = args
	}
	iter := Iterate(iterable)
	if iter == nil {
		return nil, fmt.Errorf("%s: %s value is not iterable", b.Name(), iterable.Type())
	}
	defer iter.Done()
	var extremum Value
	if !iter.Next(&extremum) {
		return nil, nameErr(b, "argument is an empty sequence")
	}

	var extremeKey Value
	var keyargs Tuple
	if keyFunc == nil {
		extremeKey = extremum
	} else {
		keyargs = Tuple{extremum}
		res, err := Call(thread, keyFunc, keyargs, nil)
		if err != nil {
			return nil, err // to preserve backtrace, don't modify error
		}
		extremeKey = res
	}

	m := thread.meter()
	var x Value
	for iter.Next(&x) {
		if err := m.add(3); err != nil { // an element: its turn of the iterator, the compare
			return nil, err
		}
		var key Value
		if keyFunc == nil {
			key = x
		} else {
			keyargs[0] = x
			res, err := Call(thread, keyFunc, keyargs, nil)
			if err != nil {
				return nil, err // to preserve backtrace, don't modify error
			}
			key = res
		}

		if ok, err := compareM(&m, op, key, extremeKey, CompareLimit); err != nil {
			return nil, nameErr(b, err)
		} else if ok {
			extremum = x
			extremeKey = key
		}
	}
	return extremum, m.flush()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#ord
func ord(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("ord does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("ord: got %d arguments, want 1", len(args))
	}
	switch x := args[0].(type) {
	case String:
		// ord(string) returns int value of sole rune.
		s := string(x)
		r, sz := utf8.DecodeRuneInString(s)
		if sz == 0 || sz != len(s) {
			n := utf8.RuneCountInString(s)
			return nil, fmt.Errorf("ord: string encodes %d Unicode code points, want 1", n)
		}
		return MakeInt(int(r)), nil

	case Bytes:
		// ord(bytes) returns int value of sole byte.
		if len(x) != 1 {
			return nil, fmt.Errorf("ord: bytes has length %d, want 1", len(x))
		}
		return MakeInt(int(x[0])), nil
	default:
		return nil, fmt.Errorf("ord: got %s, want string or bytes", x.Type())
	}
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#print
func print(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	sep := " "
	if err := UnpackArgs("print", nil, kwargs, "sep?", &sep); err != nil {
		return nil, err
	}
	limit := thread.stringLimit()
	j := formJob{kind: jobPrint, limit: limit, args: args, sep: sep}
	s, err := thread.buildForm(&j)
	if err != nil {
		return nil, err
	}
	if thread.Print != nil {
		thread.Print(thread, s)
	} else {
		fmt.Fprintln(os.Stderr, s)
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#range
func range_(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var start, stop, step int
	step = 1
	if err := UnpackPositionalArgs("range", args, kwargs, 1, &start, &stop, &step); err != nil {
		return nil, err
	}

	if len(args) == 1 {
		// range(stop)
		start, stop = 0, start
	}
	if step == 0 {
		// we were given range(start, stop, 0)
		return nil, nameErr(b, "step argument must not be zero")
	}

	n, ok := rangeLenChecked(start, stop, step)
	if !ok {
		return nil, nameErr(b, "range is too long")
	}
	return rangeValue{start: start, stop: stop, step: step, len: n}, nil
}

// A rangeValue is a comparable, immutable, indexable sequence of integers
// defined by the three parameters to a range(...) call.
// Invariant: step != 0.
type rangeValue struct{ start, stop, step, len int }

var (
	_ Indexable  = rangeValue{}
	_ Sequence   = rangeValue{}
	_ Comparable = rangeValue{}
	_ Sliceable  = rangeValue{}
	_ Container  = rangeValue{}
)

func (r rangeValue) Len() int          { return r.len }
func (r rangeValue) Index(i int) Value { return MakeInt(r.start + i*r.step) }
func (r rangeValue) Iterate() Iterator { return &rangeIterator{r, 0} }

// rangeLen calculates the length of a range with the provided start, stop, and step.
// caller must ensure that step is non-zero and that the length fits an int
// (see rangeLenChecked).
func rangeLen(start, stop, step int) int {
	n, ok := rangeLenChecked(start, stop, step)
	if !ok {
		panic("rangeLen: length overflows")
	}
	return n
}

// rangeLenChecked is rangeLen with the arithmetic done in uint64, where the
// difference of two ints cannot overflow, and reports whether the length fits
// an int. caller must ensure that step is non-zero.
func rangeLenChecked(start, stop, step int) (int, bool) {
	var span, by uint64
	switch {
	case step > 0:
		if stop <= start {
			return 0, true
		}
		span, by = uint64(stop)-uint64(start), uint64(step)
	case step < 0:
		if start <= stop {
			return 0, true
		}
		span, by = uint64(start)-uint64(stop), -uint64(step)
	default:
		panic("rangeLen: zero step")
	}
	n := (span-1)/by + 1
	if n > math.MaxInt {
		return 0, false
	}
	return int(n), true
}

func (r rangeValue) Slice(start, end, step int) Value {
	v, err := r.slice(start, end, step)
	if err != nil {
		return rangeValue{start: 0, stop: 0, step: 1, len: 0} // (see slice: callers that can fail use it)
	}
	return v
}

// slice is Slice for a caller that can report an error: the start, stop and
// step of the result are products and sums of ints, which can overflow.
func (r rangeValue) slice(start, end, step int) (Value, error) {
	errOverflow := fmt.Errorf("range slice is out of range")
	a, ok1 := mulInt(r.step, start)
	b, ok2 := mulInt(r.step, end)
	newStep, ok3 := mulInt(r.step, step)
	if !ok1 || !ok2 || !ok3 || newStep == 0 {
		return nil, errOverflow
	}
	newStart, ok4 := addInt(r.start, a)
	newStop, ok5 := addInt(r.start, b)
	if !ok4 || !ok5 {
		return nil, errOverflow
	}
	n, ok := rangeLenChecked(newStart, newStop, newStep)
	if !ok {
		return nil, errOverflow
	}
	return rangeValue{start: newStart, stop: newStop, step: newStep, len: n}, nil
}

// mulInt and addInt return the product and sum of two ints, and whether they
// are exact.
func mulInt(a, b int) (int, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	c := a * b
	if c/b != a || (a == math.MinInt && b == -1) || (b == math.MinInt && a == -1) {
		return 0, false
	}
	return c, true
}

func addInt(a, b int) (int, bool) {
	c := a + b
	if (c > a) != (b > 0) {
		return 0, false
	}
	return c, true
}

func (r rangeValue) Freeze() {} // immutable
func (r rangeValue) String() string {
	if r.step != 1 {
		return fmt.Sprintf("range(%d, %d, %d)", r.start, r.stop, r.step)
	} else if r.start != 0 {
		return fmt.Sprintf("range(%d, %d)", r.start, r.stop)
	} else {
		return fmt.Sprintf("range(%d)", r.stop)
	}
}
func (r rangeValue) Type() string          { return "range" }
func (r rangeValue) Truth() Bool           { return r.len > 0 }
func (r rangeValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: range") }

func (x rangeValue) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(rangeValue)
	switch op {
	case syntax.EQL:
		return rangeEqual(x, y), nil
	case syntax.NEQ:
		return !rangeEqual(x, y), nil
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

func (r rangeValue) Has(y Value) (bool, error) {
	i, err := NumberToInt(y)
	if err != nil {
		return false, fmt.Errorf("'in <range>' requires integer as left operand, not %s", y.Type())
	}
	return r.contains(i), nil
}

func rangeEqual(x, y rangeValue) bool {
	// Two ranges compare equal if they denote the same sequence.
	if x.len != y.len {
		return false // sequences differ in length
	}
	if x.len == 0 {
		return true // both sequences are empty
	}
	if x.start != y.start {
		return false // first element differs
	}
	return x.len == 1 || x.step == y.step
}

func (r rangeValue) contains(x Int) bool {
	x32, err := AsInt32(x)
	if err != nil {
		return false // out of range
	}
	delta := x32 - r.start
	quo, rem := delta/r.step, delta%r.step
	return rem == 0 && 0 <= quo && quo < r.len
}

type rangeIterator struct {
	r rangeValue
	i int
}

func (it *rangeIterator) Next(p *Value) bool {
	if it.i < it.r.len {
		*p = it.r.Index(it.i)
		it.i++
		return true
	}
	return false
}
func (*rangeIterator) Done() {}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#repr
func repr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("repr", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	limit := thread.stringLimit()
	j := formJob{kind: jobRepr, limit: limit, x: x}
	s, err := thread.buildForm(&j)
	if err != nil {
		return nil, err
	}
	// At the ceiling, where str reports an error, repr returns the bounded
	// form (see writeValue).
	return String(s), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#reversed
func reversed(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("reversed", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var elems []Value
	known := Len(args[0])
	if err := thread.chargeListOf(known, iterable); err != nil {
		return nil, excess(err, "reversed: excessive size (%d elements)", known)
	}
	if known >= 0 {
		elems = make([]Value, 0, known) // preallocate if length known
	}
	m := thread.meter()
	var x Value
	for iter.Next(&x) {
		if known < 0 {
			if err := m.add(iterWork(iterable)); err != nil {
				return nil, err
			}
			if err := thread.chargeOne(len(elems), allocBytesPerNewValue); err != nil {
				return nil, excess(err, "reversed: excessive size (over %d elements)", maxAlloc)
			}
		}
		elems = append(elems, x)
	}
	n := len(elems)
	for i := 0; i < n>>1; i++ {
		elems[i], elems[n-1-i] = elems[n-1-i], elems[i]
	}
	return NewList(elems), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set
func set(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("set", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	// The set and, if their number is known, its elements (duplicates are
	// charged: the size is an upper bound); else each element as it comes.
	n := 0
	if iterable != nil {
		n = Len(iterable)
	}
	if n >= 0 {
		// (duplicates are not charged: the entries that are added are)
		if err := thread.roomEntries(n); err != nil {
			return nil, excess(err, "set: excessive size (%d elements)", n)
		}
	}
	if err := thread.chargeEntries(0); err != nil {
		return nil, excess(err, "set: excessive size")
	}
	set := new(Set)
	if iterable != nil {
		iter := iterable.Iterate()
		defer iter.Done()
		m := thread.meter()
		var x Value
		for iter.Next(&x) {
			if n < 0 {
				if err := thread.chargeOne(set.Len(), allocBytesPerEntry); err != nil {
					return nil, excess(err, "set: excessive size (over %d elements)", maxAlloc)
				}
			}
			if err := set.insertM(&m, x); err != nil {
				return nil, nameErr(b, err)
			}
		}
		if n >= 0 {
			if err := thread.finishGrowth(0, &m, nil); err != nil {
				return nil, err
			}
		} else if err := m.flush(); err != nil { // (charged as it came)
			return nil, err
		}
	}
	return set, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#sorted
func sorted(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	// Oddly, Python's sorted permits all arguments to be positional, thus so do we.
	var iterable Iterable
	var key Callable
	var reverse bool
	if err := UnpackArgs("sorted", args, kwargs,
		"iterable", &iterable,
		"key?", &key,
		"reverse?", &reverse,
	); err != nil {
		return nil, err
	}

	iter := iterable.Iterate()
	defer iter.Done()
	var values []Value
	n := Len(iterable)
	if err := thread.chargeListOf(n, iterable); err != nil {
		return nil, excess(err, "sorted: excessive size (%d elements)", n)
	}
	if n > 0 {
		values = make(Tuple, 0, n) // preallocate if length is known
	}
	var x Value
	for iter.Next(&x) {
		if n < 0 {
			if err := thread.chargeOne(len(values), allocBytesPerNewValue); err != nil {
				return nil, excess(err, "sorted: excessive size (over %d elements)", maxAlloc)
			}
		}
		values = append(values, x)
	}

	// Derive keys from values by applying key function.
	var keys []Value
	if key != nil {
		if err := thread.chargeValues(len(values)); err != nil {
			return nil, excess(err, "sorted: excessive size (%d elements)", len(values))
		}
		keys = make([]Value, len(values))
		for i, v := range values {
			k, err := Call(thread, key, Tuple{v}, nil)
			if err != nil {
				return nil, err // to preserve backtrace, don't modify error
			}
			keys[i] = k
		}
	}

	slice := &sortSlice{keys: keys, values: values, m: thread.meter()}
	if reverse {
		stableSort(reverseSortSlice{slice}, len(slice.values))
	} else {
		stableSort(slice, len(slice.values))
	}
	if slice.stop != nil {
		return nil, slice.stop
	}
	if slice.err == nil {
		slice.err = slice.m.flush()
	}
	return NewList(slice.values), slice.err
}

type sortSlice struct {
	keys   []Value // nil => values[i] is key
	values []Value
	err    error
	m      meter // charged with the comparisons and the swaps
	stop   error // the error of m: the work is used up
	odd    bool  // a swap is half a unit: the swaps are counted in pairs
}

func (s *sortSlice) Len() int { return len(s.values) }

// reverseSortSlice is sort.Reverse of a sortSlice.
type reverseSortSlice struct{ *sortSlice }

func (r reverseSortSlice) Less(i, j int) bool { return r.sortSlice.Less(j, i) }
func (s *sortSlice) Less(i, j int) bool {
	keys := s.keys
	if s.keys == nil {
		keys = s.values
	}
	if s.stop != nil {
		return false // the work is used up: stop the comparisons
	}
	if err := s.m.add(1); err != nil {
		s.stop = err
		return false
	}
	ok, err := compareM(&s.m, syntax.LT, keys[i], keys[j], CompareLimit)
	if err != nil {
		s.err = err // (as before, the last error is the one reported)
		if isResourceError(err) {
			s.stop = err // a refusal of a limit ends the work: no more comparisons
		}
	}
	return ok
}
func (s *sortSlice) Swap(i, j int) {
	// A swap of two values (two slots, with the keys: four) takes half the time
	// of a comparison of small integers: a unit for each two.
	if s.stop == nil {
		if s.odd = !s.odd; !s.odd {
			if err := s.m.add(1); err != nil {
				s.stop = err
			}
		}
	}
	if s.keys != nil {
		s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
	}
	s.values[i], s.values[j] = s.values[j], s.values[i]
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#str
func str(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("str does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("str: got %d arguments, want exactly 1", len(args))
	}
	switch x := args[0].(type) {
	case String:
		return x, nil
	case Bytes:
		// Invalid encodings are replaced by that of U+FFFD. A valid string is
		// returned as it is, which allocates nothing.
		if n := utf8TranscodedLen(string(x)); n != len(x) {
			if err := thread.chargeBytes(n); err != nil {
				return nil, excess(err, "str: bytes value exceeds the size limit")
			}
		}
		return String(utf8Transcode(string(x))), nil
	default:
		if i, ok := x.(Int); ok {
			if _, big := i.get(); big == nil { // the form of a small integer is short: no machinery
				s := i.String()
				if err := thread.chargeBytes(len(s)); err != nil {
					return nil, excess(err, "str: value's string form exceeds the size limit")
				}
				return String(s), nil
			}
		}
		// Report an error if a value's string form exceeds the size bound
		// (writeValue stops at the limit, so only a bounded form reaches it).
		limit := thread.stringLimit()
		j := formJob{kind: jobStr, limit: limit, x: x}
		s, err := thread.buildForm(&j)
		if err != nil {
			return nil, err
		}
		return String(s), nil
	}
}

// utf8TranscodedLen returns len(utf8Transcode(s)) without building it.
func utf8TranscodedLen(s string) int {
	if utf8.ValidString(s) {
		return len(s)
	}
	n := 0
	for _, r := range s {
		n += utf8.RuneLen(r)
	}
	return n
}

// utf8Transcode returns the UTF-8-to-UTF-8 transcoding of s.
// The effect is that each code unit that is part of an
// invalid sequence is replaced by U+FFFD.
func utf8Transcode(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var out strings.Builder
	for _, r := range s {
		out.WriteRune(r)
	}
	return out.String()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#tuple
func tuple(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("tuple", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return Tuple(nil), nil
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var elems Tuple
	n := Len(iterable)
	// The elements if their number is known; else the header now and the
	// elements one by one, below.
	var cerr error
	if n >= 0 {
		cerr = thread.chargeTuple(n)
	} else {
		cerr = thread.charge(allocBaseTuple, allocBaseTuple)
	}
	if cerr != nil {
		return nil, excess(cerr, "tuple: excessive size (%d elements)", n)
	}
	if n > 0 {
		elems = make(Tuple, 0, n) // preallocate if length is known
	}
	m := thread.meter()
	var x Value
	for iter.Next(&x) {
		if n < 0 {
			if err := m.add(iterWork(iterable)); err != nil {
				return nil, err
			}
			if err := thread.chargeOne(len(elems), allocBytesPerNewValue); err != nil {
				return nil, excess(err, "tuple: excessive size (over %d elements)", maxAlloc)
			}
		}
		elems = append(elems, x)
	}
	return elems, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#type
func type_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("type does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("type: got %d arguments, want exactly 1", len(args))
	}
	return String(args[0].Type()), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#zip
func zip(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("zip does not accept keyword arguments")
	}
	rows, cols := 0, len(args)
	iters := make([]Iterator, cols)
	defer func() {
		for _, iter := range iters {
			if iter != nil {
				iter.Done()
			}
		}
	}()
	for i, seq := range args {
		it := Iterate(seq)
		if it == nil {
			return nil, fmt.Errorf("zip: argument #%d is not iterable: %s", i+1, seq.Type())
		}
		iters[i] = it
		n := Len(seq)
		if i == 0 || n < rows {
			rows = n // possibly -1
		}
	}
	var result []Value
	// A row is a slot of the result and a tuple of cols values: the size is
	// rows * cols values (and rows slots), whatever rows alone is, so a
	// zip of many ranges is refused before anything is made.
	rowBytes := zipRowBytes(cols)
	if rows >= 0 {
		// length known
		b := satAdd(allocBaseList, satMul(uint64(rows), rowBytes))
		if err := thread.charge(b, b); err != nil {
			return nil, excess(err, "zip: excessive size (%d rows of %d)", rows, cols)
		}
		result = make([]Value, rows)
		array := make(Tuple, cols*rows) // allocate a single backing array
		for i := 0; i < rows; i++ {
			tuple := array[:cols:cols]
			array = array[cols:]
			for j, iter := range iters {
				iter.Next(&tuple[j])
			}
			result[i] = tuple
		}
	} else {
		// length not known: the list now, and each row as it is made
		if err := thread.chargeValues(0); err != nil {
			return nil, excess(err, "zip: excessive size")
		}
		m := thread.meter() // one unit for each element of each column, as with a known length
	outer:
		for {
			tuple := make(Tuple, cols)
			for i, iter := range iters {
				if !iter.Next(&tuple[i]) {
					break outer
				}
			}
			if err := m.add(uint64(cols)); err != nil {
				return nil, err
			}
			if err := thread.charge(satMul(uint64(len(result))+1, rowBytes), rowBytes); err != nil {
				return nil, excess(err, "zip: excessive size (over %d rows)", maxAlloc)
			}
			result = append(result, tuple)
		}
		if err := m.flush(); err != nil {
			return nil, err
		}
	}
	return NewList(result), nil
}

// ---- methods of built-in types ---

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·get
func dict_get(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var key, dflt Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	m := thread.meter()
	v, ok, err := b.Receiver().(*Dict).getM(&m, key)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err)
	} else if ok {
		return v, nil
	} else if dflt != nil {
		return dflt, nil
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·clear
func dict_clear(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	return None, b.Receiver().(*Dict).Clear()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·items
func dict_items(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Dict)
	if err := thread.chargeItems(recv.Len()); err != nil {
		return nil, excess(err, "items: excessive size (%d entries)", recv.Len())
	}
	items := recv.Items()
	res := make([]Value, len(items))
	for i, item := range items {
		res[i] = item // convert [2]Value to Value
	}
	return NewList(res), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·keys
func dict_keys(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Dict)
	if err := thread.chargeValues(recv.Len()); err != nil {
		return nil, excess(err, "keys: excessive size (%d entries)", recv.Len())
	}
	return NewList(recv.Keys()), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·pop
func dict_pop(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var k, d Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &k, &d); err != nil {
		return nil, err
	}
	m := thread.meter()
	v, found, err := b.Receiver().(*Dict).deleteM(&m, k)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err) // dict is frozen or key is unhashable
	} else if found {
		return v, nil
	} else if d != nil {
		return d, nil
	}
	return nil, nameErr(b, "missing key")
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·popitem
func dict_popitem(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Dict)
	k, ok := recv.ht.first()
	if !ok {
		return nil, nameErr(b, "empty dict")
	}
	m := thread.meter()
	v, _, err := recv.deleteM(&m, k)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err) // dict is frozen
	}
	return Tuple{k, v}, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·setdefault
func dict_setdefault(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var key, dflt Value = nil, None
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	dict := b.Receiver().(*Dict)
	m := thread.meter()
	v, ok, err := dict.getM(&m, key)
	if err != nil {
		return nil, nameErr(b, err)
	}
	if ok {
		return v, m.flush()
	}
	before := dict.Len()
	if err := dict.setKeyM(&m, key, dflt); err != nil {
		return nil, nameErr(b, err)
	}
	if err := m.flush(); err != nil {
		return nil, err
	}
	if err := thread.chargeNewEntry(dict, before); err != nil {
		return nil, err
	}
	return dflt, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·update
func dict_update(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("update: got %d arguments, want at most 1", len(args))
	}
	if err := updateDict(thread, b.Receiver().(*Dict), args, kwargs, false); err != nil {
		return nil, prefixErr("update", err)
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#dict·update
func dict_values(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	// The values are collected through Items: a slot and a pair per entry.
	recv := b.Receiver().(*Dict)
	if err := thread.chargeItems(recv.Len()); err != nil {
		return nil, excess(err, "values: excessive size (%d entries)", recv.Len())
	}
	items := recv.Items()
	res := make([]Value, len(items))
	for i, item := range items {
		res[i] = item[1]
	}
	return NewList(res), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·append
func list_append(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var object Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &object); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*List)
	if err := recv.checkMutable("append to"); err != nil {
		return nil, nameErr(b, err)
	}
	recv.elems = append(recv.elems, object)
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·clear
func list_clear(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	if err := b.Receiver().(*List).Clear(); err != nil {
		return nil, nameErr(b, err)
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·extend
func list_extend(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := b.Receiver().(*List)
	var iterable Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("extend"); err != nil {
		return nil, nameErr(b, err)
	}
	if err := listExtend(thread, recv, iterable); err != nil {
		return nil, nameErr(b, err)
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·index
func list_index(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var value, start_, end_ Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &value, &start_, &end_); err != nil {
		return nil, err
	}

	recv := b.Receiver().(*List)
	start, end, err := indices(start_, end_, recv.Len())
	if err != nil {
		return nil, nameErr(b, err)
	}

	m := thread.meter()
	for i := start; i < end; i++ {
		if err := m.add(1); err != nil {
			return nil, err
		}
		if eq, err := equalM(&m, recv.elems[i], value, CompareLimit); err != nil {
			return nil, nameErr(b, err)
		} else if eq {
			return MakeInt(i), m.flush()
		}
	}
	if err := m.flush(); err != nil {
		return nil, err
	}
	return nil, nameErr(b, "value not in list")
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·insert
func list_insert(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := b.Receiver().(*List)
	var index int
	var object Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 2, &index, &object); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("insert into"); err != nil {
		return nil, nameErr(b, err)
	}

	if index < 0 {
		index += recv.Len()
	}

	if index >= recv.Len() {
		// end
		recv.elems = append(recv.elems, object)
	} else {
		if index < 0 {
			index = 0 // start
		}
		recv.elems = append(recv.elems, nil)
		copy(recv.elems[index+1:], recv.elems[index:]) // slide up one
		recv.elems[index] = object
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·remove
func list_remove(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := b.Receiver().(*List)
	var value Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &value); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("remove from"); err != nil {
		return nil, nameErr(b, err)
	}
	m := thread.meter()
	for i, elem := range recv.elems {
		if err := m.add(1); err != nil {
			return nil, err
		}
		if eq, err := equalM(&m, elem, value, CompareLimit); err != nil {
			return nil, prefixErr("remove", err)
		} else if eq {
			if err := m.add(workSlots(len(recv.elems) - i)); err != nil { // the slots after it move down
				return nil, err
			}
			recv.elems = append(recv.elems[:i], recv.elems[i+1:]...)
			return None, m.flush()
		}
	}
	if err := m.flush(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("remove: element not found")
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#list·pop
func list_pop(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := b.Receiver()
	list := recv.(*List)
	n := list.Len()
	i := n - 1
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0, &i); err != nil {
		return nil, err
	}
	origI := i
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		return nil, nameErr(b, outOfRange(origI, n, list))
	}
	if err := list.checkMutable("pop from"); err != nil {
		return nil, nameErr(b, err)
	}
	res := list.elems[i]
	list.elems = append(list.elems[:i], list.elems[i+1:]...)
	return res, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·capitalize
func string_capitalize(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	s := string(b.Receiver().(String))
	if err := thread.chargeBytes(len(s)); err != nil {
		return nil, excess(err, "%s: excessive size (%d bytes)", b.Name(), len(s))
	}
	res := new(strings.Builder)
	res.Grow(len(s))
	for i, r := range s {
		if i == 0 {
			r = unicode.ToTitle(r)
		} else {
			r = unicode.ToLower(r)
		}
		res.WriteRune(r)
	}
	return String(res.String()), nil
}

// string_iterable returns an unspecified iterable value whose iterator yields:
// - elems: successive 1-byte substrings
// - codepoints: successive substrings that encode a single Unicode code point.
// - elem_ords: numeric values of successive bytes
// - codepoint_ords: numeric values of successive Unicode code points
func string_iterable(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	s := b.Receiver().(String)
	ords := b.Name()[len(b.Name())-2] == 'd'
	codepoints := b.Name()[0] == 'c'
	if codepoints {
		return stringCodepoints{s, ords}, nil
	} else {
		return stringElems{s, ords}, nil
	}
}

// bytes_elems returns an unspecified iterable value whose
// iterator yields the int values of successive elements.
func bytes_elems(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	return bytesIterable{b.Receiver().(Bytes)}, nil
}

// A bytesIterable is an iterable returned by bytes.elems(),
// whose iterator yields a sequence of numeric bytes values.
type bytesIterable struct{ bytes Bytes }

var _ Iterable = (*bytesIterable)(nil)

func (bi bytesIterable) String() string        { return bi.bytes.String() + ".elems()" }
func (bi bytesIterable) Type() string          { return "bytes.elems" }
func (bi bytesIterable) Freeze()               {} // immutable
func (bi bytesIterable) Truth() Bool           { return True }
func (bi bytesIterable) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", bi.Type()) }
func (bi bytesIterable) Iterate() Iterator     { return &bytesIterator{bi.bytes} }

type bytesIterator struct{ bytes Bytes }

func (it *bytesIterator) Next(p *Value) bool {
	if it.bytes == "" {
		return false
	}
	*p = MakeInt(int(it.bytes[0]))
	it.bytes = it.bytes[1:]
	return true
}

func (*bytesIterator) Done() {}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·count
func string_count(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var sub string
	var start_, end_ Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &sub, &start_, &end_); err != nil {
		return nil, err
	}

	recv := string(b.Receiver().(String))
	start, end, err := indices(start_, end_, len(recv))
	if err != nil {
		return nil, nameErr(b, err)
	}

	var slice string
	if start < end {
		slice = recv[start:end]
	}
	return MakeInt(strings.Count(slice, sub)), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·isalnum
func string_isalnum(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	for _, r := range recv {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·isalpha
func string_isalpha(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	for _, r := range recv {
		if !unicode.IsLetter(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·isdigit
func string_isdigit(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	for _, r := range recv {
		if !unicode.IsDigit(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·islower
func string_islower(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	return Bool(isCasedString(recv) && recv == strings.ToLower(recv)), nil
}

// isCasedString reports whether its argument contains any cased code points.
func isCasedString(s string) bool {
	for _, r := range s {
		if isCasedRune(r) {
			return true
		}
	}
	return false
}

func isCasedRune(r rune) bool {
	// It's unclear what the correct behavior is for a rune such as 'ﬃ',
	// a lowercase letter with no upper or title case and no SimpleFold.
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || unicode.SimpleFold(r) != r
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·isspace
func string_isspace(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	for _, r := range recv {
		if !unicode.IsSpace(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·istitle
func string_istitle(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))

	// Python semantics differ from x==strings.{To,}Title(x) in Go:
	// "uppercase characters may only follow uncased characters and
	// lowercase characters only cased ones."
	var cased, prevCased bool
	for _, r := range recv {
		if 'A' <= r && r <= 'Z' || unicode.IsTitle(r) { // e.g. "ǅ"
			if prevCased {
				return False, nil
			}
			prevCased = true
			cased = true
		} else if unicode.IsLower(r) {
			if !prevCased {
				return False, nil
			}
			prevCased = true
			cased = true
		} else if unicode.IsUpper(r) {
			return False, nil
		} else {
			prevCased = false
		}
	}
	return Bool(cased), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·isupper
func string_isupper(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	return Bool(isCasedString(recv) && recv == strings.ToUpper(recv)), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·find
func string_find(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(thread, b, args, kwargs, true, false)
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·format
func string_format(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	// Every field can add as much as its argument: a format with many fields
	// and a large argument is an amplifier. Stop at the limit.
	limit := thread.stringLimit()
	j := formJob{kind: jobFormat, limit: limit, format: recv, args: args, kwargs: kwargs}
	s, err := thread.buildForm(&j)
	if err != nil {
		return nil, err
	}
	return String(s), nil
}

// stringFormatTo writes the result of format to buf (see Thread.buildForm: it
// is run twice for a large result).
func stringFormatTo(th *Thread, buf *sink, m *meter, limit int, format string, args Tuple, kwargs []Tuple) error {
	var auto, manual bool // kinds of positional indexing used
	var kwmap map[string]Value
	index := 0
	for {
		literal := format
		i := strings.IndexByte(format, '{')
		if i >= 0 {
			literal = format[:i]
		}

		// Replace "}}" with "}" in non-field portion, rejecting a lone '}'.
		for {
			j := strings.IndexByte(literal, '}')
			if j < 0 {
				buf.WriteString(literal)
				break
			}
			if len(literal) == j+1 || literal[j+1] != '}' {
				return fmt.Errorf("format: single '}' in format")
			}
			buf.WriteString(literal[:j+1])
			literal = literal[j+2:]
		}

		if i < 0 {
			break // end of format string
		}

		if i+1 < len(format) && format[i+1] == '{' {
			// "{{" means a literal '{'
			buf.WriteByte('{')
			format = format[i+2:]
			continue
		}

		format = format[i+1:]
		i = strings.IndexByte(format, '}')
		if i < 0 {
			return fmt.Errorf("format: unmatched '{' in format")
		}

		var arg Value
		conv := "s"
		var spec string

		field := format[:i]
		format = format[i+1:]

		var name string
		if i := strings.IndexByte(field, '!'); i < 0 {
			// "name" or "name:spec"
			if i := strings.IndexByte(field, ':'); i < 0 {
				name = field
			} else {
				name = field[:i]
				spec = field[i+1:]
			}
		} else {
			// "name!conv" or "name!conv:spec"
			name = field[:i]
			field = field[i+1:]
			// "conv" or "conv:spec"
			if before, after, ok := strings.Cut(field, ":"); !ok {
				conv = field
			} else {
				conv = before
				spec = after
			}
		}

		if name == "" {
			// "{}": automatic indexing
			if manual {
				return fmt.Errorf("format: cannot switch from manual field specification to automatic field numbering")
			}
			auto = true
			if index >= len(args) {
				return fmt.Errorf("format: tuple index out of range")
			}
			arg = args[index]
			index++
		} else if num, ok := decimal(name); ok {
			// positional argument
			if auto {
				return fmt.Errorf("format: cannot switch from automatic field numbering to manual field specification")
			}
			manual = true
			if num >= len(args) {
				return fmt.Errorf("format: tuple index out of range")
			} else {
				arg = args[num]
			}
		} else {
			// keyword argument (a map of them, if there are many: each field
			// would otherwise look at every one)
			if len(kwargs) > 8 {
				if kwmap == nil {
					kwmap = make(map[string]Value, len(kwargs))
					for i := len(kwargs) - 1; i >= 0; i-- { // (the first of two equal names wins)
						kwmap[string(kwargs[i][0].(String))] = kwargs[i][1]
					}
				}
				arg = kwmap[name]
			} else {
				if scanHook != nil {
					scanHook("format", len(kwargs))
				}
				for _, kv := range kwargs {
					if string(kv[0].(String)) == name {
						arg = kv[1]
						break
					}
				}
			}
			if arg == nil {
				// Starlark does not support Python's x.y or a[i] syntaxes,
				// or nested use of {...}.
				if strings.Contains(name, ".") {
					return fmt.Errorf("format: attribute syntax x.y is not supported in replacement fields: %s", errStr(name))
				}
				if strings.Contains(name, "[") {
					return fmt.Errorf("format: element syntax a[i] is not supported in replacement fields: %s", errStr(name))
				}
				if strings.Contains(name, "{") {
					return fmt.Errorf("format: nested replacement fields not supported")
				}
				return fmt.Errorf("format: keyword %s not found", errStr(name))
			}
		}

		if spec != "" {
			// Starlark does not support Python's format_spec features.
			return fmt.Errorf("format spec features not supported in replacement fields: %s", errStr(spec))
		}

		switch conv {
		case "s":
			if str, ok := AsString(arg); ok {
				if buf.Len()+len(str) >= limit {
					return th.refuseBytes(buf.Len()+len(str), "format: excessive result size")
				}
				buf.WriteString(str)
			} else if code, werr := writeValueMeter(buf, arg, limit, m); code != writeOK {
				return th.formErr(code, werr, limit, "format", "format: excessive result size")
			}
		case "r":
			if code, werr := writeValueMeter(buf, arg, limit, m); code != writeOK {
				return th.formErr(code, werr, limit, "format", "format: excessive result size")
			}
		default:
			return fmt.Errorf("format: unknown conversion %q", errStr(conv))
		}
	}
	return nil
}

// decimal interprets s as a sequence of decimal digits.
func decimal(s string) (x int, ok bool) {
	n := len(s)
	for i := range n {
		digit := s[i] - '0'
		if digit > 9 {
			return 0, false
		}
		x = x*10 + int(digit)
		if x < 0 {
			return 0, false // underflow
		}
	}
	return x, true
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·index
func string_index(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(thread, b, args, kwargs, false, false)
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·join
func string_join(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	var iterable Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	// The result is as long as the elements and the separators together,
	// n * len(sep) for n elements. Add up its length first, checking the
	// running total so as to stop at the limit, and write only then: the
	// buffer is never grown to a size that is going to be refused.
	limit := thread.stringLimit()
	total := 0
	check := func(i int, x Value) error {
		s, ok := AsString(x)
		if !ok {
			return fmt.Errorf("join: in list, want string, got %s", x.Type())
		}
		total += len(s)
		if i > 0 {
			total += len(recv)
		}
		if total >= limit {
			return thread.refuseBytes(total, "join: excessive result size")
		}
		return nil
	}
	var elems []Value
	switch it := iterable.(type) {
	case *List:
		elems = it.elems
		for i, x := range elems {
			if err := check(i, x); err != nil {
				return nil, err
			}
		}
	case Tuple:
		elems = it
		for i, x := range elems {
			if err := check(i, x); err != nil {
				return nil, err
			}
		}
	default:
		iter := iterable.Iterate()
		defer iter.Done()
		var x Value
		for i := 0; iter.Next(&x); i++ {
			if err := check(i, x); err != nil {
				return nil, err
			}
			// The elements are collected before they are joined.
			if err := thread.chargeOne(len(elems), allocBytesPerValue); err != nil {
				return nil, excess(err, "join: excessive size (over %d elements)", maxAlloc)
			}
			elems = append(elems, x)
		}
	}
	// total is below the limit: Call charges the result.
	if err := thread.chargeWork(workFast(total) + uint64(len(elems))); err != nil {
		return nil, err
	}
	buf := new(strings.Builder)
	buf.Grow(total)
	for i, x := range elems {
		if i > 0 {
			buf.WriteString(recv)
		}
		s, _ := AsString(x)
		buf.WriteString(s)
	}
	return String(buf.String()), nil
}

// changesCase reports whether ToLower (from, to = 'A', 'Z') or ToUpper
// ('a', 'z') of s may return a new string: s has a letter to convert, or a
// byte outside ASCII, which is converted by the Unicode tables.
func changesCase(s string, from, to byte) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= utf8.RuneSelf || from <= c && c <= to {
			return true
		}
	}
	return false
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·lower
func string_lower(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	s := string(b.Receiver().(String))
	if changesCase(s, 'A', 'Z') { // else the same string is returned
		if err := thread.chargeBytes(len(s)); err != nil {
			return nil, excess(err, "%s: excessive size (%d bytes)", b.Name(), len(s))
		}
	}
	return String(strings.ToLower(s)), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·partition
func string_partition(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	var sep string
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &sep); err != nil {
		return nil, err
	}
	if sep == "" {
		return nil, nameErr(b, "empty separator")
	}
	var i int
	if b.Name()[0] == 'p' {
		i = strings.Index(recv, sep) // partition
	} else {
		i = strings.LastIndex(recv, sep) // rpartition
	}
	scanned := len(recv)
	if i >= 0 {
		if b.Name()[0] == 'p' {
			scanned = i + len(sep)
		} else {
			scanned = len(recv) - i
		}
	}
	if err := thread.chargeWork(workSearch(scanned, len(sep))); err != nil {
		return nil, err
	}
	tuple := make(Tuple, 0, 3)
	if i < 0 {
		if b.Name()[0] == 'p' {
			tuple = append(tuple, String(recv), String(""), String(""))
		} else {
			tuple = append(tuple, String(""), String(""), String(recv))
		}
	} else {
		tuple = append(tuple, String(recv[:i]), String(sep), String(recv[i+len(sep):]))
	}
	return tuple, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·removeprefix
// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·removesuffix
func string_removefix(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	var fix string
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &fix); err != nil {
		return nil, err
	}
	if b.name[len("remove")] == 'p' {
		recv = strings.TrimPrefix(recv, fix)
	} else {
		recv = strings.TrimSuffix(recv, fix)
	}
	return String(recv), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·replace
func string_replace(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	var old, new string
	count := -1
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 2, &old, &new, &count); err != nil {
		return nil, err
	}
	// m replacements turn len(recv) into len(recv) + m*(len(new)-len(old)):
	// quadratic when both are large. Compute the size before replacing.
	// (strings.Count counts the empty string as len(recv)+1 runes.)
	if err := thread.chargeWork(workSearch(len(recv), len(old))); err != nil { // the scan
		return nil, err
	}
	m := strings.Count(recv, old)
	if count >= 0 && count < m {
		m = count // (count == 0: nothing is replaced)
	}
	if m == 0 || old == new {
		return String(recv), nil // the same string: nothing is allocated
	}
	size := uint64(len(recv))
	if len(new) >= len(old) {
		size = satAdd(size, satMul(uint64(m), uint64(len(new)-len(old))))
	} else {
		size -= uint64(m) * uint64(len(old)-len(new)) // matches do not overlap: no underflow
	}
	if err := thread.chargeWork(2 * uint64(m)); err != nil { // each match is a write
		return nil, err
	}
	if err := thread.charge(size, size); err != nil {
		return nil, excess(err, "replace: excessive result size (%d replacements of %d bytes by %d bytes)", m, len(old), len(new))
	}
	return String(strings.Replace(recv, old, new, count)), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·rfind
func string_rfind(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(thread, b, args, kwargs, true, true)
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·rindex
func string_rindex(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(thread, b, args, kwargs, false, true)
}

// https://github.com/google/starlark-go/starlark/blob/master/doc/spec.md#string·startswith
// https://github.com/google/starlark-go/starlark/blob/master/doc/spec.md#string·endswith
func string_startswith(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	var start, end Value = None, None
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &x, &start, &end); err != nil {
		return nil, err
	}

	// compute effective substring.
	s := string(b.Receiver().(String))
	if start, end, err := indices(start, end, len(s)); err != nil {
		return nil, nameErr(b, err)
	} else {
		if end < start {
			end = start // => empty result
		}
		s = s[start:end]
	}

	f := strings.HasPrefix
	if b.Name()[0] == 'e' { // endswith
		f = strings.HasSuffix
	}

	switch x := x.(type) {
	case Tuple:
		for i, x := range x {
			prefix, ok := AsString(x)
			if !ok {
				return nil, fmt.Errorf("%s: want string, got %s, for element %d",
					b.Name(), x.Type(), i)
			}
			if f(s, prefix) {
				return True, nil
			}
		}
		return False, nil
	case String:
		return Bool(f(s, string(x))), nil
	}
	return nil, fmt.Errorf("%s: got %s, want string or tuple of string", b.Name(), x.Type())
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·strip
// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·lstrip
// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·rstrip
func string_strip(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var chars string
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0, &chars); err != nil {
		return nil, err
	}
	recv := string(b.Receiver().(String))
	m := thread.meter()
	var left, right bool
	switch b.Name()[0] {
	case 's': // strip
		left, right = true, true
	case 'l': // lstrip
		left = true
	case 'r': // rstrip
		right = true
	}
	s, err := trimString(&m, recv, chars, left, right)
	if err != nil {
		return nil, err
	}
	if err := m.flush(); err != nil {
		return nil, err
	}
	return String(s), nil
}

// A cutset is the set of the characters of the argument of strip: the table of
// its ASCII characters, and a map of the others, built once, so that trimming
// is linear in the string and the set (strings.Trim looks every character of
// the string up in the set by a scan: the product of the two).
type cutset struct {
	ascii  [2]uint64
	others map[rune]struct{}
	n      int // the characters
}

func newCutset(chars string) *cutset {
	c := new(cutset)
	for _, r := range chars { // (an invalid byte is U+FFFD, as in strings.Trim)
		c.n++
		if r < utf8.RuneSelf {
			c.ascii[r>>6] |= 1 << (r & 63)
		} else {
			if c.others == nil {
				c.others = make(map[rune]struct{})
			}
			c.others[r] = struct{}{}
		}
	}
	return c
}

func (c *cutset) has(r rune) bool {
	if r < utf8.RuneSelf {
		return c.ascii[r>>6]&(1<<(r&63)) != 0
	}
	_, ok := c.others[r]
	return ok
}

// trimString is Trim, TrimLeft or TrimRight of recv by the characters chars
// (the white space if there are none), charging the work to m as it goes: a
// unit for each character of the cutset, and one for each two characters that
// are looked at.
func trimString(m *meter, recv, chars string, left, right bool) (string, error) {
	var in func(r rune) bool
	if chars == "" {
		in = unicode.IsSpace
	} else {
		if err := m.add(uint64(len(chars))); err != nil {
			return "", err
		}
		c := newCutset(chars)
		in = c.has
	}
	s := recv
	looked := uint64(0)
	if left {
		for len(s) > 0 {
			r, n := rune(s[0]), 1
			if r >= utf8.RuneSelf {
				r, n = utf8.DecodeRuneInString(s)
			}
			if !in(r) {
				break
			}
			s = s[n:]
			if looked++; looked%1024 == 0 {
				if err := m.add(512); err != nil {
					return "", err
				}
			}
		}
	}
	if right {
		for len(s) > 0 {
			r, n := rune(s[len(s)-1]), 1
			if r >= utf8.RuneSelf {
				r, n = utf8.DecodeLastRuneInString(s)
			}
			if !in(r) {
				break
			}
			s = s[:len(s)-n]
			if looked++; looked%1024 == 0 {
				if err := m.add(512); err != nil {
					return "", err
				}
			}
		}
	}
	return s, m.add(looked%1024/2 + 1)
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·title
func string_title(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}

	s := string(b.Receiver().(String))
	if err := thread.chargeBytes(len(s)); err != nil {
		return nil, excess(err, "%s: excessive size (%d bytes)", b.Name(), len(s))
	}

	// Python semantics differ from x==strings.{To,}Title(x) in Go:
	// "uppercase characters may only follow uncased characters and
	// lowercase characters only cased ones."
	buf := new(strings.Builder)
	buf.Grow(len(s))
	var prevCased bool
	for _, r := range s {
		if prevCased {
			r = unicode.ToLower(r)
		} else {
			r = unicode.ToTitle(r)
		}
		prevCased = isCasedRune(r)
		buf.WriteRune(r)
	}
	return String(buf.String()), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·upper
func string_upper(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	s := string(b.Receiver().(String))
	if changesCase(s, 'a', 'z') { // else the same string is returned
		if err := thread.chargeBytes(len(s)); err != nil {
			return nil, excess(err, "%s: excessive size (%d bytes)", b.Name(), len(s))
		}
	}
	return String(strings.ToUpper(s)), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·split
// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·rsplit
func string_split(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(b.Receiver().(String))
	var sep_ Value
	maxsplit := -1
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0, &sep_, &maxsplit); err != nil {
		return nil, err
	}

	var res []string

	// The result is a list of up to len(recv) new strings: count the fields
	// first, then check.
	// (rsplit on a separator splits the whole string and then joins the
	// leading fields, so it allocates all of them whatever maxsplit is.)
	var fields int
	clamp := true
	if sep_ == nil || sep_ == None {
		fields = fieldsCount(recv)
	} else if sep, ok := AsString(sep_); ok && sep != "" {
		fields = strings.Count(recv, sep) + 1
		clamp = b.Name() == "split"
	}
	if clamp && maxsplit >= 0 && maxsplit < fields-1 { // (not maxsplit+1: it overflows)
		fields = maxsplit + 1
	}
	if err := thread.chargeNewValues(fields); err != nil {
		return nil, excess(err, "%s: excessive size (%d elements)", b.Name(), fields)
	}
	// The scan, and a field is a new string.
	scan := workSearch(len(recv), 1)
	if sep, ok := AsString(sep_); ok {
		scan = 2 * workSearch(len(recv), len(sep)) // (Split counts, then searches)
	} else if sep_ == nil || sep_ == None {
		scan = workSlow(len(recv)) // whitespace is decided rune by rune
	}
	if err := thread.chargeWork(scan + 4*uint64(fields)); err != nil {
		return nil, err
	}

	// The usual forms make the list of values directly: not a list of strings
	// first, which is another 16 bytes a field.
	if sep_ == nil || sep_ == None {
		if maxsplit < 0 {
			return NewList(fieldsValues(recv, fields)), nil
		}
	} else if sep, ok := AsString(sep_); ok && sep != "" && (maxsplit < 0 || b.Name() == "split") {
		return NewList(splitValues(recv, sep, maxsplit, fields)), nil
	}

	if sep_ == nil || sep_ == None {
		// special case: split on whitespace
		if maxsplit < 0 {
			res = strings.Fields(recv)
		} else if b.Name() == "split" {
			res = splitspace(recv, maxsplit)
		} else { // rsplit
			res = rsplitspace(recv, maxsplit)
		}

	} else if sep, ok := AsString(sep_); ok {
		if sep == "" {
			return nil, fmt.Errorf("split: empty separator")
		}
		// usual case: split on non-empty separator
		if maxsplit < 0 {
			res = strings.Split(recv, sep)
		} else if b.Name() == "split" {
			res = strings.SplitN(recv, sep, maxsplit+1)
		} else { // rsplit
			res = strings.Split(recv, sep)
			if excess := len(res) - maxsplit; excess > 0 {
				res[0] = strings.Join(res[:excess], sep)
				res = append(res[:1], res[excess:]...)
			}
		}

	} else {
		return nil, fmt.Errorf("split: got %s for separator, want string", sep_.Type())
	}

	list := make([]Value, len(res))
	for i, x := range res {
		list[i] = String(x)
	}
	return NewList(list), nil
}

// fieldsValues is strings.Fields(s) as a slice of n (the number of fields) values.
func fieldsValues(s string, n int) []Value {
	res := make([]Value, 0, n)
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				res = append(res, String(s[start:i]))
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		res = append(res, String(s[start:]))
	}
	return res
}

// splitValues is strings.SplitN(s, sep, maxsplit+1) (all of them if maxsplit
// is negative) as a slice of n values.
func splitValues(s, sep string, maxsplit, n int) []Value {
	res := make([]Value, 0, n)
	for maxsplit != 0 {
		i := strings.Index(s, sep)
		if i < 0 {
			break
		}
		res = append(res, String(s[:i]))
		s = s[i+len(sep):]
		if maxsplit > 0 {
			maxsplit--
		}
	}
	return append(res, String(s))
}

// fieldsCount returns the number of fields strings.Fields(s) would return.
func fieldsCount(s string) int {
	n := 0
	inField := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inField = false
		} else if !inField {
			inField = true
			n++
		}
	}
	return n
}

// Precondition: max >= 0.
func rsplitspace(s string, max int) []string {
	// A field is at least a byte and a separator: preallocate no more than
	// the string can hold, whatever max is.
	res := make([]string, 0, min(max, len(s)/2)+1)
	end := -1 // index of field end, or -1 in a region of spaces.
	for i := len(s); i > 0; {
		r, sz := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsSpace(r) {
			if end >= 0 {
				if len(res) == max {
					break // let this field run to the start
				}
				res = append(res, s[i:end])
				end = -1
			}
		} else if end < 0 {
			end = i
		}
		i -= sz
	}
	if end >= 0 {
		res = append(res, s[:end])
	}

	resLen := len(res)
	for i := 0; i < resLen/2; i++ {
		res[i], res[resLen-1-i] = res[resLen-1-i], res[i]
	}

	return res
}

// Precondition: max >= 0.
func splitspace(s string, max int) []string {
	var res []string
	start := -1 // index of field start, or -1 in a region of spaces
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				if len(res) == max {
					break // let this field run to the end
				}
				res = append(res, s[start:i])
				start = -1
			}
		} else if start == -1 {
			start = i
		}
	}
	if start >= 0 {
		res = append(res, s[start:])
	}
	return res
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#string·splitlines
func string_splitlines(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var keepends bool
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0, &keepends); err != nil {
		return nil, err
	}
	var lines []Value
	if s := string(b.Receiver().(String)); s != "" {
		// One line per newline, and one more: count before splitting.
		n := strings.Count(s, "\n") + 1
		if err := thread.chargeNewValues(n); err != nil {
			return nil, excess(err, "splitlines: excessive size (%d elements)", n)
		}
		if err := thread.chargeWork(workFast(len(s)) + 4*uint64(n)); err != nil {
			return nil, err
		}
		// TODO(adonovan): handle CRLF correctly.
		lines = make([]Value, 0, n)
		for s != "" {
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				lines = append(lines, String(s))
				break
			}
			if keepends {
				lines = append(lines, String(s[:i+1]))
			} else {
				lines = append(lines, String(s[:i]))
			}
			s = s[i+1:]
		}
	}
	return NewList(lines), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·add.
func set_add(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var elem Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &elem); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Set)
	// It is always an error to attempt to mutate a set that cannot be mutated
	if err := recv.ht.checkMutable("insert into"); err != nil {
		return nil, nameErr(b, err)
	}
	// TODO(adonovan): opt: combine Has+Insert. (e.g. use Insert and re-check Len)
	m := thread.meter()
	if found, err := recv.hasM(&m, elem); err != nil {
		return nil, nameErr(b, err)
	} else if found {
		return None, m.flush()
	}
	before := recv.Len()
	err := recv.insertM(&m, elem)
	if err != nil {
		return nil, nameErr(b, err)
	}
	if err := m.flush(); err != nil {
		return nil, err
	}
	if err := thread.chargeNewSetEntry(recv, before); err != nil {
		return nil, err
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·clear.
func set_clear(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	if b.Receiver().(*Set).Len() > 0 {
		if err := b.Receiver().(*Set).Clear(); err != nil {
			return nil, nameErr(b, err)
		}
	}
	return None, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·difference.
func set_difference(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	// TODO: support multiple others: s.difference(*others)
	var other Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &other); err != nil {
		return nil, err
	}
	// The result is a copy of the receiver less some elements.
	if err := thread.roomEntries(b.Receiver().(*Set).Len()); err != nil {
		return nil, excess(err, "difference: excessive size (%d elements)", b.Receiver().(*Set).Len())
	}
	if err := thread.chargeEntries(0); err != nil {
		return nil, excess(err, "difference: excessive size (%d elements)", b.Receiver().(*Set).Len())
	}
	iter := other.Iterate()
	defer iter.Done()
	m := thread.meter()
	diff, err := b.Receiver().(*Set).differenceM(&m, iter)
	if err = thread.finishGrowth(0, &m, err); err != nil {
		return nil, nameErr(b, err)
	}
	return diff, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set_intersection.
func set_intersection(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	// TODO: support multiple others: s.difference(*others)
	var other Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &other); err != nil {
		return nil, err
	}
	// The result has at most as many elements as the receiver, and as
	// the other operand if its length is known.
	n := b.Receiver().(*Set).Len()
	if m := Len(other); m >= 0 && m < n {
		n = m
	}
	if err := thread.roomEntries(n); err != nil {
		return nil, excess(err, "intersection: excessive size (%d elements)", n)
	}
	if err := thread.chargeEntries(0); err != nil {
		return nil, excess(err, "intersection: excessive size (%d elements)", n)
	}
	iter := other.Iterate()
	defer iter.Done()
	m := thread.meter()
	diff, err := b.Receiver().(*Set).intersectionM(&m, iter)
	if err = thread.finishGrowth(0, &m, err); err != nil {
		return nil, nameErr(b, err)
	}
	return diff, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set_issubset.
func set_issubset(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var other Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &other); err != nil {
		return nil, err
	}
	iter := other.Iterate()
	defer iter.Done()
	m := thread.meter()
	diff, err := b.Receiver().(*Set).isSubsetM(&m, iter)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err)
	}
	return Bool(diff), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set_issuperset.
func set_issuperset(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var other Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &other); err != nil {
		return nil, err
	}
	iter := other.Iterate()
	defer iter.Done()
	m := thread.meter()
	diff, err := b.Receiver().(*Set).isSupersetM(&m, iter)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err)
	}
	return Bool(diff), nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·discard.
func set_discard(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var k Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &k); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Set)
	// It is always an error to attempt to mutate a set that cannot be mutated
	if err := recv.ht.checkMutable("delete from"); err != nil {
		return nil, nameErr(b, err)
	}
	// TODO(adonovan): opt: combine Has+Delete (e.g. use Delete and re-check Len)
	m := thread.meter()
	if found, err := recv.hasM(&m, k); err != nil {
		return nil, nameErr(b, err)
	} else if !found {
		return None, m.flush()
	}
	if _, err := recv.deleteM(&m, k); err != nil {
		return nil, nameErr(b, err) // set is frozen
	}
	return None, m.flush()
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·pop.
func set_pop(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := b.Receiver().(*Set)
	k, ok := recv.ht.first()
	if !ok {
		return nil, nameErr(b, "empty set")
	}
	m := thread.meter()
	_, err := recv.deleteM(&m, k)
	if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err) // set is frozen
	}
	return k, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·remove.
func set_remove(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var k Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &k); err != nil {
		return nil, err
	}
	m := thread.meter()
	if found, err := b.Receiver().(*Set).deleteM(&m, k); err != nil {
		return nil, nameErr(b, err) // dict is frozen or key is unhashable
	} else if found {
		return None, m.flush()
	}
	return nil, nameErr(b, "missing key")
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·symmetric_difference.
func set_symmetric_difference(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var other Iterable
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &other); err != nil {
		return nil, err
	}
	// The result has at most the elements of both operands (when the
	// length of the other is not known, Call charges the result).
	known := Len(other) >= 0
	n := b.Receiver().(*Set).Len() + max(Len(other), 0)
	// (the entries that are added are charged: not the elements of the other
	// that are in the receiver)
	if err := thread.roomEntries(n); err != nil {
		return nil, excess(err, "symmetric_difference: excessive size (%d elements)", n)
	}
	if err := thread.chargeEntries(0); err != nil {
		return nil, excess(err, "symmetric_difference: excessive size (%d elements)", n)
	}
	iter := other.Iterate()
	defer iter.Done()
	var ci *chargedIter
	if !known {
		// the elements of an iterable of unknown length are charged as they come
		ci = &chargedIter{Iterator: iter, thread: thread, have: b.Receiver().(*Set).Len(), unit: allocBytesPerEntry}
		iter = ci
	}
	m := thread.meter()
	diff, err := b.Receiver().(*Set).symmetricDifferenceM(&m, iter)
	if ci != nil && ci.err != nil {
		return nil, ci.err
	}
	if known {
		err = thread.finishGrowth(0, &m, err)
	} else if err == nil {
		err = m.flush()
	}
	if err != nil {
		return nil, nameErr(b, err)
	}
	return diff, nil
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·union.
func set_union(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	// The result has at most the elements of the receiver and the arguments.
	n := b.Receiver().(*Set).Len() + setUpdateLen(args)
	if err := thread.roomEntries(n); err != nil {
		return nil, excess(err, "union: excessive size (%d elements)", n)
	}
	if err := thread.chargeEntries(0); err != nil {
		return nil, excess(err, "union: excessive size (%d elements)", n)
	}
	m := thread.meter()
	receiverSet, err := b.Receiver().(*Set).cloneM(&m)
	if err != nil {
		return nil, nameErr(b, err)
	}
	if err := setUpdateM(thread, &m, receiverSet, args, kwargs); err != nil {
		return nil, nameErr(b, err)
	}
	return receiverSet, thread.finishGrowth(0, &m, nil)
}

// https://github.com/google/starlark-go/blob/master/doc/spec.md#set·update.
func set_update(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	// The arguments are copied into the receiver in one step: charge their
	// elements (an upper bound: duplicates are charged).
	recv := b.Receiver().(*Set)
	if n := setUpdateLen(args); n > 0 {
		if err := thread.roomGrowth(recv.Len(), n); err != nil {
			return nil, excess(err, "update: excessive size (%d elements)", n)
		}
	}
	before := recv.Len()
	m := thread.meter()
	err := setUpdateM(thread, &m, recv, args, kwargs)
	if err := thread.finishGrowth(before, &m, err); err != nil {
		return nil, nameErr(b, err)
	}
	return None, nil
}

// Common implementation of string_{r}{find,index}.
func string_find_impl(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple, allowError, last bool) (Value, error) {
	var sub string
	var start_, end_ Value
	if err := UnpackPositionalArgs(b.Name(), args, kwargs, 1, &sub, &start_, &end_); err != nil {
		return nil, err
	}

	s := string(b.Receiver().(String))
	start, end, err := indices(start_, end_, len(s))
	if err != nil {
		return nil, nameErr(b, err)
	}
	var slice string
	if start < end {
		slice = s[start:end]
	}

	var i int
	if last {
		i = strings.LastIndex(slice, sub)
	} else {
		i = strings.Index(slice, sub)
	}
	// The work is the distance searched (the whole slice if there is no match).
	scanned := len(slice)
	if i >= 0 {
		if last {
			scanned = len(slice) - i
		} else {
			scanned = i + len(sub)
		}
	}
	if err := thread.chargeWork(workSearch(scanned, len(sub))); err != nil {
		return nil, err
	}
	if i < 0 {
		if !allowError {
			return nil, nameErr(b, "substring not found")
		}
		return MakeInt(-1), nil
	}
	return MakeInt(i + start), nil
}

// Common implementation of builtin dict function and dict.update method.
// Precondition: len(updates) == 0 or 1.
//
// The entries that are ADDED are charged to thread, one by one as they are:
// the keys that are in the dict already are not (a dict that is updated with
// its own keys does not grow). If the number of entries of the argument is
// known, an update that could not fit is refused before it begins.
func updateDict(thread *Thread, dict *Dict, updates Tuple, kwargs []Tuple, fresh bool) error {
	if fresh {
		// the dict that dict() makes: its base, after the check that the
		// entries it is to be made of fit (a refusal charges nothing)
		n := 0
		if len(updates) == 1 {
			n = max(Len(updates[0]), 0)
		}
		if err := thread.roomEntries(n); err != nil {
			return excess(err, "excessive size (%d entries)", n)
		}
		if err := thread.chargeEntries(0); err != nil {
			return excess(err, "excessive size")
		}
	}
	m := thread.meter() // the work of the inserts: hash of the keys, chains
	// set adds a key and a value, and charges the entry if it is new.
	set := func(k, v Value) error {
		oldlen := dict.Len()
		if err := dict.setKeyM(&m, k, v); err != nil {
			return err // dict is frozen
		}
		return excess(thread.chargeNewEntry(dict, oldlen), "excessive size (over %d entries)", maxAlloc)
	}
	if len(updates) == 1 {
		switch updates := updates[0].(type) {
		case IterableMapping:
			// Iterate over dict's key/value pairs, not just keys.
			if n := Len(updates); n >= 0 {
				if err := thread.roomGrowth(dict.Len(), n); err != nil {
					return excess(err, "excessive size (%d entries)", n)
				}
			}
			items := updates.Items()
			if Len(updates) < 0 {
				if err := thread.roomGrowth(dict.Len(), len(items)); err != nil {
					return excess(err, "excessive size (%d entries)", len(items))
				}
			}
			for _, item := range items {
				if err := set(item[0], item[1]); err != nil {
					return err
				}
			}
		default:
			// all other sequences
			iter := Iterate(updates)
			if iter == nil {
				return fmt.Errorf("got %s, want iterable", updates.Type())
			}
			defer iter.Done()
			if n := Len(updates); n >= 0 {
				if err := thread.roomGrowth(dict.Len(), n); err != nil {
					return excess(err, "excessive size (%d entries)", n)
				}
			}
			var pair Value
			for i := 0; iter.Next(&pair); i++ {
				iter2 := Iterate(pair)
				if iter2 == nil {
					return fmt.Errorf("dictionary update sequence element #%d is not iterable (%s)", i, pair.Type())

				}
				defer iter2.Done()
				len := Len(pair)
				if len < 0 {
					return fmt.Errorf("dictionary update sequence element #%d has unknown length (%s)", i, pair.Type())
				} else if len != 2 {
					return fmt.Errorf("dictionary update sequence element #%d has length %d, want 2", i, len)
				}
				var k, v Value
				iter2.Next(&k)
				iter2.Next(&v)
				if err := set(k, v); err != nil {
					return err
				}
			}
		}
	}

	// Then add the kwargs.
	before := dict.Len()
	for _, pair := range kwargs {
		if err := set(pair[0], pair[1]); err != nil {
			return err
		}
	}
	// In the common case, each kwarg will add another dict entry.
	// If that's not so, check whether it is because there was a duplicate kwarg.
	if dict.Len() < before+len(kwargs) {
		keys := make(map[String]bool, len(kwargs))
		for _, kv := range kwargs {
			k := kv[0].(String)
			if keys[k] {
				return fmt.Errorf("duplicate keyword arg: %s", errValue(k))
			}
			keys[k] = true
		}
	}

	return m.flush()
}

// setUpdateLen returns the number of elements of the arguments of
// set.update or set.union whose length is known.
func setUpdateLen(args Tuple) int {
	n := 0
	for _, arg := range args {
		n += max(Len(arg), 0)
	}
	return n
}

func setUpdateM(thread *Thread, m *meter, s *Set, args Tuple, kwargs []Tuple) error {
	if len(kwargs) > 0 {
		return errors.New("does not accept keyword arguments")
	}

	for i, arg := range args {
		iterable, ok := arg.(Iterable)
		if !ok {
			return fmt.Errorf("argument #%d is not iterable: %s", i+1, arg.Type())
		}
		if err := func() error {
			iter := iterable.Iterate()
			defer iter.Done()
			if Len(arg) >= 0 {
				return s.insertAllM(m, iter) // charged by the caller
			}
			// Unknown length: charge each element as it comes.
			ci := &chargedIter{Iterator: iter, thread: thread, have: s.Len(), unit: allocBytesPerEntry}
			err := s.insertAllM(m, ci)
			if ci.err != nil {
				return ci.err
			}
			return err
		}(); err != nil {
			return err
		}
	}

	return nil
}

// nameErr returns an error message of the form "name: msg"
// where name is b.Name() and msg is a string or error.
//
// A budget error is returned unchanged (see prefixErr).
func nameErr(b *Builtin, msg any) error {
	switch err := msg.(type) {
	case *AllocBudgetError:
		return err
	case *WorkBudgetError:
		return err
	case *cancelledError:
		return err
	}
	return fmt.Errorf("%s: %v", b.Name(), msg)
}
