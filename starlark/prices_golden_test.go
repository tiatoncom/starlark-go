package starlark

// A golden test of the prices: the EXACT work that every priced built-in and
// method is charged on a fixed set of operands, one row each, in
// testdata/prices.golden. The floor test (TestPrices_AtLeastTheMeasuredCost)
// holds a price from below only; this one holds the value, so a price that is
// halved, dropped or mixed up with another (or a helper of the prices: argLen,
// recvLen, strBytes, bigWordsOf, sumBytes, indexArg, workFast, workSlots,
// workSearch) changes a row.
//
// The work of a call is measured from Go: the built-in is called by Call on a
// fresh thread, and the row is the difference of Thread.Work() (the call of the
// built-in, its price, and what the built-in charges itself as it goes). A call
// that fails, because the operands do not suit the built-in, has been charged
// all the same, and the row says "error": the price is computed before the
// call, from the sizes of the operands.
//
// The operands are big enough that every formula is at least 2 and its halves
// are visible: strings of 1, 200 and 10000 bytes, containers of 1000 elements,
// an integer of 2**5000, needles of 1, 2, 16, 17, 63 and 64 bytes.
//
// The golden file is written, and only then, by
//
//	STARLARK_WRITE_GOLDEN=$PWD/starlark go test ./starlark -run TestPricesGolden
//
// (the same variable as the writers of the goldens of v0.2.0: the directory of
// the starlark package whose testdata is written).
//
// A new built-in appears in the tables, so it shows up here as a new row.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

const pgGoldenFile = "testdata/prices.golden"

// A pgCase is one set of operands: it builds them afresh each time, because a
// method may change its receiver.
type pgCase struct {
	label string
	make  func() (recv Value, args Tuple)
}

// pgText is a string of n bytes that is not all of one kind.
func pgText(n int) String {
	const unit = "aBc d1"
	return String(strings.Repeat(unit, n/len(unit)+1)[:n])
}

func pgList(n int) *List {
	vs := make([]Value, n)
	for i := range vs {
		vs[i] = MakeInt(i)
	}
	return NewList(vs)
}

func pgTuple(n int) Tuple {
	t := make(Tuple, n)
	for i := range t {
		t[i] = MakeInt(i)
	}
	return t
}

func pgDict(n int) *Dict {
	d := NewDict(n)
	for i := 0; i < n; i++ {
		d.SetKey(MakeInt(i), MakeInt(i))
	}
	return d
}

func pgSet(n int) *Set {
	s := NewSet(n)
	for i := 0; i < n; i++ {
		s.Insert(MakeInt(i))
	}
	return s
}

func pgBigInt() Int {
	x := MakeInt(1)
	for i := 0; i < 5000; i++ {
		x = x.Add(x)
	}
	return x
}

func pgArgs(label string, mk func() Tuple) pgCase {
	return pgCase{label, func() (Value, Tuple) { return nil, mk() }}
}

// pgUniverseCases are the operands of the functions of Universe.
func pgUniverseCases() []pgCase {
	one := func(label string, v func() Value) pgCase {
		return pgArgs(label, func() Tuple { return Tuple{v()} })
	}
	return []pgCase{
		pgArgs("()", func() Tuple { return nil }),
		one("str1", func() Value { return pgText(1) }),
		one("str200", func() Value { return pgText(200) }),
		one("str10000", func() Value { return pgText(10000) }),
		one("bytes200", func() Value { return Bytes(pgText(200)) }),
		one("bytes10000", func() Value { return Bytes(pgText(10000)) }),
		one("int5", func() Value { return MakeInt(5) }),
		one("int2**5000", func() Value { return pgBigInt() }),
		one("list1000", func() Value { return pgList(1000) }),
		one("tuple1000", func() Value { return pgTuple(1000) }),
		one("dict1000", func() Value { return pgDict(1000) }),
		one("set1000", func() Value { return pgSet(1000) }),
		one("range1000", func() Value { return rangeValue{0, 1000, 1, 1000} }),
		pgArgs("list1000,list1000", func() Tuple { return Tuple{pgList(1000), pgList(1000)} }),
		pgArgs("list1000,tuple500,str200", func() Tuple { return Tuple{pgList(1000), pgTuple(500), pgText(200)} }),
		pgArgs("str10000,str200", func() Tuple { return Tuple{pgText(10000), pgText(200)} }),
		pgArgs("str200,bytes10000,int7", func() Tuple { return Tuple{pgText(200), Bytes(pgText(10000)), MakeInt(7)} }),
		pgArgs("list1000,int5", func() Tuple { return Tuple{pgList(1000), MakeInt(5)} }),
	}
}

func pgRecv(label string, recv func() Value, args func() Tuple) pgCase {
	return pgCase{label, func() (Value, Tuple) {
		var a Tuple
		if args != nil {
			a = args()
		}
		return recv(), a
	}}
}

// pgDictCases are the operands of the methods of dict.
func pgDictCases() []pgCase {
	d := func(n int) func() Value { return func() Value { return pgDict(n) } }
	return []pgCase{
		pgRecv("dict1()", d(1), nil),
		pgRecv("dict1000()", d(1000), nil),
		pgRecv("dict1000(5)", d(1000), func() Tuple { return Tuple{MakeInt(5)} }),
		pgRecv("dict1000(5,0)", d(1000), func() Tuple { return Tuple{MakeInt(5), MakeInt(0)} }),
		pgRecv("dict1000(2000,0)", d(1000), func() Tuple { return Tuple{MakeInt(2000), MakeInt(0)} }),
		pgRecv("dict1000(dict1000)", d(1000), func() Tuple { return Tuple{pgDict(1000)} }),
		pgRecv("dict1(list1000)", d(1), func() Tuple { return Tuple{pgList(1000)} }),
	}
}

// pgListCases are the operands of the methods of list.
func pgListCases() []pgCase {
	l := func(n int) func() Value { return func() Value { return pgList(n) } }
	ints := func(xs ...int) func() Tuple {
		return func() Tuple {
			t := make(Tuple, len(xs))
			for i, x := range xs {
				t[i] = MakeInt(x)
			}
			return t
		}
	}
	return []pgCase{
		pgRecv("list1()", l(1), nil),
		pgRecv("list200()", l(200), nil),
		pgRecv("list1000()", l(1000), nil),
		pgRecv("list1000(5)", l(1000), ints(5)),
		pgRecv("list1000(0,7)", l(1000), ints(0, 7)),
		pgRecv("list1000(2,7)", l(1000), ints(2, 7)),
		pgRecv("list1000(100,7)", l(1000), ints(100, 7)),
		pgRecv("list1000(700,7)", l(1000), ints(700, 7)),
		pgRecv("list1000(-100,7)", l(1000), ints(-100, 7)),
		pgRecv("list1000(100)", l(1000), ints(100)),
		pgRecv("list1000(700)", l(1000), ints(700)),
		pgRecv("list1000(-100)", l(1000), ints(-100)),
		pgRecv("list1000(-900)", l(1000), ints(-900)),
		pgRecv("list1000(list1000)", l(1000), func() Tuple { return Tuple{pgList(1000)} }),
		pgRecv("list1(list1000)", l(1), func() Tuple { return Tuple{pgList(1000)} }),
	}
}

// pgSetCases are the operands of the methods of set.
func pgSetCases() []pgCase {
	s := func(n int) func() Value { return func() Value { return pgSet(n) } }
	return []pgCase{
		pgRecv("set1()", s(1), nil),
		pgRecv("set1000()", s(1000), nil),
		pgRecv("set1000(5)", s(1000), func() Tuple { return Tuple{MakeInt(5)} }),
		pgRecv("set1000(2000)", s(1000), func() Tuple { return Tuple{MakeInt(2000)} }),
		pgRecv("set1000(set1000)", s(1000), func() Tuple { return Tuple{pgSet(1000)} }),
		pgRecv("set1000(list1000)", s(1000), func() Tuple { return Tuple{pgList(1000)} }),
		pgRecv("set1(list1000,tuple500)", s(1), func() Tuple { return Tuple{pgList(1000), pgTuple(500)} }),
	}
}

// pgStringCases are the operands of the methods of string.
func pgStringCases() []pgCase {
	str := func(n int) func() Value { return func() Value { return pgText(n) } }
	cs := []pgCase{
		pgRecv("str1()", str(1), nil),
		pgRecv("str200()", str(200), nil),
		pgRecv("str10000()", str(10000), nil),
	}
	// needles of the classes of workSearch, in a long haystack
	for _, k := range []int{1, 2, 16, 17, 63, 64, 200, 10000} {
		k := k
		cs = append(cs, pgRecv(fmt.Sprintf("str10000(needle%d)", k), str(10000),
			func() Tuple { return Tuple{String(strings.Repeat("a", k))} }))
	}
	cs = append(cs,
		pgRecv("str10000(str100,str200)", str(10000), func() Tuple { return Tuple{pgText(100), pgText(200)} }),
		pgRecv("str10000(tuple(str3,str5))", str(10000), func() Tuple { return Tuple{Tuple{pgText(3), pgText(5)}} }),
		pgRecv("str200(list1000)", str(200), func() Tuple { return Tuple{pgList(1000)} }),
		pgRecv("str200(str1,int3)", str(200), func() Tuple { return Tuple{pgText(1), MakeInt(3)} }),
	)
	return cs
}

// pgBytesCases are the operands of the methods of bytes.
func pgBytesCases() []pgCase {
	return []pgCase{
		pgRecv("bytes200()", func() Value { return Bytes(pgText(200)) }, nil),
	}
}

// pgMeasure calls b on the operands and returns the work that it was charged.
func pgMeasure(b *Builtin, c pgCase) string {
	thread := &Thread{Name: "prices-golden", Print: func(*Thread, string) {}}
	recv, args := c.make()
	if recv != nil {
		b = b.BindReceiver(recv)
	}
	w0 := thread.Work()
	_, err := Call(thread, b, args, nil)
	row := fmt.Sprint(thread.Work() - w0)
	if err != nil {
		row += " error"
	}
	return row
}

// pgRows are the rows of the golden file, sorted.
func pgRows(t *testing.T) []string {
	type table struct {
		name   string
		prices map[string]price
		get    func(name string) *Builtin
		cases  []pgCase
	}
	universe := func(name string) *Builtin {
		b, _ := Universe[name].(*Builtin)
		return b
	}
	method := func(m map[string]*Builtin) func(string) *Builtin {
		return func(name string) *Builtin { return m[name] }
	}
	tables := []table{
		{"universe", universePrices, universe, pgUniverseCases()},
		{"dict", dictPrices, method(dictMethods), pgDictCases()},
		{"list", listPrices, method(listMethods), pgListCases()},
		{"set", setPrices, method(setMethods), pgSetCases()},
		{"string", stringPrices, method(stringMethods), pgStringCases()},
		{"bytes", bytesPrices, method(bytesMethods), pgBytesCases()},
	}
	var rows []string
	for _, tb := range tables {
		for name := range tb.prices {
			b := tb.get(name)
			if b == nil {
				rows = append(rows, fmt.Sprintf("%s.%s: no such built-in", tb.name, name))
				continue
			}
			if b.price == nil {
				rows = append(rows, fmt.Sprintf("%s.%s: no price attached", tb.name, name))
				continue
			}
			for _, c := range tb.cases {
				rows = append(rows, fmt.Sprintf("%s.%s %s = %s", tb.name, name, c.label, pgMeasure(b, c)))
			}
		}
	}
	sort.Strings(rows)
	return rows
}

func TestPricesGolden_ExactWork(t *testing.T) {
	rows := pgRows(t)
	got := strings.Join(rows, "\n") + "\n"
	if dir := os.Getenv("STARLARK_WRITE_GOLDEN"); dir != "" {
		path := dir + "/" + pgGoldenFile
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d rows", path, len(rows))
		return
	}
	wantBytes, err := os.ReadFile(pgGoldenFile)
	if err != nil {
		t.Fatalf("%v (STARLARK_WRITE_GOLDEN=<directory of this starlark package> writes the golden file)", err)
	}
	want := strings.Split(strings.TrimSuffix(string(wantBytes), "\n"), "\n")
	if len(want) == 1 && want[0] == "" {
		want = nil
	}
	pgCompare(t, want, rows)
}

// pgCompare fails with the rows that differ (by their key, the text before " = ").
func pgCompare(t *testing.T, want, got []string) {
	key := func(row string) string {
		if i := strings.LastIndex(row, " = "); i >= 0 {
			return row[:i]
		}
		return row
	}
	w := make(map[string]string, len(want))
	for _, r := range want {
		w[key(r)] = r
	}
	g := make(map[string]string, len(got))
	for _, r := range got {
		g[key(r)] = r
	}
	var diffs []string
	for _, r := range want {
		if x, ok := g[key(r)]; !ok {
			diffs = append(diffs, "- "+r+"   (gone)")
		} else if x != r {
			diffs = append(diffs, "- "+r, "+ "+x)
		}
	}
	for _, r := range got {
		if _, ok := w[key(r)]; !ok {
			diffs = append(diffs, "+ "+r+"   (new)")
		}
	}
	if len(diffs) == 0 {
		return
	}
	const shown = 60
	more := ""
	if len(diffs) > shown {
		more = fmt.Sprintf("\n... and %d more lines", len(diffs)-shown)
		diffs = diffs[:shown]
	}
	t.Errorf("the work of the calls differs from %s (STARLARK_WRITE_GOLDEN=<directory of this starlark package> rewrites it, if the change of a price is meant):\n%s%s",
		pgGoldenFile, strings.Join(diffs, "\n"), more)
}
