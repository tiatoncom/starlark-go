package starlark_test

// A differential test against v0.2.0 of the TEXT of the errors that name a
// value or a name of the script (B4 of the review of compatibility): a key, a
// keyword, an attribute, a literal, a format field, an integer out of range.
//
// Each kind of message is produced with an operand of every size around the
// bound. A message whose operand is of at most the bound (errTextWhole bytes
// of its form) is the text of v0.2.0, exactly, with the same steps. A longer
// operand is cut in the message (it must not be as large as a value that can
// be megabytes): that is a bound of this version, and it is a line of the
// Compatibility list for each kind of message (TestCompatibilityDivergencesFromV020),
// not a difference that these tests let pass: from the first size that is cut
// they assert the cut text, exactly.
//
// The expectations of v0.2.0 are in testdata/diff_errtext_v020.golden, produced
// by running TestWriteErrTextGolden in a tree of v0.2.0 (d34a821).

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
)

// errTextWhole is the size of the form of a value, in bytes, up to which an
// error message of this version names it whole (value.go: errValueLimit; the
// constant is not used, since a change of it is a change of the messages, and a
// test of the messages must notice it).
const errTextWhole = 256

const goldenErrText = "testdata/diff_errtext_v020.golden"

// errTextSizes are the sizes of the long operand, around the bounds of the
// forms: a string in quotes (n+2), a name (n), a tuple (n+8) and an integer
// (decimal digits).
var errTextSizes = []int{1, 10, 94, 95, 96, 97, 100, 200, 240, 247, 248, 249, 250, 251, 252, 253, 254, 255, 256, 257, 258, 259, 260, 300, 1000, 5000, 100000}

// How an operand is named in a message.
const (
	asValue      = iota // errValue of a string, a tuple: the first 256 bytes of the form and the mark
	asName              // errStr: the first 256 bytes and "...<N bytes>"
	asQuotedName        // errStr under %q
	asInt               // errValue of an integer: "<int of B bits>", marked when it is of at most MaxIntDigits
	asNone              // the message does not name the operand
)

type errTextKind struct {
	name string
	mk   func(n int) string // the program, for an operand of size n
	msg  string             // the message, %s the operand ("" if it names none)
	form func(n int) string // the operand as v0.2.0 prints it
	how  int
	// cutFrom is the first size at which the message is not v0.2.0's (0: never).
	cutFrom int
	// msgFrom is the first size at which msg is the message (below it the
	// operand is a valid one, or another message is given).
	msgFrom int
	// cut, if set, is what is named of the operand when it is cut (a tuple is
	// cut at an element, not at the byte).
	cut func(n int) string
	// v020 and now, if set, are the whole outcome (not derived from msg): a
	// program that v0.2.0 runs and this version refuses.
	v020, now func(n int) outcomeText
}

// outcomeText is a status and the text of an error or the type of a result.
type outcomeText struct{ status, text string }

func quoted(n int) string { return `"` + strings.Repeat("a", n) + `"` }
func aaa(n int) string    { return strings.Repeat("a", n) }
func nines(n int) string  { return strings.Repeat("9", n) }

func errTextKinds() []errTextKind {
	str := func(n int) string { return "'" + aaa(n) + "'" }
	return []errTextKind{
		{name: "dict key not found, string", mk: func(n int) string { return "d = {'x': 1}\nresult = d[" + str(n) + "]\n" },
			msg: "key %s not in dict", form: quoted, how: asValue, cutFrom: 255},
		{name: "dict key not found, tuple", mk: func(n int) string { return "d = {'x': 1}\nresult = d[(" + str(n) + ", 1)]\n" },
			msg: "key %s not in dict", form: func(n int) string { return "(" + quoted(n) + ", 1)" }, how: asValue, cutFrom: 252,
			cut: func(n int) string {
				form := "(" + quoted(n) + ", 1)"
				if n <= 253 { // the string fits (1 + n+2 < 257): the cut falls after it, at the element
					return "(" + quoted(n) + ", ...<truncated at size limit>"
				}
				return form[:errTextWhole] + "...<truncated at size limit>"
			}},
		{name: "dict key not found, int", mk: func(n int) string { return "d = {'x': 1}\nresult = d[" + nines(n) + "]\n" },
			msg: "key %s not in dict", form: nines, how: asInt, cutFrom: 257},
		{name: "dict key not found, negative int", mk: func(n int) string { return "d = {'x': 1}\nresult = d[-" + nines(n) + "]\n" },
			msg: "key %s not in dict", form: func(n int) string { return "-" + nines(n) }, how: asInt, cutFrom: 256},
		{name: "dict key not found, list of strings", mk: func(n int) string { return "d = {'x': 1}\nresult = d[[" + str(n) + "]]\n" }, how: asNone},
		{name: "duplicate key in a literal", mk: func(n int) string { return "result = {" + str(n) + ": 1, " + str(n) + ": 2}\n" },
			msg: "duplicate key: %s", form: quoted, how: asValue, cutFrom: 255},
		{name: "unexpected keyword of a function", mk: func(n int) string { return "def f(): pass\nresult = f(**{" + str(n) + ": 1})\n" }, how: asNone},
		{name: "multiple values for a parameter", mk: func(n int) string {
			return "def f(" + aaa(n) + "): pass\nresult = f(1, **{" + str(n) + ": 2})\n"
		}, msg: "function f got multiple values for parameter %s", form: quoted, how: asValue, cutFrom: 255},
		{name: "unexpected keyword of a built-in", mk: func(n int) string { return "result = sorted([], **{" + str(n) + ": 1})\n" },
			msg: "sorted: unexpected keyword argument %s", form: quoted, how: asValue, cutFrom: 255},
		{name: "duplicate keyword argument", mk: func(n int) string { return "result = dict(" + aaa(n) + "=1, **{" + str(n) + ": 2})\n" },
			msg: "dict: duplicate keyword arg: %s", form: quoted, how: asValue, cutFrom: 255},
		{name: "no such attribute (source)", mk: func(n int) string { return "result = 'abc'." + aaa(n) + "\n" },
			msg: "string has no .%s field or method", form: aaa, how: asName, cutFrom: 257},
		{name: "no such attribute (getattr)", mk: func(n int) string { return "result = getattr('abc', " + str(n) + ")\n" },
			msg: "getattr: string has no .%s field or method", form: aaa, how: asName, cutFrom: 257},
		{name: "no such attribute of a list (source)", mk: func(n int) string { return "result = [1]." + aaa(n) + "\n" },
			msg: "list has no .%s field or method", form: aaa, how: asName, cutFrom: 257},
		{name: "key not found in a format string", mk: func(n int) string { return "result = '%(" + aaa(n) + ")s' % {'x': 1}\n" },
			msg: "key not found: %s", form: aaa, how: asName, cutFrom: 257},
		{name: "invalid float literal", mk: func(n int) string { return "result = float(" + str(n) + ")\n" },
			msg: "invalid float literal: %s", form: aaa, how: asName, cutFrom: 257},
		{name: "invalid int literal", mk: func(n int) string { return "result = int(" + str(n) + ")\n" },
			msg: "int: invalid literal with base 10: %s", form: aaa, how: asName, cutFrom: 257},
		{name: "int(s, 16) of more than 4300 decimal digits", mk: func(n int) string { return "result = int(" + str(n) + ", 16)\n" },
			cutFrom: 5000, how: asNone,
			v020: func(n int) outcomeText {
				z, _ := new(big.Int).SetString(aaa(n), 16)
				return outcomeText{"ok", "int:" + z.String()}
			},
			now: func(n int) outcomeText {
				return outcomeText{"error", "int: the string has more than 4300 digits: its conversion to an integer is not allowed"}
			}},
		{name: "format: keyword not found", mk: func(n int) string { return "result = '{" + aaa(n) + "}'.format()\n" },
			msg: "format: keyword %s not found", form: aaa, how: asName, cutFrom: 257},
		{name: "format: attribute syntax", mk: func(n int) string { return "result = '{" + aaa(n) + ".x}'.format(**{'" + aaa(n) + "': 1})\n" },
			msg: "format: attribute syntax x.y is not supported in replacement fields: %s", form: func(n int) string { return aaa(n) + ".x" }, how: asName, cutFrom: 255},
		{name: "format: spec", mk: func(n int) string { return "result = '{0:" + aaa(n) + "}'.format(1)\n" },
			msg: "format spec features not supported in replacement fields: %s", form: aaa, how: asName, cutFrom: 257},
		{name: "format: conversion", mk: func(n int) string { return "result = '{0!" + aaa(n) + "}'.format(1)\n" },
			msg: "format: unknown conversion %s", form: aaa, how: asQuotedName, cutFrom: 257},
		{name: "%c with a string", mk: func(n int) string { return "result = '%c' % " + str(n) + "\n" }, how: asNone},
		{name: "int out of range: chr", mk: func(n int) string { return "result = chr(" + nines(n) + ")\n" },
			msg: "chr: %s out of range", form: nines, how: asInt, cutFrom: 257, msgFrom: 10},
		{name: "int out of range: index", mk: func(n int) string { return "result = [1][" + nines(n) + "]\n" },
			msg: "list index: %s out of range", form: nines, how: asInt, cutFrom: 257, msgFrom: 10},
		{name: "int out of range: repeat", mk: func(n int) string { return "result = [1] * " + nines(n) + "\n" }, how: asNone},
		{name: "int out of range: range", mk: func(n int) string { return "result = range(" + nines(n) + ")\n" },
			msg: "range: for parameter 1: %s out of range (want value in signed 64-bit range)", form: nines, how: asInt, cutFrom: 257, msgFrom: 20},
		{name: "int out of range: bytes", mk: func(n int) string { return "result = bytes([" + nines(n) + "])\n" },
			msg: "bytes: at index 0, %s out of range (want value in unsigned 8-bit range)", form: nines, how: asInt, cutFrom: 257, msgFrom: 10},
		{name: "int out of range: shift", mk: func(n int) string { return "result = 1 << " + nines(n) + "\n" },
			msg: "%s out of range", form: nines, how: asInt, cutFrom: 257, msgFrom: 10},
		{name: "int out of range: enumerate", mk: func(n int) string { return "result = enumerate([1], " + nines(n) + ")\n" },
			msg: "enumerate: for parameter 2: %s out of range (want value in signed 64-bit range)", form: nines, how: asInt, cutFrom: 257, msgFrom: 20},
		{name: "missing argument by a long name", mk: func(n int) string { return "def f(" + aaa(n) + "): pass\nresult = f()\n" }, how: asNone},
		{name: "unhashable key, list", mk: func(n int) string { return "result = {[" + str(n) + "]: 1}\n" }, how: asNone},
		{name: "fail with a long message", mk: func(n int) string { return "fail(" + str(n) + ")\n" }, how: asNone},
		{name: "hasattr with a long name", mk: func(n int) string { return "result = hasattr(1, " + str(n) + ")\n" }, how: asNone},
		{name: "load of a long name", mk: func(n int) string { return "load('m', " + str(n) + "='x')\n" }, how: asNone},
		{name: "unknown global (resolver)", mk: func(n int) string { return "result = " + aaa(n) + "\n" }, how: asNone},
		{name: "call of an unknown long name", mk: func(n int) string { return aaa(n) + "()\n" }, how: asNone},
	}
}

// formCut is what this version names of an operand whose v0.2.0 form is form.
func (k errTextKind) formCut(n int) string {
	if k.cut != nil {
		return k.cut(n)
	}
	form := k.form(n)
	switch k.how {
	case asValue:
		if len(form) <= errTextWhole {
			return form
		}
		return form[:errTextWhole] + "...<truncated at size limit>"
	case asName:
		if len(form) <= errTextWhole {
			return form
		}
		return form[:errTextWhole] + fmt.Sprintf("...<%d bytes>", len(form))
	case asQuotedName:
		if len(form) <= errTextWhole {
			return fmt.Sprintf("%q", form)
		}
		return fmt.Sprintf("%q", form[:errTextWhole]+fmt.Sprintf("...<%d bytes>", len(form)))
	case asInt:
		digits := strings.TrimPrefix(form, "-")
		if len(form) <= errTextWhole {
			return form
		}
		z, _ := new(big.Int).SetString(digits, 10)
		s := fmt.Sprintf("<int of %d bits>", z.BitLen())
		if z.BitLen() <= 14285 { // MaxIntBits: the conversion is refused above, and no mark follows
			s += "...<truncated at size limit>"
		}
		return s
	}
	panic("no operand")
}

// wantV020 and wantNow are the text of the error, in v0.2.0 and in this
// version, for an operand of size n.
func (k errTextKind) wantV020(n int) string {
	if k.how == asQuotedName {
		return fmt.Sprintf(k.msg, fmt.Sprintf("%q", k.form(n)))
	}
	return fmt.Sprintf(k.msg, k.form(n))
}

func (k errTextKind) wantNow(n int) string { return fmt.Sprintf(k.msg, k.formCut(n)) }

type errTextProg struct {
	kind errTextKind
	n    int
	src  string
}

func errTextCorpus() []errTextProg {
	var out []errTextProg
	for _, k := range errTextKinds() {
		for _, n := range errTextSizes {
			out = append(out, errTextProg{k, n, k.mk(n)})
		}
	}
	return out
}

// errTextSame are programs around int(s): where the answer is known without a
// conversion (an invalid literal, a string of zeros in base 0), the length of
// the string does not change it; they are v0.2.0's, text and steps.
var errTextSame = []string{
	"result = int('" + strings.Repeat("0", 5000) + "', 0)\n",
	"result = int('" + strings.Repeat("0", 5000) + "1x')\n",
	"result = int('" + strings.Repeat("9", 5000) + "x')\n",
	"result = int('x" + strings.Repeat("9", 5000) + "')\n",
	"result = int('" + strings.Repeat("0755", 2000) + "', 0)\n",
	"result = int('+-" + strings.Repeat("9", 5000) + "')\n",
	"result = int('-+" + strings.Repeat("9", 5000) + "')\n",
	"result = int('0x" + strings.Repeat("f", 5000) + "g', 16)\n",
	"result = int('" + strings.Repeat("f", 3000) + "', 16) > 0\n",
	"result = int('" + strings.Repeat("9", 4000) + "') > 0\n",
	"result = int('', 16)\n",
	"result = int('0x', 16)\n",
	"result = int('" + strings.Repeat(" ", 5000) + "')\n",
	"result = int('" + strings.Repeat("1", 4300) + "') > 0\n",
	"result = int('" + strings.Repeat("1", 4301) + "x')\n",
}

func errTextProgs() []string {
	var out []string
	for _, p := range errTextCorpus() {
		out = append(out, p.src)
	}
	out = append(out, errTextSame...)
	return uniq(out)
}

// TestWriteErrTextGolden writes the golden file. It is run in a tree of
// v0.2.0, not here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark
// package>.
func TestWriteErrTextGolden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden file (in a tree of v0.2.0)")
	}
	writeGolden(t, dir+"/"+goldenErrText, errTextProgs())
}

func sameOutcome(a, b diffOutcome) bool {
	return a.Status == b.Status && a.Result == b.Result && a.Err == b.Err && a.Prints == b.Prints && a.Steps == b.Steps
}

// TestDifferentialErrorTexts: the text of an error that names a value or a
// name is v0.2.0's up to the bound, and the cut text, exactly, from it.
func TestDifferentialErrorTexts(t *testing.T) {
	golden := map[string]diffOutcome{}
	for _, g := range readGolden(t, goldenErrText) {
		golden[g.Src] = g
	}
	same, cut := 0, 0
	for _, p := range errTextCorpus() {
		g, ok := golden[p.src]
		if !ok {
			t.Fatalf("%s, size %d: the golden has no such program", p.kind.name, p.n)
		}
		got := runDiffProgram(p.src)
		k := p.kind
		if k.cutFrom == 0 || p.n < k.cutFrom {
			if !sameOutcome(g, got) {
				t.Errorf("%s, size %d: not v0.2.0's\n   v0.2.0: %s %.200q steps=%d\n   now:    %s %.200q steps=%d", k.name, p.n, g.Status, g.Err+g.Result, g.Steps, got.Status, got.Err+got.Result, got.Steps)
			}
			// (and the message is the one the table says, when it names the operand)
			if k.msg != "" && p.n >= k.msgFrom && (g.Status != "error" || g.Err != k.wantV020(p.n)) {
				t.Errorf("%s, size %d: v0.2.0 said %.200q, the table says %.200q", k.name, p.n, g.Err, k.wantV020(p.n))
			}
			same++
			continue
		}
		cut++
		if k.now != nil { // a program that v0.2.0 runs and this version refuses
			w0, w1 := k.v020(p.n), k.now(p.n)
			if g.Status != w0.status || (w0.status == "error" && g.Err != w0.text) || (w0.status == "ok" && g.Result != w0.text) {
				t.Errorf("%s, size %d: v0.2.0 gave %s %.100q %.100q, the table says %v", k.name, p.n, g.Status, g.Err, g.Result, w0)
			}
			if got.Status != w1.status || got.Err != w1.text {
				t.Errorf("%s, size %d: now %s %.100q, the table says %v", k.name, p.n, got.Status, got.Err, w1)
			}
			continue
		}
		if g.Status != "error" || g.Err != k.wantV020(p.n) {
			t.Errorf("%s, size %d: v0.2.0 said %.200q, the table says %.200q", k.name, p.n, g.Err, k.wantV020(p.n))
		}
		if got.Status != "error" || got.Err != k.wantNow(p.n) || got.Steps != g.Steps || got.Prints != g.Prints {
			t.Errorf("%s, size %d: now %s %.300q steps=%d, the table says %.300q steps=%d", k.name, p.n, got.Status, got.Err, got.Steps, k.wantNow(p.n), g.Steps)
		}
	}
	for _, src := range errTextSame {
		g, ok := golden[src]
		if !ok {
			t.Fatalf("the golden has no program %.60q", src)
		}
		got := runDiffProgram(src)
		if !sameOutcome(g, got) && g.Status == "error" && got.Status == "error" && got.Steps == g.Steps && strings.HasPrefix(g.Err, "int: invalid literal with base ") {
			// the same message, with the operand (a string of more than 256
			// bytes) cut: the bound of the error texts, held above
			op := strings.Index(g.Err, ": invalid literal") // (the end of "int")
			op += strings.Index(g.Err[op+2:], ": ") + 4     // the start of the operand
			if strings.HasPrefix(got.Err, g.Err[:op+errTextWhole]) && strings.HasSuffix(got.Err, " bytes>") && got.Err[:op] == g.Err[:op] {
				same++
				continue
			}
		}
		if !sameOutcome(g, got) {
			t.Errorf("%.60q...: not v0.2.0's\n   v0.2.0: %s %.200q steps=%d\n   now:    %s %.200q steps=%d", src, g.Status, g.Err+g.Result, g.Steps, got.Status, got.Err+got.Result, got.Steps)
		}
		same++
	}
	if same < 500 || cut < 100 {
		t.Errorf("only %d sizes compared as v0.2.0's and %d as cut", same, cut)
	}
	t.Logf("%d messages identical to v0.2.0, %d cut at the bound", same, cut)
}
