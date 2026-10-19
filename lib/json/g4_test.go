package json_test

// Group G4a (round 2 of the review): the text of the error of json.encode of a
// deeply nested value is bounded and built once (N1), the depth of a value is
// charged by the one rule of the written forms, starlark.DeepFormWork (N2), and
// the text of an int by starlark.IntToStringWork (one function, not a copy).

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// g4TextMax is the bound of the text of an error of this package: the name of
// the function (at most 20 bytes: "json.encode_indent: "), the path of the
// containers (cut to 2048 bytes, since v0.2.0 names a key of 256 bytes whole
// and it is up to 1 KB quoted; marked: at most ~2080), and the cause (cut to
// 256 bytes and marked: at most ~290). 2500 holds the three.
const g4TextMax = 2500

// g4Out is the work of n bytes of output: 3/4 of a unit for the quoting, and
// the sixteenth that the allocation of the result costs.
func g4Out(n int) uint64 { return uint64(n)*3/4 + uint64(n)/16 }

func g4Nest(depth int, leaf starlark.Value) starlark.Value {
	v := leaf
	for i := 0; i < depth; i++ {
		v = starlark.NewList([]starlark.Value{v})
	}
	return v
}

func g4Call(th *starlark.Thread, name string, args ...starlark.Value) (starlark.Value, error) {
	return starlark.Call(th, json.Module.Members[name], starlark.Tuple(args), nil)
}

// g4Work is the work of one call of json.<name> on a fresh thread, and its error.
func g4Work(name string, args ...starlark.Value) (uint64, error) {
	th := &starlark.Thread{Name: "g4"}
	before := th.Work()
	_, err := g4Call(th, name, args...)
	return th.Work() - before, err
}

// N1: the text of the error of a deep value is bounded (a text of a level was
// wrapped by the level above: 161 KB at depth 9500).
func TestG4_EncodeErrorTextOfADeepValueIsBounded(t *testing.T) {
	v := g4Nest(9500, starlark.Float(math.Inf(1)))
	_, err := g4Call(&starlark.Thread{Name: "g4"}, "encode", v)
	if err == nil {
		t.Fatal("no error")
	}
	// (6 levels of "at list index 0: ", the marker, the cause: far under the bound)
	if len(err.Error()) > 300 {
		t.Errorf("the text of the error is %d bytes at depth 9500, want at most 300", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "cannot encode non-finite float +inf") {
		t.Errorf("the cause is lost: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "json.encode: at list index 0: ") {
		t.Errorf("the outermost levels are not named: %.80q", err.Error())
	}
	// A shallow value keeps the text of v0.2.0, whole.
	_, err = g4Call(&starlark.Thread{Name: "g4"}, "encode", g4Nest(3, starlark.Float(math.Inf(1))))
	const want = "json.encode: at list index 0: at list index 0: at list index 0: cannot encode non-finite float +inf"
	if err == nil || err.Error() != want {
		t.Errorf("shallow: got %v, want %q", err, want)
	}
}

// N1: the cost of a failing encode is linear in the depth: its work is the
// nodes and the depth charge, to within the chunk that is not yet charged, and
// the bytes that it allocates grow by 2x (not 4x) when the depth does.
func TestG4_EncodeFailingDeepIsLinear(t *testing.T) {
	alloc := func(d int) uint64 {
		v := g4Nest(d, starlark.Float(math.Inf(1)))
		var m1, m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)
		g4Call(&starlark.Thread{Name: "g4"}, "encode", v)
		runtime.ReadMemStats(&m2)
		return m2.TotalAlloc - m1.TotalAlloc
	}
	for _, d := range []int{1000, 4750, 9500} {
		w, err := g4Work("encode", g4Nest(d, starlark.Float(math.Inf(1))))
		if err == nil {
			t.Fatal("no error")
		}
		want := uint64(4 * (d + 1)) // a node each, the leaf too
		for k := 0; k < d; k++ {
			want += starlark.DeepFormWork(k)
		}
		// The meter charges a chunk of 1024 units at a time, the rest is not charged.
		if w > want || want-w >= 1024 {
			t.Errorf("depth %d: work %d, want %d (less up to 1023 not yet charged)", d, w, want)
		}
	}
	a1, a2 := alloc(4750), alloc(9500)
	if a2 > 5*a1/2 {
		t.Errorf("allocation of the failing encode: %d bytes at depth 4750, %d at 9500: not linear", a1, a2)
	}
}

// N2: json.encode charges the depth by starlark.DeepFormWork, for the levels
// that are lists, exactly.
func TestG4_EncodeChargesTheDepth(t *testing.T) {
	w1, err := g4Work("encode", g4Nest(1, starlark.None))
	if err != nil {
		t.Fatal(err)
	}
	// encode([None]) = F + 2 nodes (4 units each) + 6 bytes written
	fixed := w1 - 8 - g4Out(6)
	for _, d := range []int{20, 32, 33, 34, 100} {
		w, err := g4Work("encode", g4Nest(d, starlark.None))
		if err != nil {
			t.Fatal(err)
		}
		// d lists and a null: d+1 nodes, 2d+4 bytes
		want := fixed + uint64(4*(d+1)) + g4Out(2*d+4)
		for k := 0; k < d; k++ {
			want += starlark.DeepFormWork(k)
		}
		if w != want {
			t.Errorf("depth %d: work %d, want %d (old work + the depth charge)", d, w, want)
		}
	}
}

// N2: a tuple, a dict and a struct are entered by the same rule.
func TestG4_EncodeChargesTheDepthOfEveryContainer(t *testing.T) {
	const d = 40
	for _, c := range []struct {
		name string
		wrap func(starlark.Value) starlark.Value
		out  int // bytes written for a level
	}{
		{"tuple", func(v starlark.Value) starlark.Value { return starlark.Tuple{v} }, 2},
		{"dict", func(v starlark.Value) starlark.Value {
			dd := starlark.NewDict(1)
			dd.SetKey(starlark.String("k"), v)
			return dd
		}, 6}, // {"k":} is 6 bytes
		{"struct", func(v starlark.Value) starlark.Value {
			return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"k": v})
		}, 6},
	} {
		build := func(n int) starlark.Value {
			var v starlark.Value = starlark.None
			for i := 0; i < n; i++ {
				v = c.wrap(v)
			}
			return v
		}
		w1, err := g4Work("encode", build(1))
		if err != nil {
			t.Fatal(c.name, err)
		}
		wd, err := g4Work("encode", build(d))
		if err != nil {
			t.Fatal(c.name, err)
		}
		w30, err := g4Work("encode", build(30)) // (above 32 levels the depth is charged)
		if err != nil {
			t.Fatal(c.name, err)
		}
		// Without the depth charge a level costs the same (up to the rounding of
		// the quarters and sixteenths of the bytes, and of the items of a dict):
		// w1 + (d-1) levels of (w30-w1)/29. The depth is charged above that, by
		// DeepFormWork for each level, which is 288 units for 40 levels: much more
		// than the tolerance.
		var deep uint64
		for k := 0; k < d; k++ {
			deep += starlark.DeepFormWork(k)
		}
		flat := w1 + uint64(d-1)*(w30-w1)/29
		if wd < flat+deep-8 || wd > flat+deep+8 {
			t.Errorf("%s: work at depth %d is %d, want %d (the levels) + %d (the depth) = %d, +-8", c.name, d, wd, flat, deep, flat+deep)
		}
	}
}

// 3: the text of an int is charged by starlark.IntToStringWork, exactly.
func TestG4_EncodeChargesIntsByIntToStringWork(t *testing.T) {
	w5, err := g4Work("encode", starlark.MakeInt(5))
	if err != nil {
		t.Fatal(err)
	}
	for _, digits := range []int{20, 100, 600, 4300} {
		b := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits-1)), nil)
		w, err := g4Work("encode", starlark.MakeBigInt(b))
		if err != nil {
			t.Fatal(digits, err)
		}
		// the same node as w5, the conversion, and the digits written
		want := w5 + starlark.IntToStringWork(b, true) + g4Out(digits)
		if w != want {
			t.Errorf("%d digits: work %d, want %d", digits, w, want)
		}
	}
}

// ---- the sweep: every error path of the module, with huge operands ----

type g4Huge struct {
	name string
	err  string // the text of the error of MarshalJSON / Attr
}

func (h g4Huge) String() string        { return "g4Huge" }
func (h g4Huge) Type() string          { return "g4Huge" }
func (h g4Huge) Freeze()               {}
func (h g4Huge) Truth() starlark.Bool  { return true }
func (h g4Huge) Hash() (uint32, error) { return 0, nil }
func (h g4Huge) MarshalJSON() ([]byte, error) {
	return nil, errors.New(h.err)
}

// g4Attrs has attributes whose names and errors are huge.
type g4Attrs struct{ name, err string }

func (h g4Attrs) String() string        { return "g4Attrs" }
func (h g4Attrs) Type() string          { return "g4Attrs" }
func (h g4Attrs) Freeze()               {}
func (h g4Attrs) Truth() starlark.Bool  { return true }
func (h g4Attrs) Hash() (uint32, error) { return 0, nil }
func (h g4Attrs) AttrNames() []string   { return []string{h.name} }
func (h g4Attrs) Attr(name string) (starlark.Value, error) {
	if h.err == "" {
		return nil, nil // "missing attribute"
	}
	return nil, errors.New(h.err)
}

func TestG4_ErrorTextsAreBounded(t *testing.T) {
	huge := strings.Repeat("k", 50<<20)
	hugeBytes := strings.Repeat("\x00", 1<<20) // quotes to 6 bytes a byte
	inf := starlark.Float(math.Inf(1))
	var cyc starlark.Value
	{
		inner := starlark.NewList(nil)
		top := starlark.Value(inner)
		for i := 0; i < 9500; i++ {
			top = starlark.NewList([]starlark.Value{top})
		}
		inner.Append(top)
		cyc = top
	}
	bigInt := starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 8<<20))
	dictAt := func(k string, v starlark.Value) starlark.Value {
		d := starlark.NewDict(1)
		d.SetKey(starlark.String(k), v)
		return d
	}
	nonStringKey := starlark.NewDict(1)
	nonStringKey.SetKey(starlark.MakeInt(1), starlark.None)

	type call struct {
		name string
		fn   string
		arg  starlark.Value
	}
	cases := []call{
		{"deep, non-finite", "encode", g4Nest(9500, inf)},
		{"deep, non-finite, indent", "encode_indent", g4Nest(9500, inf)},
		{"deep, 50 MB key", "encode", g4Nest(9500, dictAt(huge, inf))},
		{"deep, key of NULs", "encode", g4Nest(9500, dictAt(hugeBytes, inf))},
		{"deep dicts of keys of NULs", "encode", func() starlark.Value {
			var v starlark.Value = inf
			for i := 0; i < 400; i++ {
				v = dictAt(hugeBytes[:2000], v)
			}
			return v
		}()},
		{"deep, struct with a 50 MB field", "encode", g4Nest(9500, starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{huge: inf}))},
		{"deep, cycle", "encode", cyc},
		{"too deep", "encode", g4Nest(starlark.MaxValueDepth+5, starlark.None)},
		{"deep, 8M-bit int", "encode", g4Nest(9500, bigInt)},
		{"deep, function", "encode", g4Nest(9500, starlark.NewBuiltin("f"+huge[:300], nil))},
		{"deep, non-string key", "encode", g4Nest(9500, nonStringKey)},
		{"deep, host error of 50 MB", "encode", g4Nest(9500, g4Huge{err: huge})},
		{"deep, attribute error of 50 MB", "encode", g4Nest(9500, g4Attrs{name: "a", err: huge})},
		{"deep, attribute of 50 MB name, error", "encode", g4Nest(9500, g4Attrs{name: huge, err: "x"})},
		{"deep, attribute of 50 MB name, missing", "encode", g4Nest(9500, g4Attrs{name: huge})},
		{"decode, 50 MB of garbage", "decode", starlark.String(huge)},
		{"decode, 50 MB number", "decode", starlark.String("1" + strings.Repeat("0", 50<<20) + "x")},
		{"decode, 50 MB float", "decode", starlark.String("1." + strings.Repeat("0", 50<<20) + "x")},
		{"decode, 50 MB unterminated string", "decode", starlark.String("\"" + huge)},
		{"decode, deep, then garbage", "decode", starlark.String(strings.Repeat("[", 9500) + "x")},
		{"decode, too deep", "decode", starlark.String(strings.Repeat("[", 10050))},
		{"decode, too deep object", "decode", starlark.String(strings.Repeat("{\"a\":", 10050))},
		{"decode, 50 MB key", "decode", starlark.String("{\"" + huge + "\":")},
		{"decode, 8M digits", "decode", starlark.String(strings.Repeat("9", 8<<20))},
		{"decode, trailing", "decode", starlark.String("[]" + huge)},
		{"indent, 50 MB of garbage", "indent", starlark.String(huge)},
		{"indent, deep, then garbage", "indent", starlark.String(strings.Repeat("[", 9500) + "x")},
		{"indent, 50 MB unterminated", "indent", starlark.String("[\"" + huge)},
	}
	for _, c := range cases {
		th := &starlark.Thread{Name: "sweep"}
		_, err := g4Call(th, c.fn, c.arg)
		if err == nil {
			// (a case that works is not an error path: it does not belong to the sweep)
			t.Errorf("%s: no error", c.name)
			continue
		}
		if n := len(err.Error()); n > g4TextMax {
			t.Errorf("%s: the text of the error is %d bytes, want at most %d: %.100q...", c.name, n, g4TextMax, err.Error())
		}
	}
	// The same with a budget of allocation (errors of the budget are fixed text).
	th := &starlark.Thread{Name: "sweep"}
	th.SetMaxAllocBytes(1 << 20)
	_, err := g4Call(th, "encode", starlark.String(huge))
	if err == nil || len(err.Error()) > g4TextMax {
		t.Errorf("encode of 50 MB with a budget: %v", fmt.Sprint(err)[:min(len(fmt.Sprint(err)), 100)])
	}
}
