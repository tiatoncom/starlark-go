package starlark

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestWritePricesDoc writes the table of prices as markdown, from the tables of
// prices.go, which are the source of truth: STARLARK_WRITE_PRICES=<file>.
func TestWritePricesDoc(t *testing.T) {
	path := os.Getenv("STARLARK_WRITE_PRICES")
	if path == "" {
		t.Skip("set STARLARK_WRITE_PRICES to write the document")
	}
	var b strings.Builder
	kind := func(p price) string {
		switch {
		case p.o1:
			return "O(1)"
		case p.work != nil:
			return "before the call"
		default:
			return "as it goes"
		}
	}
	section := func(title string, table map[string]price) {
		fmt.Fprintf(&b, "\n### %s\n\n| name | charged | work (units) |\n|---|---|---|\n", title)
		var names []string
		for n := range table {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := table[n]
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", n, kind(p), strings.ReplaceAll(p.desc, "|", "\\|"))
		}
	}
	fmt.Fprintf(&b, "# Steps prices of the starlark fork (v0.3.0)\n\nGenerated from `starlark/prices.go` (the table in the code is the source of truth; a test fails for a built-in or operator without an entry).\n\n")
	fmt.Fprintf(&b, "WorkPerStep = %d units of work = 1 step. FreeWork = %d units: an operation that does less work is not charged (its steps are those of v0.2.0). An operation that does more is charged all of its work / WorkPerStep steps.\n\n", WorkPerStep, FreeWork)
	fmt.Fprintf(&b, "Charged *before the call*: from the sizes of the operands, so a call that does not fit in the steps left is refused, not run. *As it goes*: to a meter, in chunks of %d units, because the work depends on the data (it stops at the first match, or it hashes and compares); the operation stops at the chunk where the limit is reached.\n", flushWork)
	b.WriteString(pricesDocIntro)
	section("Universe (functions)", universePrices)
	section("dict methods", dictPrices)
	section("list methods", listPrices)
	section("set methods", setPrices)
	section("string methods", stringPrices)
	section("bytes methods", bytesPrices)
	section("operators and operations of the interpreter", operatorPrices)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

const pricesDocIntro = `
## The coefficient K = WorkPerStep = 16

One unit of work is the time of one element comparison, ~8 ns. A step is an opcode: 2.5-9 ns in plain loops (measured below). Equal time per step would
be K = 1; K = 16 is chosen so that (a) every operation whose operands have fewer than 16 elements, whatever the weight of an element (at most 4 units), stays
under FreeWork = 64 units and is not charged: the steps of such programs are exactly those of v0.2.0 (5574 programs compared); (b) the most time one step of the
limit can buy is K * 8 ns = 128 ns: the engine's default of 10M steps is at most ~1.3 s of work, against hours before.

Measured on darwin/arm64 (Apple M-series), Go 1.26, GOMAXPROCS=1, v0.2.0 tree, by differencing a loop of the operation with an empty loop:

| primitive | ns | per | units charged |
|---|---|---|---|
| opcode, empty loop (for i in range(N): pass) | 2.5 | step | - |
| opcode, small-int arithmetic loop | 4.4 | step | - |
| opcode, index/store/call loop | 9.1 | step | - |
| opcode, dict loop | 4.1 | step | - |
| x in list[int] (miss) | 9.7 | element | 1 (chunked, + the leaves) |
| x in tuple[int] (miss) | 9.7 | element | 1 |
| x in list[str] (miss) | 2.5 | element | 1 |
| == of two int lists | 8.0 | element | 1 |
| max(l) of ints | 9.2 | element | 1 |
| any(l) | 3.9 | element | 1 |
| sorted(l), reversed input | 7.8 | n log2 n comparison | 1 |
| l.insert(0, x), l.pop(0) | 0.4-0.5 | 8 bytes moved | 1/4 per 16-byte slot |
| s.find, s.count, 'b' in s | 0.016-0.021 | byte | 1/64 |
| s + s, s * n, '%s' % s | 0.026-0.031 | byte | 1/64 (+ allocation) |
| s == t, equal 16 MB | 0.030 | byte | 1/64 |
| d[s] = 1, d[t] (string hash) | 0.07-0.10 | byte | 1/64 |
| s.upper() | 2.3 | byte | 1/4 |
| s.title() | 3.5 | byte | 1/2 |
| s.isalpha(), s[::-1] | 0.75-0.77 | byte | 1/4 |
| s.replace('a', 'b') per match | 9.0 | match | 2 (+ len/64) |
| s.split(sep) | 28-38 | field | 4 (+ len/64) |
| list(s.elems()) | 26 | element | 4 |
| d.keys() | 34 | entry | 3 |
| d == dict(d) | 120 | entry | lookup (1 + chain) + compare |
| set(l), dict(zip(l, l)), 1M ints, cold | 340-500 | insert | 1 + log2(n)/2, + chain buckets, + 1/32 per byte of memory |
| list(l), l + l (allocation of 16 MB) | 6-11 | element | 1/32 per byte allocated above 1 KiB |
| int('9' * 20000) | 337 us | digits^2 = 4e8 | digits^2 / 4096 (limit: 4300 digits) |
| str(int of 66000 bits) | 224 us | digits^2 | digits^2 / 4096 |

## Points limits (not time)

- MaxIntDigits = 4300 decimal digits (14285 bits): int(s), str(n), repr(n), %d, {}, json. Legitimate integers have a few dozen digits; Python's default is the same.
- MaxValueDepth = 10000 levels of nesting: string form, hash of a tuple, json.encode/decode; Freeze is iterative.
`
