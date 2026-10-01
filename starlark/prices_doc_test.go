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
