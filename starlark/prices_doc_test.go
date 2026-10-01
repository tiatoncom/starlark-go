package starlark_test

// Writes steps-prices.md, the document of the prices, from the tables of
// prices.go (the source of truth), the probes of the calibration with the times
// that were measured (calibrate_floors_test.go), and the work and steps of the
// handlers as they are charged now:
//
//	STARLARK_WRITE_PRICES=<file> go test -run TestWritePricesDoc ./starlark/

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func TestWritePricesDoc(t *testing.T) {
	path := os.Getenv("STARLARK_WRITE_PRICES")
	if path == "" {
		t.Skip("set STARLARK_WRITE_PRICES to write the document")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Work and steps of the starlark fork (v0.3.0)\n\n")
	fmt.Fprintf(&b, pricesDocIntro, int(starlark.WorkNanoseconds))

	// ---- the probes: what the work is for the operations that were measured
	type row struct {
		name        string
		ns          float64
		work, steps uint64
		nsPerUnit   float64
	}
	var rows []row
	var worst []row
	for _, p := range calibrationProbes {
		ns, ok := calibratedNs[p.name]
		if !ok {
			continue
		}
		work, steps, err := probeWork(t, p)
		if err != nil {
			continue
		}
		r := row{p.name, ns, work, steps, ns / float64(max(work, 1))}
		rows = append(rows, r)
		if !strings.HasPrefix(p.name, "loop:") {
			worst = append(worst, r)
		}
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i].nsPerUnit > worst[j].nsPerUnit })
	fmt.Fprintf(&b, "\n## What a unit costs, by probe (%d probes)\n\nThe time is the least of seven runs, measured by `TestCalibrate` on the date and under the load above; the work is what the operation is charged now. The ratio is the nanoseconds a unit of work bought in that probe: the prices are held to %d ns (`TestPrices_AtLeastTheMeasuredCost`). The worst of the operations that are priced:\n\n| probe | ns | work | ns / unit |\n|---|---|---|---|\n", len(rows), int(priceNanoseconds))
	for i, r := range worst {
		if i >= 15 {
			break
		}
		fmt.Fprintf(&b, "| %s | %.0f | %d | %.1f |\n", r.name, r.ns, r.work, r.nsPerUnit)
	}
	b.WriteString("\nAll of them:\n\n| probe | ns | work | steps | ns / unit |\n|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %.0f | %d | %d | %.2f |\n", strings.ReplaceAll(r.name, "|", "\\|"), r.ns, r.work, r.steps, r.nsPerUnit)
	}

	// ---- the prices
	b.WriteString("\n## The prices\n")
	table := ""
	for _, pr := range starlark.PriceRows() {
		if pr.Table != table {
			table = pr.Table
			fmt.Fprintf(&b, "\n### %s\n\n| name | charged | work (units) |\n|---|---|---|\n", table)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", pr.Name, pr.Kind, strings.ReplaceAll(pr.Desc, "|", "\\|"))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pricesDocIntro is the text of the document before the tables.
const pricesDocIntro = `
The steps of a program (` + "`Thread.Steps`" + `, ` + "`ExecutionSteps()`" + `) are what they were in v0.2.0, byte for byte, whatever the operands: the interpreter counts an opcode, and nothing else is converted to steps. The time of the operations is a second counter, the work (` + "`Thread.Work()`" + `), with its own limit (` + "`SetMaxWork`" + `).

**The unit of work** is about the time of a simple step of the interpreter (8 ns nominal). Each step is a unit, and the operations charge for what they do beyond the opcodes that start them: the elements they visit, compare, probe, hash or copy, the bytes they scan, and the memory they allocate (garbage is allocator and collector time as well).

**The invariant**: a program that has done W units of work has used at most C * W nanoseconds of CPU, with C = %d ns on the reference machine (the interpreter's own loops cost up to that much a unit; the priced operations at most ` + "`priceNanoseconds`" + ` of it).
`
