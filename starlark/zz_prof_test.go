package starlark_test

import (
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func BenchmarkZZ(b *testing.B) {
	b.ReportAllocs()
	pre := starlark.StringDict{"json": json.Module}
	for i := 0; i < b.N; i++ {
		th := &starlark.Thread{}
		_, err := starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t", "l = [[i, [i]] for i in range(100000)]\nr = json.encode(l)\n", pre)
		if err != nil {
			b.Fatal(err)
		}
	}
}
