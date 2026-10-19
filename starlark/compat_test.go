package starlark_test

// The Compatibility list: every observable difference of this version from
// v0.2.0 that is KEPT, one case each, as a fact that is tested and not as a
// sentence. Each case is a program, what v0.2.0 gives for it, what this version
// gives, and the bound that makes the difference (a deliberate limit of this
// version: nothing else is kept; a difference that is not a bound is returned
// to v0.2.0 and held by the differential tests, TestDifferential*).
//
// What v0.2.0 gives is checked against testdata/diff_compat_v020.golden, which
// was produced by running TestWriteCompatGolden in a tree of v0.2.0 (the commit
// d34a821); what this version gives, against a run of the program. A case that
// stops being a difference, or whose text moves, is red.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

const goldenCompat = "testdata/diff_compat_v020.golden"

// A compatOutcome is the status and the text of a run: the error, or the type
// and the value of `result`.
type compatOutcome struct{ Status, Text string }

func (o compatOutcome) String() string { return fmt.Sprintf("%s %.300q", o.Status, o.Text) }

func outcomeOf(d diffOutcome) compatOutcome {
	if d.Status == "ok" {
		return compatOutcome{"ok", d.Result}
	}
	return compatOutcome{d.Status, d.Err}
}

type compatCase struct {
	ID    string // the line of the Compatibility list
	Src   string
	V020  compatOutcome
	Now   compatOutcome
	Bound string // the bound of this version that makes the difference
	// Steps says that the steps of the run are not those of v0.2.0 (they are,
	// by default: the steps are the interpreter's alone).
	StepsDiffer bool
}

func compatCases() []compatCase {
	var cs []compatCase
	cs = append(cs, errTextCompatCases()...)
	cs = append(cs, digitsCompatCases()...)
	cs = append(cs, unionCompatCases()...)
	cs = append(cs, sortedCompatCases()...)
	return cs
}

const boundSortFirst = "sorted stops at the first error of a comparison (no comparison is made after it, so no time is spent on them): v0.2.0 went on sorting and named the last pair that failed"

// sortedCompatCases: sorted of elements that are not comparable. v0.2.0 kept
// sorting after the first failing comparison and reported the error of the LAST
// one; this version stops at the first, and reports it. The steps are the same.
// (The programs are those of the corpus of the small differential test, whose
// v0.2.0 outcome is in diff_v020.golden.)
func sortedCompatCases() []compatCase {
	was := compatOutcome{"error", "NoneType < string not implemented"}
	now := compatOutcome{"error", "string < int not implemented"}
	return []compatCase{
		{ID: "sorted of incomparable elements names the first pair, not the last", Bound: boundSortFirst,
			Src: "result = sorted([1, 'a', None])\n", V020: was, Now: now},
		{ID: "sorted of incomparable elements names the first pair, not the last (other statements)", Bound: boundSortFirst,
			Src: "v0 = sorted([1, 'a', None])\nv1 = {}.replace('a', 'b')\nresult = v0\n", V020: was, Now: now},
	}
}

const boundDepth = "MaxValueDepth / глубина сравнения: v0.2.0 молча обрывал объединение, v0.3.0 отказывает"

// unionCompatCases: the union d | e of dicts that have the same key, a tuple
// nested more deeply than a comparison goes (10 levels). Comparing the keys is
// refused ("comparison exceeded maximum recursion depth"); v0.2.0 dropped that
// error, the union stopped there, and the keys of e after it were not added.
func unionCompatCases() []compatCase {
	var cs []compatCase
	refused := compatOutcome{"error", "comparison exceeded maximum recursion depth"}
	for _, k := range []int{10, 12} {
		t := nest("tuple", k, "1")
		cs = append(cs,
			compatCase{ID: fmt.Sprintf("dict union with a tuple key nested %d deep", k), Bound: boundDepth, StepsDiffer: true,
				Src:  fmt.Sprintf("d = {%s: 1}\ne = {%s: 1}\nresult = d | e\n", t, t),
				V020: compatOutcome{"ok", fmt.Sprintf("dict:{%s: 1}", t)}, Now: refused},
			// the keys after the failing one are lost in v0.2.0, and the value is not updated
			compatCase{ID: fmt.Sprintf("dict union with a tuple key nested %d deep, more keys after it", k), Bound: boundDepth, StepsDiffer: true,
				Src:  fmt.Sprintf("d = {%s: 1}\ne = {%s: 2, 'z': 3}\nresult = d | e\n", t, t),
				V020: compatOutcome{"ok", fmt.Sprintf("dict:{%s: 1}", t)}, Now: refused},
		)
	}
	return cs
}

// digitsCompatCases are the integers of more than MaxIntDigits decimal digits
// that are converted to and from a string: v0.2.0 converted them (in time
// quadratic in the digits: a few steps for seconds of CPU), this version refuses
// the conversion with an error. (An integer longer than the bound is still an
// integer: it is computed with, compared and hashed.)
func digitsCompatCases() []compatCase {
	var cs []compatCase
	nines := func(n int) string { return strings.Repeat("9", n) }
	// the refusal is an error of the call: the default of json.decode is not
	// returned for it (B5: it was returned silently by an earlier state of
	// this version)
	jsonErr := "json.decode: number has more than 4300 digits"
	for _, c := range []struct{ id, arg, def, want string }{
		{"json.decode: an integer of 5000 digits", "'" + nines(5000) + "'", "", "int:" + nines(5000)},
		{"json.decode: a negative integer of 5000 digits", "'-" + nines(5000) + "'", "", "int:-" + nines(5000)},
		{"json.decode: an integer of 5000 digits in a list", "'[" + nines(5000) + ", 1]'", "", "list:[" + nines(5000) + ", 1]"},
		{"json.decode: an integer of 5000 digits in an object", "'{\"a\": " + nines(5000) + "}'", "", "dict:{\"a\": " + nines(5000) + "}"},
		{"json.decode: an integer of 5000 digits, with a default", "'" + nines(5000) + "'", ", default=7", "int:" + nines(5000)},
		{"json.decode: an integer of 4301 digits, with a default", "'" + nines(4301) + "'", ", default=None", "int:" + nines(4301)},
	} {
		cs = append(cs, compatCase{ID: c.id, Src: "result = json.decode(" + c.arg + c.def + ")\n",
			V020: compatOutcome{"ok", c.want}, Now: compatOutcome{"error", jsonErr}, Bound: boundDigits, StepsDiffer: true})
	}
	return cs
}

const (
	boundErrText = "the text of an error names an operand (a key, a name, a literal, an integer) whole up to 256 bytes of its form (value.go: errValueLimit), as in v0.2.0, and cut there above it: the message must not be as large as a value that can be megabytes"
	boundDigits  = "MaxIntDigits (4300): an integer of more decimal digits (or the bits of them in another base) is not converted from a string"
)

// errTextCompatCases are the cut error texts: one case for each kind of message
// that names an operand, with an operand of 1000 bytes (a 5000-digit one for the
// conversion bound).
func errTextCompatCases() []compatCase {
	var cs []compatCase
	for _, k := range errTextKinds() {
		if k.cutFrom == 0 {
			continue
		}
		n := 1000
		bound := boundErrText
		if k.now != nil {
			n = 5000
			bound = boundDigits
		}
		c := compatCase{ID: "error text: " + k.name, Src: k.mk(n), Bound: bound}
		if k.now != nil {
			w0, w1 := k.v020(n), k.now(n)
			c.V020, c.Now = compatOutcome{w0.status, w0.text}, compatOutcome{w1.status, w1.text}
			c.StepsDiffer = true // (the program stops at the refusal)
		} else {
			c.V020 = compatOutcome{"error", k.wantV020(n)}
			c.Now = compatOutcome{"error", k.wantNow(n)}
		}
		cs = append(cs, c)
	}
	// the string forms of an integer of 12000 digits (y = (10**4000 - 1) ** 3)
	pre := "x = int('" + nines(4000) + "')\ny = x * x * x\n"
	x, _ := new(big.Int).SetString(nines(4000), 10)
	y := new(big.Int).Mul(new(big.Int).Mul(x, x), x)
	ys := y.String()
	strErr := "an integer of more than 4300 decimal digits is not converted to a string"
	for _, c := range []struct{ id, expr, want, err string }{
		{"str of an integer of 12000 digits", "len(str(y))", fmt.Sprint(len(ys)), "str: " + strErr},
		{"repr of an integer of 12000 digits", "len(repr(y))", fmt.Sprint(len(ys)), "repr: " + strErr},
		{"%d of an integer of 12000 digits", "len('%d' % y)", fmt.Sprint(len(ys)), strErr},
		{"%s of an integer of 12000 digits", "len('%s' % y)", fmt.Sprint(len(ys)), "%: " + strErr},
		{"format of an integer of 12000 digits", "len('{}'.format(y))", fmt.Sprint(len(ys)), "format: " + strErr},
		{"str of a list with an integer of 12000 digits", "len(str([y]))", fmt.Sprint(len(ys) + 2), "str: " + strErr},
		{"json.encode of an integer of 12000 digits", "len(json.encode(y))", fmt.Sprint(len(ys)), "json.encode: " + strErr},
		{"int of a string of 5000 digits", "int('" + nines(5000) + "') > 0", "", "int: the string has more than 4300 digits: its conversion to an integer is not allowed"},
	} {
		want := compatOutcome{"ok", "int:" + c.want}
		if c.want == "" {
			want = compatOutcome{"ok", "bool:True"}
		}
		cs = append(cs, compatCase{ID: c.id, Src: pre + "result = " + c.expr + "\n",
			V020: want, Now: compatOutcome{"error", c.err}, Bound: boundDigits, StepsDiffer: true})
	}
	return cs
}

// TestWriteCompatGolden writes the golden file. It is run in a tree of v0.2.0,
// not here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark package>.
func TestWriteCompatGolden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden file (in a tree of v0.2.0)")
	}
	refuseInV030(t)
	var progs []string
	for _, c := range compatCases() {
		progs = append(progs, c.Src)
	}
	writeGolden(t, dir+"/"+goldenCompat, uniq(progs))
	// the cases of the host
	var b strings.Builder
	for _, h := range compatHostCases() {
		line, _ := json.Marshal(map[string]string{"id": h.ID, "out": h.Run()})
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(dir+"/"+goldenCompatHost, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goldenCompatHost = "testdata/diff_compat_host_v020.golden"

// A compatHost is a difference that a host sees through the Go API, not a
// script: what it calls returns Run(), in v0.2.0 and now.
type compatHost struct {
	ID    string
	Run   func() string
	V020  string
	Now   string
	Bound string
}

// compatHostCases: the string form (Value.String, fmt) of a container that
// holds an integer of more than MaxIntBits bits. The form of a value is the
// host's to ask for, with no error to return it in: the integer is shown as its
// size, "<int of N bits>", where v0.2.0 converted it (the conversion is
// quadratic in the digits). A script that asks for the same gets the error of
// the digits cases above.
func compatHostCases() []compatHost {
	z := new(big.Int).Exp(big.NewInt(10), big.NewInt(5000), nil) // 5001 digits, 16610 bits
	zs := z.String()
	bits := fmt.Sprintf("<int of %d bits>", z.BitLen())
	n := starlark.MakeBigInt(z)
	one := starlark.MakeInt(1)
	dict := func(k, v starlark.Value) starlark.Value {
		d := starlark.NewDict(1)
		_ = d.SetKey(k, v)
		return d
	}
	set := func(v starlark.Value) starlark.Value {
		s := starlark.NewSet(1)
		_ = s.Insert(v)
		return s
	}
	// The form of this version ends at the integer, and every bracket that was
	// open is closed: what follows the integer is not written (a dict whose key
	// is the integer is closed without its value).
	forms := []struct {
		id  string
		v   func() starlark.Value
		was string // the form of v0.2.0, with %s the integer
		now string // the form now, with %s the marker
	}{
		{"list", func() starlark.Value { return starlark.NewList([]starlark.Value{n, one}) }, "[%s, 1]", "[%s]"},
		{"list, the integer last", func() starlark.Value { return starlark.NewList([]starlark.Value{one, n}) }, "[1, %s]", "[1, %s]"},
		{"tuple", func() starlark.Value { return starlark.Tuple{one, n} }, "(1, %s)", "(1, %s)"},
		{"tuple of one", func() starlark.Value { return starlark.Tuple{n} }, "(%s,)", "(%s,)"},
		{"dict", func() starlark.Value { return dict(starlark.String("k"), n) }, `{"k": %s}`, `{"k": %s}`},
		{"dict with the integer as key", func() starlark.Value { return dict(n, one) }, `{%s: 1}`, `{%s}`},
		{"set", func() starlark.Value { return set(n) }, "set([%s])", "set([%s])"},
		{"list in a list", func() starlark.Value {
			return starlark.NewList([]starlark.Value{starlark.NewList([]starlark.Value{one, n, one}), one})
		}, "[[1, %s, 1], 1]", "[[1, %s]]"},
		{"dict in a tuple in a list", func() starlark.Value {
			return starlark.NewList([]starlark.Value{starlark.Tuple{dict(starlark.String("a"), starlark.NewList([]starlark.Value{n}))}, one})
		}, `[({"a": [%s]},), 1]`, `[({"a": [%s]},)]`},
		{"set in a dict", func() starlark.Value { return dict(one, set(n)) }, "{1: set([%s])}", "{1: set([%s])}"},
	}
	var hs []compatHost
	for _, f := range forms {
		f := f
		hs = append(hs, compatHost{
			ID:    "Value.String() of a " + f.id + " with an integer of 16610 bits",
			Run:   func() string { return f.v().String() },
			V020:  fmt.Sprintf(f.was, zs),
			Now:   fmt.Sprintf(f.now, bits),
			Bound: boundDigits,
		})
	}
	return hs
}

// TestCompatibilityDivergencesFromV020: each kept difference from v0.2.0 is
// what its case says, on both sides, exactly.
func TestCompatibilityDivergencesFromV020(t *testing.T) {
	golden := map[string]diffOutcome{}
	// (the programs of the small differential corpus are there too: a case may
	// be a program that it holds, whose v0.2.0 outcome is not written twice)
	for _, g := range readGolden(t, goldenSmall) {
		golden[g.Src] = g
	}
	for _, g := range readGolden(t, goldenErrText) { // (the error texts: the cut operands)
		golden[g.Src] = g
	}
	for _, g := range readGolden(t, goldenCompat) {
		golden[g.Src] = g
	}
	ids := map[string]bool{}
	for _, c := range compatCases() {
		if ids[c.ID] {
			t.Errorf("%s: two cases of one id", c.ID)
		}
		ids[c.ID] = true
		if c.Bound == "" {
			t.Errorf("%s: no bound named", c.ID)
		}
		g, ok := golden[c.Src]
		if !ok {
			t.Errorf("%s: the golden has no such program", c.ID)
			continue
		}
		got := runDiffProgram(c.Src)
		if o := outcomeOf(g); o != c.V020 {
			t.Errorf("%s: v0.2.0 gave\n   %v\n   the case says\n   %v", c.ID, o, c.V020)
		}
		if o := outcomeOf(got); o != c.Now {
			t.Errorf("%s: now\n   %v\n   the case says\n   %v", c.ID, o, c.Now)
		}
		if c.V020 == c.Now {
			t.Errorf("%s: not a difference (both sides: %v)", c.ID, c.Now)
		}
		if same := got.Steps == g.Steps; same == c.StepsDiffer {
			t.Errorf("%s: steps now %d, v0.2.0 %d (steps differ: %v)", c.ID, got.Steps, g.Steps, c.StepsDiffer)
		}
		if got.Prints != g.Prints {
			t.Errorf("%s: prints differ", c.ID)
		}
	}
	// the cases of the host
	hostGolden := map[string]string{}
	if data, err := os.ReadFile(goldenCompatHost); err != nil {
		t.Fatal(err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var e map[string]string
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatal(err)
			}
			hostGolden[e["id"]] = e["out"]
		}
	}
	for _, h := range compatHostCases() {
		if ids[h.ID] {
			t.Errorf("%s: two cases of one id", h.ID)
		}
		ids[h.ID] = true
		if g, ok := hostGolden[h.ID]; !ok || g != h.V020 {
			t.Errorf("%s: v0.2.0 gave %.100q (%d bytes), the case says %.100q (%d bytes)", h.ID, g, len(g), h.V020, len(h.V020))
		}
		if got := h.Run(); got != h.Now {
			t.Errorf("%s: now %.100q (%d bytes), the case says %.100q", h.ID, got, len(got), h.Now)
		}
		if h.V020 == h.Now || h.Bound == "" {
			t.Errorf("%s: not a difference, or no bound named", h.ID)
		}
	}
	if len(ids) < 20 {
		t.Errorf("only %d cases", len(ids))
	}
	var b strings.Builder
	for _, c := range compatCases() {
		fmt.Fprintf(&b, "\n  %-60s %s", c.ID, c.Bound[:min(len(c.Bound), 60)])
	}
	for _, h := range compatHostCases() {
		fmt.Fprintf(&b, "\n  %-60s %s", h.ID, h.Bound[:min(len(h.Bound), 60)])
	}
	t.Logf("the Compatibility list (%d):%s", len(ids), b.String())
}
