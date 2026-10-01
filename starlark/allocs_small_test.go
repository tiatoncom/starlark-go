package starlark_test

// The allocations of the short paths: the mechanism that counts and bounds
// what the built-ins make must not cost the program that makes a small string.
// The numbers are those of v0.2.0 (run this file in a tree of it to see them)
// and may not be exceeded.

import (
	"runtime"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func smallOps() []struct {
	name string
	f    func(th *starlark.Thread)
	max  float64 // allocations
	maxB float64 // bytes
} {
	call := func(name string, args ...starlark.Value) func(th *starlark.Thread) {
		fn := starlark.Universe[name]
		return func(th *starlark.Thread) { starlark.Call(th, fn, args, nil) }
	}
	binary := func(op syntax.Token, x, y starlark.Value) func(th *starlark.Thread) {
		return func(th *starlark.Thread) { starlark.Binary(op, x, y) }
	}
	prog := func(src string) func(th *starlark.Thread) {
		g, err := starlark.ExecFile(&starlark.Thread{}, "p.star", "def f(i, s):\n    return "+src+"\n", nil)
		if err != nil {
			panic(err)
		}
		f := g["f"]
		args := starlark.Tuple{starlark.MakeInt(5), starlark.String("abc")}
		return func(th *starlark.Thread) { starlark.Call(th, f, args, nil) }
	}
	small := starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.String("a")})
	return []struct {
		name string
		f    func(th *starlark.Thread)
		max  float64
		maxB float64
	}{
		{"str(int)", call("str", starlark.MakeInt(12345)), 0, 0},
		{"repr(int)", call("repr", starlark.MakeInt(12345)), 0, 0},
		{"repr(str)", call("repr", starlark.String("abc")), 0, 0},
		{"str(float)", call("str", starlark.Float(1.5)), 0, 0},
		{"str(list)", call("str", small), 0, 0},
		{"repr(list)", call("repr", small), 0, 0},
		{"def: '%d' % i", prog("'n=%d' % i"), 0, 0},
		{"def: '%s' % s", prog("'n=%s' % s"), 0, 0},
		{"def: '%s %d' % (s, i)", prog("'%s %d' % (s, i)"), 0, 0},
		{"def: '%x' % i", prog("'%x' % i"), 0, 0},
		{"def: '%5.2f' % 1.5", prog("'%f' % 1.5"), 0, 0},
		{"def: s + str(i)", prog("s + str(i)"), 0, 0},
		{"def: '{}'.format(i)", prog("'{}'.format(i)"), 0, 0},
		{"def: i + 1", prog("i + 1"), 0, 0},
		{"def: str(i)", prog("str(i)"), 0, 0},
		{"def: len(s)", prog("len(s)"), 0, 0},
		{"def: s.upper()", prog("s.upper()"), 0, 0},
		{"def: [i, i]", prog("[i, i]"), 0, 0},
		{"def: s.startswith('a')", prog("s.startswith('a')"), 0, 0},
		{"def: s.replace('a', 'b')", prog("s.replace('a', 'b')"), 0, 0},
		{"def: sorted([i, 1])", prog("sorted([i, 1])"), 0, 0},
		{"def: {i: s}", prog("{i: s}"), 0, 0},
		{"def: s in 'abcd'", prog("s in 'abcd'"), 0, 0},
		{"def: i in [1, 2, 3]", prog("i in [1, 2, 3]"), 0, 0},
		{"s + s", binary(syntax.PLUS, starlark.String("abc"), starlark.String("def")), 0, 0},
		{"s.format(i)", func(th *starlark.Thread) {
			m, _ := starlark.String("n={}").Attr("format")
			starlark.Call(th, m, starlark.Tuple{starlark.MakeInt(5)}, nil)
		}, 0, 0},
		{"s.upper()", func(th *starlark.Thread) {
			m, _ := starlark.String("abc").Attr("upper")
			starlark.Call(th, m, nil, nil)
		}, 0, 0},
		{"s.split(',')", func(th *starlark.Thread) {
			m, _ := starlark.String("a,b,c").Attr("split")
			starlark.Call(th, m, starlark.Tuple{starlark.String(",")}, nil)
		}, 0, 0},
		{"sorted(l)", call("sorted", starlark.NewList([]starlark.Value{starlark.MakeInt(3), starlark.MakeInt(1), starlark.MakeInt(2)})), 0, 0},
		{"d[k] = v", func(th *starlark.Thread) {
			d := new(starlark.Dict)
			d.SetKey(starlark.String("k"), starlark.MakeInt(1))
		}, 0, 0},
		{"list(l)", call("list", small), 0, 0},
		{"print(i)", func(th *starlark.Thread) {
			starlark.Call(th, starlark.Universe["print"], starlark.Tuple{starlark.MakeInt(1)}, nil)
		}, 0, 0},
	}
}

var smallBase = map[string][2]float64{ // allocations, bytes of v0.2.0
	"str(int)":                 {2, 21.4},
	"repr(int)":                {3, 37.3},
	"repr(str)":                {4, 48},
	"str(float)":               {2, 24},
	"str(list)":                {6, 88},
	"repr(list)":               {7, 104},
	"'%d' % i":                 {7, 88},
	"'%s' % s":                 {3, 56},
	"'%s %d' % (s, i)":         {7, 88},
	"def: '%d' % i":            {8, 152},
	"def: '%s' % s":            {4, 120},
	"def: '%s %d' % (s, i)":    {10, 224},
	"def: '%x' % i":            {8, 152},
	"def: '%5.2f' % 1.5":       {4, 120},
	"def: s + str(i)":          {5, 132},
	"def: '{}'.format(i)":      {7, 200},
	"def: i + 1":               {1, 64},
	"def: str(i)":              {3, 96},
	"def: len(s)":              {3, 96},
	"def: s.upper()":           {5, 136},
	"def: [i, i]":              {3, 128},
	"def: s.startswith('a')":   {7, 192},
	"def: s.replace('a', 'b')": {9, 241.2},
	"def: sorted([i, 1])":      {12, 353.2},
	"def: {i: s}":              {2, 608},
	"def: s in 'abcd'":         {1, 64.1},
	"def: i in [1, 2, 3]":      {3, 176.2},
	"s + s":                    {2, 24},
	"s.format(i)":              {5, 120},
	"s.upper()":                {3, 72},
	"s.split(',')":             {10, 264},
	"sorted(l)":                {8, 209},
	"d[k] = v":                 {1, 512},
	"list(l)":                  {5, 112},
	"print(i)":                 {4, 72},
}

// An operation on small operands allocates no more than in v0.2.0 (the bytes,
// with the 25 % that a field of a struct that is a counter takes in the one
// that holds it: sorted).
func TestSmallAllocs_NoMoreThanV020(t *testing.T) {
	th := &starlark.Thread{Name: "small", Print: func(*starlark.Thread, string) {}}
	for _, op := range smallOps() {
		base, ok := smallBase[op.name]
		if !ok {
			t.Errorf("%s: no number of v0.2.0", op.name)
			continue
		}
		allocs := testing.AllocsPerRun(200, func() { op.f(th) })
		var ms0, ms1 runtime.MemStats
		runtime.ReadMemStats(&ms0)
		for i := 0; i < 1000; i++ {
			op.f(th)
		}
		runtime.ReadMemStats(&ms1)
		bytes := float64(ms1.TotalAlloc-ms0.TotalAlloc) / 1000
		t.Logf("SMALL %-26s %5.1f allocs (v0.2.0 %4.1f) %7.1f B (%6.1f)", op.name, allocs, base[0], bytes, base[1])
		if allocs > base[0] {
			t.Errorf("%s: %.1f allocations, v0.2.0 made %.1f", op.name, allocs, base[0])
		}
		if bytes > base[1]*1.25+8 {
			t.Errorf("%s: %.1f bytes, v0.2.0 made %.1f", op.name, bytes, base[1])
		}
	}
}
