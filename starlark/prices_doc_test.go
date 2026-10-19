package starlark_test

// Writes doc/prices.md, the document of the prices, from the tables of
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
	// the table is that of the reference platform; the other platforms are in
	// the summary under it (each judged by its own floors and its own C)
	var ref *calibrationPlatform
	for i := range calibrationPlatforms {
		if calibrationPlatforms[i].Platform == referencePlatform {
			ref = &calibrationPlatforms[i]
		}
	}
	if ref == nil {
		t.Fatalf("the reference platform %s is not registered", referencePlatform)
	}
	var rows []row
	var worst []row
	for _, p := range calibrationProbes {
		ns, ok := ref.Ns[p.name]
		if !ok {
			continue
		}
		c := countedProbe(t, p)
		if c.err != nil {
			continue
		}
		work, steps := c.work, c.steps
		r := row{p.name, ns, work, steps, ns / float64(max(work, 1))}
		rows = append(rows, r)
		if !strings.HasPrefix(p.name, "loop:") && ns >= minProbeNanoseconds {
			worst = append(worst, r)
		}
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i].nsPerUnit > worst[j].nsPerUnit })
	fmt.Fprintf(&b, "\n## What a unit costs, by probe (%d probes)\n\nThe time is the least of seven runs, measured by `TestCalibrate` (the date and the load of the machine are in the header of `calibrate_floors_test.go`; doc/budgets.md says what they mean); the work is what the operation is charged now. The ratio is the nanoseconds a unit of work bought in that probe: no probe is over C = %d ns (`TestWork_NoProbeCostsMoreThanC`) and every priced one is held to it (`TestPrices_AtLeastTheMeasuredCost`). The worst of the operations that are priced:\n\n| probe | ns | work | ns / unit |\n|---|---|---|---|\n", len(rows), int(starlark.WorkNanoseconds))
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

	// ---- the platforms: each has its own C, and is judged by it
	b.WriteString("\n## The platforms\n\nThe units of work are the same on every platform; C turns them into time on a given machine, and each platform has its own (a platform is judged by its own floors with its own C, never by a mixture of them). The table above is that of the reference platform, " + referencePlatform + ". Per platform, the worst probe of 2000 ns or more by the work that is charged now:\n\n| platform | machine | date | C (ns / unit) | worst probe | ns / unit |\n|---|---|---|---|---|---|\n")
	plats := append([]calibrationPlatform(nil), calibrationPlatforms...)
	sort.Slice(plats, func(i, j int) bool { return plats[i].Platform < plats[j].Platform })
	for _, pl := range plats {
		wn, wr := "", 0.0
		for _, p := range calibrationProbes {
			ns, ok := pl.Ns[p.name]
			if !ok || ns < minProbeNanoseconds {
				continue
			}
			c := countedProbe(t, p)
			if c.err != nil {
				continue
			}
			if r := ns / float64(max(c.work, 1)); r > wr {
				wn, wr = p.name, r
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.0f | %s | %.1f |\n", pl.Platform, strings.ReplaceAll(pl.Machine, "|", "\\|"), pl.Date, pl.C, strings.ReplaceAll(wn, "|", "\\|"), wr)
	}

	// ---- the prices
	b.WriteString("\n## The prices\n")
	table := ""
	for _, pr := range starlark.PriceRows() {
		if pr.Table != table {
			table = pr.Table
			fmt.Fprintf(&b, "\n### %s\n\n| name | charged | the call | work (units) |\n|---|---|---|---|\n", table)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %s |\n", pr.Name, pr.Kind, pr.Call, strings.ReplaceAll(pr.Desc, "|", "\\|"))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pricesDocIntro is the text of the document before the tables.
const pricesDocIntro = `
The steps of a program (` + "`Thread.Steps`" + `, ` + "`ExecutionSteps()`" + `) are what they were in v0.2.0, byte for byte, whatever the operands: the interpreter counts an opcode, and nothing else is converted to steps. The time of the operations is a second counter, the work (` + "`Thread.Work()`" + `), with its own limit (` + "`SetMaxWork`" + `).

**The unit of work** is about the time of a simple step of the interpreter (the loop probes below take 1.5 to 11.2 ns a unit; C below is the bound that is asserted). Each step is a unit, and the operations charge for what they do beyond the opcodes that start them: the elements they visit, compare, probe, hash or copy, the bytes they scan, and the memory they allocate (garbage is allocator and collector time as well).

**The invariant**: a program that has done W units of work has used at most C * W nanoseconds of CPU, with C = %d ns on the reference machine, darwin/arm64 (C is per platform: see the platforms below;the interpreter's own loops and the priced operations alike: no probe of the calibration cost more).

**The search family** (find, rfind, index, rindex, partition, rpartition) charges each window of the haystack before it scans it (windows of 64 bytes or the needle, doubling up to a chunk of 1024 units), so a call is refused before the window that does not fit, and a match that is found may be charged up to the rest of its last window.
`
