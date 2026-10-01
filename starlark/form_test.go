package starlark

// The string forms that are made in two passes (form.go) are the same bytes as
// the ones that are made in one, and the loops that were written out to
// allocate once are the standard library's.

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"unicode"

	"go.starlark.net/syntax"
)

// randString makes a string of n bytes with what a form has to escape: quotes,
// backslashes, control characters, multi-byte runes, invalid bytes.
func randString(r *rand.Rand, n int) string {
	pieces := []string{"a", "b", "z", "0", " ", "\"", "\\", "\n", "\t", "\x00", "\x7f", "é", "世", "\U0001F600", "\xff", "\xc3", "'", "<", "&"}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(pieces[r.Intn(len(pieces))])
	}
	return b.String()
}

func childSize(n int) int {
	if n > 100 {
		return 0 // a big container of leaves
	}
	return n / 4
}

// randValue makes a value of about n elements.
func randValue(r *rand.Rand, n int, depth int) Value {
	switch k := r.Intn(12); {
	case n <= 1 || depth > 6 || k < 5:
		switch r.Intn(8) {
		case 0:
			return MakeInt(r.Intn(2000) - 1000)
		case 1:
			return MakeInt64(r.Int63() * (1 - 2*int64(r.Intn(2))))
		case 2:
			return Float(r.NormFloat64() * 1e3)
		case 3:
			return Float(float64(r.Intn(100)))
		case 4:
			return Bool(r.Intn(2) == 0)
		case 5:
			return None
		case 6:
			return Bytes(randString(r, r.Intn(40)))
		default:
			return String(randString(r, r.Intn(200)))
		}
	case k < 8:
		l := make([]Value, 0, n)
		for i := 0; i < n; i++ {
			l = append(l, randValue(r, childSize(n), depth+1))
		}
		return NewList(l)
	case k < 10:
		t := make(Tuple, 0, n)
		for i := 0; i < n; i++ {
			t = append(t, randValue(r, childSize(n), depth+1))
		}
		return t
	default:
		d := new(Dict)
		for i := 0; i < n; i++ {
			d.SetKey(String(fmt.Sprintf("%s-%d", randString(r, 6), i)), randValue(r, childSize(n), depth+1))
		}
		return d
	}
}

// str, repr, %s, {} and print of a value of any size are the string form of
// the value, whether it is made in one pass or two.
func TestForm_TwoPassesGiveTheOneForm(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	sizes := []int{0, 1, 3, 20, 200, 2000, 8000}
	for iter := 0; iter < 60; iter++ {
		v := randValue(r, sizes[iter%len(sizes)], 0)
		want := v.String()
		var printed string
		th := &Thread{Name: "t", Print: func(_ *Thread, s string) { printed = s }}
		predeclared := StringDict{"v": v}
		if _, ok := v.(Bytes); ok || want == "" {
			continue // (str of bytes is its text, not its form)
		}
		g, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star",
			"a = str(v)\nb = repr(v)\nc = '%s' % (v,)\nd = '%r' % (v,)\ne = '{}'.format(v)\nf = '{!r}'.format(v)\nprint(v)\n", predeclared)
		if err != nil {
			t.Fatalf("iteration %d: %v", iter, err)
		}
		for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
			got := string(g[name].(String))
			w := want
			// repr of a string is its quoted form, str of it is the string
			if s, ok := v.(String); ok && (name == "a" || name == "c" || name == "e") {
				w = string(s)
			}
			if got != w {
				t.Fatalf("iteration %d (%d bytes): %s differs from the string form\n got %.100q\nwant %.100q", iter, len(want), name, got, w)
			}
		}
		if s, ok := v.(String); ok {
			want = string(s)
		}
		if printed != want {
			t.Fatalf("iteration %d: print differs from the string form", iter)
		}
	}
}

// The forms of floats and small integers are written without the string that
// Float.String and Int.String make.
func TestForm_FloatAndIntForms(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 20000; i++ {
		var f Float
		switch i % 5 {
		case 0:
			f = Float(r.NormFloat64())
		case 1:
			f = Float(float64(r.Int63n(1 << 40)))
		case 2:
			f = Float(r.NormFloat64() * 1e300)
		case 3:
			f = Float(r.NormFloat64() * 1e-300)
		default:
			f = Float(float64(r.Intn(1000)))
		}
		if got, want := string(appendFloatG(nil, f)), f.String(); got != want {
			t.Fatalf("appendFloatG(%v) = %q, want %q", float64(f), got, want)
		}
	}
	for _, f := range []Float{0, 1, -1, 1e21, 1e-7, 123456789, Float(math.Inf(1)), Float(math.Inf(-1)), Float(math.NaN())} {
		if got, want := string(appendFloatG(nil, f)), f.String(); got != want {
			t.Errorf("appendFloatG(%v) = %q, want %q", float64(f), got, want)
		}
	}
}

func TestForm_QuotedInPieces(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 3000; i++ {
		s := randString(r, r.Intn(700))
		for _, bytes := range []bool{false, true} {
			var out sink
			out.writeQuoted(s, bytes)
			if got, want := out.String(), syntax.Quote(s, bytes); got != want {
				t.Fatalf("writeQuoted(%q, %v) = %q, want %q", s, bytes, got, want)
			}
		}
	}
	// A string of continuation bytes only (no rune starts) is quoted too.
	s := strings.Repeat("\x80", 300)
	var out sink
	out.writeQuoted(s, false)
	if out.String() != syntax.Quote(s, false) {
		t.Errorf("a string of continuation bytes is quoted differently")
	}
}

// split, splitlines and the slices with a step make the values at once.
func TestForm_SplitsAreTheStandardLibrarys(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 3000; i++ {
		s := randString(r, r.Intn(300))
		s = strings.ReplaceAll(s, "z", " ")
		s = strings.ReplaceAll(s, "b", "\n")
		fields := strings.Fields(s)
		if got := fieldsValues(s, fieldsCount(s)); !sameStrings(got, fields) {
			t.Fatalf("fieldsValues(%q) = %v, want %q", s, got, fields)
		}
		for _, sep := range []string{"a", "é", "  ", "\n", "ab", "\x00"} {
			for _, max := range []int{-1, 0, 1, 2, 5} {
				want := strings.SplitN(s, sep, max+1)
				if max < 0 {
					want = strings.Split(s, sep)
				}
				if got := splitValues(s, sep, max, len(want)); !sameStrings(got, want) {
					t.Fatalf("splitValues(%q, %q, %d) = %v, want %q", s, sep, max, got, want)
				}
			}
		}
	}
	for _, c := range []struct{ src, want string }{
		{"''.splitlines()", "[]"},
		{"'a'.splitlines()", `["a"]`},
		{"'a\\n'.splitlines()", `["a"]`},
		{"'a\\nb'.splitlines()", `["a", "b"]`},
		{"'\\n'.splitlines()", `[""]`},
		{"'\\n\\n'.splitlines()", `["", ""]`},
		{"'a\\nb\\n'.splitlines(True)", `["a\n", "b\n"]`},
		{"'a\\nb'.splitlines(True)", `["a\n", "b"]`},
		{"'\\n'.splitlines(True)", `["\n"]`},
		{"'a b  c'.split()", `["a", "b", "c"]`},
		{"'a,b,,c'.split(',')", `["a", "b", "", "c"]`},
		{"'a,b,,c'.split(',', 1)", `["a", "b,,c"]`},
		{"'a,b,,c'.rsplit(',', 1)", `["a,b,", "c"]`},
		{"'a,b,,c'.split(',', 0)", `["a,b,,c"]`},
	} {
		g, err := ExecFileOptions(&syntax.FileOptions{}, &Thread{}, "t.star", "r = "+c.src+"\n", nil)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got := g["r"].String(); got != c.want {
			t.Errorf("%s = %s, want %s", c.src, got, c.want)
		}
	}
	// Slices with a step.
	for i := 0; i < 500; i++ {
		s := randString(r, r.Intn(100))
		l := make([]Value, len(s))
		for k := range l {
			l[k] = MakeInt(k)
		}
		for _, step := range []int{1, 2, 3, -1, -2, -5} {
			var start, end int
			if step > 0 {
				start, end = r.Intn(len(s)+1), len(s)
				if start > end {
					start = end
				}
			} else {
				start, end = len(s)-1, -1
			}
			var want []byte
			var wantL []Value
			for k := start; (step > 0 && k < end) || (step < 0 && k > end); k += step {
				want = append(want, s[k])
				wantL = append(wantL, l[k])
			}
			if got := string(String(s).Slice(start, end, step).(String)); got != string(want) {
				t.Fatalf("slice(%q, %d, %d, %d) = %q, want %q", s, start, end, step, got, want)
			}
			if got := string(Bytes(s).Slice(start, end, step).(Bytes)); got != string(want) {
				t.Fatalf("bytes slice differs")
			}
			if got := NewList(l).Slice(start, end, step).(*List); got.Len() != len(wantL) {
				t.Fatalf("list slice has %d elements, want %d", got.Len(), len(wantL))
			}
			if got := Tuple(l).Slice(start, end, step).(Tuple); len(got) != len(wantL) {
				t.Fatalf("tuple slice has %d elements, want %d", len(got), len(wantL))
			}
		}
	}
}

func sameStrings(got []Value, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if string(got[i].(String)) != want[i] {
			return false
		}
	}
	return true
}

// strip, lstrip and rstrip are strings.Trim, TrimLeft and TrimRight (and the
// white space forms), for any string and any set of characters.
func TestForm_TrimIsTheStandardLibrarys(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	for i := 0; i < 20000; i++ {
		s := randString(r, r.Intn(40))
		chars := randString(r, r.Intn(12))
		if r.Intn(4) == 0 {
			chars = ""
		}
		for k, c := range []struct {
			left, right bool
			want        string
		}{
			{true, true, ifEmpty(chars, strings.TrimSpace(s), strings.Trim(s, chars))},
			{true, false, ifEmpty(chars, strings.TrimLeftFunc(s, unicode.IsSpace), strings.TrimLeft(s, chars))},
			{false, true, ifEmpty(chars, strings.TrimRightFunc(s, unicode.IsSpace), strings.TrimRight(s, chars))},
		} {
			m := (&Thread{}).meter()
			got, err := trimString(&m, s, chars, c.left, c.right)
			if err != nil || got != c.want {
				t.Fatalf("case %d trim(%q, %q) = %q, want %q (%v)", k, s, chars, got, c.want, err)
			}
		}
	}
}

func ifEmpty(chars, a, b string) string {
	if chars == "" {
		return a
	}
	return b
}

// A value in the text of an error is printed whole if its form is of at most
// errValueLimit bytes, as in v0.2.0, and cut to that many bytes, marked, if it
// is longer: the boundary is exact.
func TestErrValue_TheBoundaryIsExact(t *testing.T) {
	for n := errValueLimit - 3; n <= errValueLimit+3; n++ {
		body := strings.Repeat("a", n-2) // the form is the string and its quotes
		got := errValue(String(body))
		if want := `"` + body + `"`; n <= errValueLimit {
			if got != want {
				t.Errorf("a form of %d bytes was not printed whole: %.40q", n, got)
			}
		} else {
			if !strings.HasPrefix(got, want[:errValueLimit]) || !strings.HasSuffix(got, writeValueOverflowMark) || len(got) != errValueLimit+len(writeValueOverflowMark) {
				t.Errorf("a form of %d bytes: %d bytes, %.40q...%q", n, len(got), got, got[len(got)-30:])
			}
		}
		// the same for a string of the script that is a name
		name := strings.Repeat("a", n)
		if got := errStr(name); n <= errValueLimit && got != name {
			t.Errorf("a name of %d bytes was cut", n)
		} else if n > errValueLimit && (len(got) <= errValueLimit || !strings.HasSuffix(got, fmt.Sprintf("...<%d bytes>", n))) {
			t.Errorf("a name of %d bytes was not cut: %.20q", n, got)
		}
	}
	// A list: whole up to the limit, and then its prefix of the limit.
	whole := errValue(NewList([]Value{String("x"), MakeInt(1), Tuple{None, Bool(true)}}))
	if whole != `["x", 1, (None, True)]` {
		t.Errorf("a short list: %q", whole)
	}
	long := errValue(NewList([]Value{String(strings.Repeat("a", 100)), String(strings.Repeat("b", 100)), String(strings.Repeat("c", 100))}))
	if !strings.HasSuffix(long, writeValueOverflowMark) || !strings.Contains(long, `"bbbb`) || strings.Contains(long, `"ccc`) && len(long) > errValueLimit+len(writeValueOverflowMark)+1 {
		t.Errorf("a long list: %q", long)
	}
}

// The size of the form of a value in an error message is 256 bytes: the text of
// a name or of a value up to it is whole, as it was in v0.2.0, and a longer one is
// cut there. (The constant is not used: a change of it is a change of every
// message of that kind.)
func TestErrValue_TheLimitIs256Bytes(t *testing.T) {
	for _, n := range []int{250, 253, 254, 255, 256, 257, 300, 1000} {
		s := String(strings.Repeat("k", n))
		got := errValue(s)
		if n+2 <= 256 { // with the quotes
			if want := `"` + string(s) + `"`; got != want {
				t.Errorf("a string of %d bytes: got %d bytes, want it whole", n, len(got))
			}
			continue
		}
		if want := `"` + strings.Repeat("k", 255) + writeValueOverflowMark; got != want {
			t.Errorf("a string of %d bytes: got %q (%d bytes), want the first 256 bytes and the mark", n, got, len(got))
		}
	}
	for _, n := range []int{255, 256, 257} {
		name := strings.Repeat("n", n)
		got := errStr(name)
		if n <= 256 && got != name {
			t.Errorf("a name of %d bytes was cut", n)
		}
		if n > 256 && got != strings.Repeat("n", 256)+fmt.Sprintf("...<%d bytes>", n) {
			t.Errorf("a name of %d bytes: got %q", n, got)
		}
	}
}
