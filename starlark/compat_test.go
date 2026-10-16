package starlark_test

// The Compatibility list: every observable difference of this version from
// v0.2.0 that is KEPT, one case each, as a fact that is tested and not as a
// sentence. Each case is a program, what v0.2.0 gives for it, what this version
// gives, and the bound that makes the difference (a deliberate limit of this
// version: nothing else is kept; a difference that is not a bound is returned
// to v0.2.0 and held by the differential tests, TestDifferential*).
//
// What v0.2.0 gives is checked against testdata/diff_compat_v020.golden, which
// was produced by running TestWriteCompatGolden in a tree of v0.2.0 (the commit
// d34a821); what this version gives, against a run of the program. A case that
// stops being a difference, or whose text moves, is red.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const goldenCompat = "testdata/diff_compat_v020.golden"

// A compatOutcome is the status and the text of a run: the error, or the type
// and the value of `result`.
type compatOutcome struct{ Status, Text string }

func (o compatOutcome) String() string { return fmt.Sprintf("%s %.300q", o.Status, o.Text) }

func outcomeOf(d diffOutcome) compatOutcome {
	if d.Status == "ok" {
		return compatOutcome{"ok", d.Result}
	}
	return compatOutcome{d.Status, d.Err}
}

type compatCase struct {
	ID    string // the line of the Compatibility list
	Src   string
	V020  compatOutcome
	Now   compatOutcome
	Bound string // the bound of this version that makes the difference
	// Steps says that the steps of the run are not those of v0.2.0 (they are,
	// by default: the steps are the interpreter's alone).
	StepsDiffer bool
}

func compatCases() []compatCase {
	var cs []compatCase
	cs = append(cs, errTextCompatCases()...)
	return cs
}

const (
	boundErrText = "the text of an error names an operand (a key, a name, a literal, an integer) whole up to 256 bytes of its form (value.go: errValueLimit), as in v0.2.0, and cut there above it: the message must not be as large as a value that can be megabytes"
	boundDigits  = "MaxIntDigits (4300): an integer of more decimal digits (or the bits of them in another base) is not converted from a string"
)

// errTextCompatCases are the cut error texts: one case for each kind of message
// that names an operand, with an operand of 1000 bytes (a 5000-digit one for the
// conversion bound).
func errTextCompatCases() []compatCase {
	var cs []compatCase
	for _, k := range errTextKinds() {
		if k.cutFrom == 0 {
			continue
		}
		n := 1000
		bound := boundErrText
		if k.now != nil {
			n = 5000
			bound = boundDigits
		}
		c := compatCase{ID: "error text: " + k.name, Src: k.mk(n), Bound: bound}
		if k.now != nil {
			w0, w1 := k.v020(n), k.now(n)
			c.V020, c.Now = compatOutcome{w0.status, w0.text}, compatOutcome{w1.status, w1.text}
			c.StepsDiffer = true // (the program stops at the refusal)
		} else {
			c.V020 = compatOutcome{"error", k.wantV020(n)}
			c.Now = compatOutcome{"error", k.wantNow(n)}
		}
		cs = append(cs, c)
	}
	return cs
}

// TestWriteCompatGolden writes the golden file. It is run in a tree of v0.2.0,
// not here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark package>.
func TestWriteCompatGolden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden file (in a tree of v0.2.0)")
	}
	var progs []string
	for _, c := range compatCases() {
		progs = append(progs, c.Src)
	}
	writeGolden(t, dir+"/"+goldenCompat, uniq(progs))
}

// TestCompatibilityDivergencesFromV020: each kept difference from v0.2.0 is
// what its case says, on both sides, exactly.
func TestCompatibilityDivergencesFromV020(t *testing.T) {
	golden := map[string]diffOutcome{}
	for _, g := range readGolden(t, goldenCompat) {
		golden[g.Src] = g
	}
	ids := map[string]bool{}
	for _, c := range compatCases() {
		if ids[c.ID] {
			t.Errorf("%s: two cases of one id", c.ID)
		}
		ids[c.ID] = true
		if c.Bound == "" {
			t.Errorf("%s: no bound named", c.ID)
		}
		g, ok := golden[c.Src]
		if !ok {
			t.Errorf("%s: the golden has no such program", c.ID)
			continue
		}
		got := runDiffProgram(c.Src)
		if o := outcomeOf(g); o != c.V020 {
			t.Errorf("%s: v0.2.0 gave\n   %v\n   the case says\n   %v", c.ID, o, c.V020)
		}
		if o := outcomeOf(got); o != c.Now {
			t.Errorf("%s: now\n   %v\n   the case says\n   %v", c.ID, o, c.Now)
		}
		if c.V020 == c.Now {
			t.Errorf("%s: not a difference (both sides: %v)", c.ID, c.Now)
		}
		if same := got.Steps == g.Steps; same == c.StepsDiffer {
			t.Errorf("%s: steps now %d, v0.2.0 %d (steps differ: %v)", c.ID, got.Steps, g.Steps, c.StepsDiffer)
		}
		if got.Prints != g.Prints {
			t.Errorf("%s: prints differ", c.ID)
		}
	}
	if len(ids) < 20 {
		t.Errorf("only %d cases", len(ids))
	}
	var b strings.Builder
	for _, c := range compatCases() {
		fmt.Fprintf(&b, "\n  %-60s %s", c.ID, c.Bound[:min(len(c.Bound), 60)])
	}
	t.Logf("the Compatibility list (%d):%s", len(ids), b.String())
}
