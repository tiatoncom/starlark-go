package starlark

// What the external tests (package starlark_test) need of the tables of prices,
// to write the document of them (steps-prices.md).

// PriceRow is one line of a table of prices.
type PriceRow struct {
	Table, Name, Kind, Desc string
	Call                    uint64 // the work of the call itself
}

// PriceRows are the prices of the built-ins and of the operators, by table.
func PriceRows() []PriceRow {
	kind := func(p price) string {
		switch {
		case p.o1:
			return "constant"
		case p.work != nil:
			return "before the call"
		default:
			return "as it goes"
		}
	}
	var rows []PriceRow
	for _, t := range []struct {
		name  string
		table map[string]price
	}{
		{"Universe", universePrices}, {"dict", dictPrices}, {"list", listPrices}, {"set", setPrices},
		{"string", stringPrices}, {"bytes", bytesPrices}, {"operators", operatorPrices},
	} {
		var names []string
		for n := range t.table {
			names = append(names, n)
		}
		sortStrings(names)
		for _, n := range names {
			rows = append(rows, PriceRow{t.name, n, kind(t.table[n]), t.table[n].Desc(), t.table[n].call})
		}
	}
	return rows
}

// Desc is the formula of the price, in words.
func (p price) Desc() string { return p.desc }

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// WorkNanoseconds is the C of the invariant.
const WorkNanoseconds = workNanoseconds

// FlushWork is the chunk of work that a meter charges at a time.
const FlushWork = flushWork
