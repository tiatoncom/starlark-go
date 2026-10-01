package starlark

import (
	"math/bits"
)

// The price of every built-in function and method of this package, and of the
// operators, in units of work (work.go).
//
// The unit of work is about the time of one simple step of the interpreter
// (8 ns nominal), and the invariant is one sentence: a program that has done W
// units of work (Thread.Work) has used at most C * W nanoseconds, C being
// workNanoseconds. Each step of the interpreter is a unit; a price is what an
// operation costs beyond the steps that start it, by what it does in the worst
// case: the elements it visits, compares, probes, hashes or copies, the bytes it
// scans, and the memory it allocates, which is garbage as well (allocator and
// collector time: allocBytesPerWork).
//
// A price is one of:
//
//	o1      the work does not depend on the size of the operands, or on
//	        anything but the type: nothing is charged beyond the steps of the
//	        call. (A test checks it: the same call on an operand of size n and
//	        100n costs the same work.)
//	work    a function of the operands, computed BEFORE the call from sizes
//	        that are known without looking at the data (lengths), charged by
//	        Call: a call that does not fit in the work that is left is refused,
//	        not run.
//	inside  the work depends on the data (it stops at the first match, or it
//	        hashes and compares elements): the function charges as it goes, to
//	        a meter.
//
// Every entry of Universe and of the method tables has a price here, and a test
// (TestEveryBuiltinHasAPrice) fails for a built-in that has none: a built-in
// without a price is a built-in whose time nothing bounds.
//
// The prices are the measured costs of the primitives in the worst form that is
// known, divided by the unit; steps-prices.md has the table of the
// measurements (and calibrate_probes_test.go the programs of them), and
// TestPrices_AtLeastTheMeasuredCost holds every priced operation to it.
type price struct {
	desc string // the formula, in words (the table of steps-prices.md)
	// accounts is set for built-ins that charge the allocation budget
	// themselves, or return a value that already exists (the argument, an
	// element, a substring that shares memory): Call does not charge their
	// result (alloc.go). It is set from accountedBuiltins when the price is
	// attached, so a built-in of the host, which has no price, is never exempt.
	accounts bool
	o1       bool
	inside   bool
	work     func(recv Value, args Tuple, kwargs []Tuple) uint64
}

func o1(desc string) price     { return price{desc: desc, o1: true} }
func inside(desc string) price { return price{desc: desc, inside: true} }
func work(desc string, f func(Value, Tuple, []Tuple) uint64) price {
	return price{desc: desc, work: f}
}

// argLen is the length of the i'th argument if it has one.
func argLen(args Tuple, i int) int {
	if i < len(args) {
		return max(Len(args[i]), 0)
	}
	return 0
}

// recvLen is the length of the receiver of a method.
func recvLen(recv Value) int { return max(Len(recv), 0) }

// strBytes is the number of bytes of a string or bytes value.
func strBytes(v Value) int {
	switch v := v.(type) {
	case String:
		return len(v)
	case Bytes:
		return len(v)
	}
	return 0
}

func log2ceil(n int) uint64 {
	if n <= 1 {
		return 1
	}
	return uint64(bits.Len(uint(n - 1)))
}

// universePrices are the prices of the functions of Universe.
var universePrices = map[string]price{
	"abs":  work("words of a big integer", func(_ Value, a Tuple, _ []Tuple) uint64 { return bigWordsOf(a, 0) }),
	"any":  inside("one unit per element visited, until the first true one"),
	"all":  inside("one unit per element visited, until the first false one"),
	"bool": o1("a type conversion"),
	"bytes": work("bytes(str): len/64; bytes(iterable): one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 {
		if len(a) == 1 {
			if s, ok := a[0].(String); ok {
				return workFast(len(s))
			}
			if _, ok := a[0].(Bytes); !ok {
				return uint64(argLen(a, 0))
			}
		}
		return 0
	}),
	"chr":       o1("one code point"),
	"dict":      inside("one insert (8 units + hash + chain) per entry"),
	"dir":       o1("the attributes of a type, not of the data"),
	"enumerate": work("one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 { return uint64(argLen(a, 0)) }),
	"fail":      work("len of the message/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(sumBytes(a)) }),
	"float":     work("float(str): len/4", func(_ Value, a Tuple, _ []Tuple) uint64 { return workSlow(strBytesAt(a, 0)) }),
	"getattr":   o1("a lookup of an attribute by name"),
	"hasattr":   o1("a lookup of an attribute by name"),
	"hash":      work("len/4 (FNV, byte by byte)", func(_ Value, a Tuple, _ []Tuple) uint64 { return workSlow(strBytesAt(a, 0)) }),
	"int":       inside("int(str): digits^2/4096 (and at most maxIntDigits digits); other: o1"),
	"len":       o1("the length is stored"),
	"list":      work("one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 { return uint64(argLen(a, 0)) }),
	"max":       inside("one comparison (a unit and the leaves) per element"),
	"min":       inside("one comparison (a unit and the leaves) per element"),
	"ord":       o1("one code point"),
	"print":     work("len of the output/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(sumBytes(a)) }),
	"range":     o1("a range is lazy"),
	"repr":      inside("len of the result/4"),
	"reversed":  work("one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 { return uint64(argLen(a, 0)) }),
	"set":       inside("one insert (8 units + hash + chain) per element"),
	"sorted": work("n * ceil(log2 n) comparisons", func(_ Value, a Tuple, _ []Tuple) uint64 {
		n := argLen(a, 0)
		return uint64(n) * log2ceil(n)
	}),
	"str":   inside("len of the result/4"),
	"tuple": work("one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 { return uint64(argLen(a, 0)) }),
	"type":  o1("the name of a type"),
	"zip": work("one unit per element of each column", func(_ Value, a Tuple, _ []Tuple) uint64 {
		rows := 0
		for i := range a {
			if n := argLen(a, i); i == 0 || n < rows {
				rows = n
			}
		}
		return satMul(uint64(rows), uint64(len(a)))
	}),
}

func bigWordsOf(a Tuple, i int) uint64 {
	if i < len(a) {
		if x, ok := a[i].(Int); ok {
			return bigWords(x)
		}
	}
	return 0
}

func sumBytes(a Tuple) int {
	n := 0
	for _, v := range a {
		n += strBytes(v)
	}
	return n
}

func strBytesAt(a Tuple, i int) int {
	if i < len(a) {
		return strBytes(a[i])
	}
	return 0
}

// dictPrices are the prices of the methods of dict.
var dictPrices = map[string]price{
	"clear":      o1("the table is replaced by that of a new dict (not zeroed)"),
	"get":        inside("hash of the key, chain of the bucket"),
	"items":      work("3 units per entry", func(r Value, _ Tuple, _ []Tuple) uint64 { return 3 * uint64(recvLen(r)) }),
	"keys":       work("3 units per entry", func(r Value, _ Tuple, _ []Tuple) uint64 { return 3 * uint64(recvLen(r)) }),
	"pop":        inside("hash of the key, chain of the bucket"),
	"popitem":    inside("hash of the key, chain of the bucket"),
	"setdefault": inside("hash of the key, chain of the bucket, an insert"),
	"update":     inside("one insert (8 units + hash + chain) per entry"),
	"values":     work("3 units per entry", func(r Value, _ Tuple, _ []Tuple) uint64 { return 3 * uint64(recvLen(r)) }),
}

// listPrices are the prices of the methods of list.
var listPrices = map[string]price{
	"append": o1("amortized: the growth of the array is charged as memory"),
	"clear":  work("one unit per 4 slots (zeroed)", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlots(recvLen(r)) }),
	"extend": work("one unit per element", func(_ Value, a Tuple, _ []Tuple) uint64 { return uint64(argLen(a, 0)) }),
	"index":  inside("one comparison per element, until the first match"),
	"insert": work("slots after the index/4 (memmove)", func(r Value, a Tuple, _ []Tuple) uint64 { return workSlots(recvLen(r) - indexArg(a, 0)) }),
	"pop": work("slots after the index/4 (memmove)", func(r Value, a Tuple, _ []Tuple) uint64 {
		n := recvLen(r)
		if len(a) == 0 {
			return 0
		}
		i := indexArg(a, 0)
		if i < 0 {
			i += n
		}
		return workSlots(n - 1 - max(i, 0))
	}),
	"remove": inside("one comparison per element until the first match, then slots after it/4"),
}

// indexArg is the i'th argument as an int, 0 if it is not one.
func indexArg(a Tuple, i int) int {
	if i < len(a) {
		if n, err := AsInt32(a[i]); err == nil {
			return n
		}
	}
	return 0
}

// setPrices are the prices of the methods of set.
var setPrices = map[string]price{
	"add":                  inside("hash of the element, chain of the bucket, an insert"),
	"clear":                o1("the table is replaced by that of a new set (not zeroed)"),
	"difference":           inside("a copy of the set and a delete per element of the argument"),
	"discard":              inside("hash of the element, chain of the bucket"),
	"intersection":         inside("a lookup per element of the argument"),
	"issubset":             inside("a lookup per element of the argument, and the buckets"),
	"issuperset":           inside("a lookup per element of the argument"),
	"pop":                  inside("the first element and its delete"),
	"remove":               inside("hash of the element, chain of the bucket"),
	"symmetric_difference": inside("a copy of the set and a delete or insert per element of the argument"),
	"union":                inside("a copy of the set and an insert per element of the arguments"),
	"update":               inside("an insert per element of the arguments"),
}

// stringPrices are the prices of the methods of string.
var stringPrices = map[string]price{
	"capitalize":     work("2 x len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return 2 * workSlow(strBytes(r)) }),
	"codepoint_ords": o1("lazy: the iterator is the work"),
	"codepoints":     o1("lazy: the iterator is the work"),
	"count": work("the search of the needle in len bytes (workSearch), and half a unit for each of the len/needle matches", func(r Value, a Tuple, _ []Tuple) uint64 {
		m := max(strBytesAt(a, 0), 1)
		return workSearch(strBytes(r), m) + uint64(strBytes(r)/m)/2
	}),
	"elem_ords":    o1("lazy: the iterator is the work"),
	"elems":        o1("lazy: the iterator is the work"),
	"endswith":     work("bytes of the suffix(es)/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(sumBytes(a)) + uint64(argLen(a, 0)) }),
	"find":         inside("position of the match/64"),
	"format":       inside("len of the result/4"),
	"index":        inside("position of the match/64"),
	"isalnum":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"isalpha":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"isdigit":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"islower":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"isspace":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"istitle":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"isupper":      work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"join":         inside("total bytes/64 and one unit per element"),
	"lower":        work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
	"lstrip":       inside("bytes trimmed/4"),
	"partition":    inside("position of the match/64"),
	"removeprefix": work("bytes of the prefix/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(strBytesAt(a, 0)) }),
	"removesuffix": work("bytes of the suffix/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(strBytesAt(a, 0)) }),
	"replace":      inside("len/64, and 2 units per match"),
	"rfind":        inside("distance of the match from the end/64"),
	"rindex":       inside("distance of the match from the end/64"),
	"rpartition":   inside("distance of the match from the end/64"),
	"rsplit":       inside("len/64, and 4 units per field"),
	"rstrip":       inside("bytes trimmed/4"),
	"split":        inside("len/64, and 4 units per field"),
	"splitlines":   inside("len/64, and 4 units per line"),
	"startswith":   work("bytes of the prefix(es)/64", func(_ Value, a Tuple, _ []Tuple) uint64 { return workFast(sumBytes(a)) + uint64(argLen(a, 0)) }),
	"strip":        inside("bytes trimmed/4"),
	"title":        work("2 x len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return 2 * workSlow(strBytes(r)) }),
	"upper":        work("len/4", func(r Value, _ Tuple, _ []Tuple) uint64 { return workSlow(strBytes(r)) }),
}

// bytesPrices are the prices of the methods of bytes.
var bytesPrices = map[string]price{
	"elems": o1("lazy: the iterator is the work"),
}

// operatorPrices are the prices of the operators and the other operations of
// the interpreter that are not calls of a built-in. The operations that are
// listed here are charged where they are implemented (eval.go, interp.go,
// value.go); the list is the closed list that the review of the code and
// TestOperatorPricesAreDeclared check against the operations.
var operatorPrices = map[string]price{
	"load/store/constant/jump/call (opcodes)":                    o1("one step each"),
	"x + y (int, float)":                                         o1("big integers: words of the longer"),
	"x + y (string, bytes)":                                      inside("memory: 1 unit per 32 bytes (allocation, above 1 KiB)"),
	"x + y (list, tuple)":                                        inside("memory: 1 unit per 32 bytes (allocation, above 1 KiB)"),
	"x * n (string, list, tuple)":                                inside("memory: 1 unit per 32 bytes of the result (above 1 KiB)"),
	"x * y, x // y, x % y (big int)":                             inside("words(x) * words(y) / 8"),
	"x - y, x & y, x | y, x ^ y, x << y, x >> y (int)":           inside("words of the longer"),
	"x % y (string interpolation)":                               inside("memory of the result; the limit of the thread bounds the form"),
	"x in l (list, tuple)":                                       inside("one unit per element visited, and the leaves compared"),
	"x in s (string, bytes)":                                     inside("len(s)/64"),
	"x in d (dict), x in s (set)":                                inside("hash of the key, chain of the bucket"),
	"x in r (range)":                                             o1("arithmetic"),
	"x == y, x != y, x < y, x <= y, x > y, x >= y (string)":      inside("min(len)/64"),
	"x == y, x != y, x < y, x <= y, x > y, x >= y (list, tuple)": inside("one unit per pair visited, and the leaves compared"),
	"x == y, x != y (dict)":                                      inside("one lookup per entry, and the values compared"),
	"x == y, x != y, x <= y, x < y, x >= y, x > y (set)":         inside("one lookup per element"),
	"x == y, x != y, x < y, x > y (int, float, bool, None)":      o1("big integers: words"),
	"x[i] (list, tuple, string, range)":                          o1("an index"),
	"x[k] (dict)":                                                inside("hash of the key, chain of the bucket"),
	"x[i:j:k] (list, tuple, string, bytes)":                      inside("memory of the result: 1 unit per 32 bytes (above 1 KiB)"),
	"x[i] = v (list)":                                            o1("an index"),
	"x[k] = v (dict)":                                            inside("hash of the key, chain of the bucket, an insert"),
	"d1 | d2, d1 |= d2":                                          inside("an insert per entry"),
	"s1 | s2, s1 & s2, s1 - s2, s1 ^ s2":                         inside("a copy and one insert or lookup per element"),
	"-x, +x, ~x, not x":                                          o1("big integers: words"),
	"for x in l (iteration)":                                     o1("one step per element"),
	"a, b = l (unpacking)":                                       o1("the count of targets is in the code"),
	"f(*l), f(**d)":                                              inside("memory of the copy: 1 unit per 32 bytes (above 1 KiB)"),
	"x.attr, x.method":                                           o1("a lookup of an attribute by name"),
	"list comprehension, dict comprehension":                     o1("one step per element"),
	"{k: v}, [a, b], (a, b), lambda":                             o1("the number of elements is in the code"),
	"def f(*args, **kwargs) (parameters)":                        inside("memory of the copy: 1 unit per 32 bytes (above 1 KiB)"),
	"hash of a string":                                           inside("len/64"),
	"hash of a tuple":                                            inside("one unit per element visited"),
	"hash of an int":                                             o1("big integers: words"),
	"freeze":                                                     inside("one visit per container, a tuple once"),
	"json.encode, json.decode, json.indent":                      inside("memory of the result: 1 unit per 32 bytes, and one unit per node"),
}

// attachPrices sets the price of every built-in of the tables. A built-in that
// has no entry keeps a nil price (and TestEveryBuiltinHasAPrice fails).
func attachPrices() {
	attach := func(b *Builtin, tableName string, table map[string]price, name string) {
		if p, ok := table[name]; ok {
			p.accounts = accountedBuiltins[tableName+"."+name]
			b.price = &p
		}
	}
	for name, v := range Universe {
		if b, ok := v.(*Builtin); ok {
			attach(b, "universe", universePrices, name)
		}
	}
	for name, b := range dictMethods {
		attach(b, "dict", dictPrices, name)
	}
	for name, b := range listMethods {
		attach(b, "list", listPrices, name)
	}
	for name, b := range setMethods {
		attach(b, "set", setPrices, name)
	}
	for name, b := range stringMethods {
		attach(b, "string", stringPrices, name)
	}
	for name, b := range bytesMethods {
		attach(b, "bytes", bytesPrices, name)
	}
}

// accountedBuiltins is the closed set of built-ins whose result Call does not
// charge to the allocation budget: they return a value that exists already
// (dict.get, min, a substring that shares memory) or charge what they allocate
// themselves (str, bytes, replace, lower, upper). A new built-in is charged by
// Call unless it is listed here (TestAccountedBuiltins_AreTheDeclaredSet).
var accountedBuiltins = map[string]bool{
	"universe.bytes": true, "universe.getattr": true, "universe.max": true, "universe.min": true,
	"universe.str": true, "universe.type": true,
	"dict.get": true, "dict.pop": true, "dict.setdefault": true,
	"list.pop": true, "set.pop": true,
	"string.strip": true, "string.lstrip": true, "string.rstrip": true,
	"string.removeprefix": true, "string.removesuffix": true,
	"string.lower": true, "string.upper": true, "string.replace": true,
}
