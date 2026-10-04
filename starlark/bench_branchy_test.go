package starlark_test

import (
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// branchySrc is a while loop with if/elif, and/or, comparisons, `in` and
// calls of Starlark functions: the shape a trace hook sees most.
const branchySrc = `
def classify(n, scale=2):
    if n % 15 == 0:
        return 3
    elif n % 5 == 0 and scale > 1:
        return 2
    elif n % 3 == 0 or (n > 40 and n not in [41, 43]):
        return 1
    return 0

def work(limit):
    i = 0
    total = 0
    while i < limit:
        total += classify(i, scale=i % 4)
        i += 1
    return total
`

// benchBranchy times work(100) with the given thread setup.
func benchBranchy(b *testing.B, set func(*starlark.Thread)) {
	thread := new(starlark.Thread)
	set(thread)
	opts := &syntax.FileOptions{While: true}
	globals, err := starlark.ExecFileOptions(opts, thread, "branchy.star", branchySrc, nil)
	if err != nil {
		b.Fatal(err)
	}
	work := globals["work"]
	args := starlark.Tuple{starlark.MakeInt(100)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := starlark.Call(thread, work, args, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBranchy runs with no tracer at all (Thread.Trace == nil).
func BenchmarkBranchy(b *testing.B) { benchBranchy(b, func(*starlark.Thread) {}) }
