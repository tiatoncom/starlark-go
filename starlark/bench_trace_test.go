package starlark_test

import (
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// BenchmarkBranchyEmpty runs with a Tracer none of whose hooks is set.
func BenchmarkBranchyEmpty(b *testing.B) {
	benchBranchy(b, func(th *starlark.Thread) { th.Trace = new(starlark.Tracer) })
}

// BenchmarkBranchyAll runs with all four hooks set (they only count).
// It reports how many events of each kind one iteration makes, to turn the
// difference to BenchmarkBranchy into a cost per hook.
func BenchmarkBranchyAll(b *testing.B) {
	var branches, compares, enters, exits int
	benchBranchy(b, func(th *starlark.Thread) {
		th.Trace = &starlark.Tracer{
			OnBranch: func(*starlark.Thread, *starlark.Function, starlark.Branch, starlark.Value, bool) { branches++ },
			OnCompare: func(*starlark.Thread, *starlark.Function, starlark.Branch, syntax.Token, starlark.Value, starlark.Value, bool) {
				compares++
			},
			OnEnter: func(*starlark.Thread, *starlark.Function, starlark.CallSite, []starlark.Arg) { enters++ },
			OnExit:  func(*starlark.Thread, *starlark.Function, starlark.Value, error) { exits++ },
		}
	})
	n := float64(b.N)
	b.ReportMetric(float64(branches)/n, "branches/op")
	b.ReportMetric(float64(compares)/n, "compares/op")
	b.ReportMetric(float64(enters)/n, "enters/op")
	b.ReportMetric(float64(exits)/n, "exits/op")
}
