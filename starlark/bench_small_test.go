package starlark_test

import (
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Benchmarks of the hot path: calls of built-ins and operators on small
// operands, where nothing is charged beyond the opcodes (the free window). They
// run unchanged on v0.2.0, to show what the accounting of work costs there.
func benchProgram(b *testing.B, setup, body string) {
	src := setup + "\ndef f():\n  for i in range(1000):\n" + body + "\n"
	th := &starlark.Thread{}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true}, th, "b.star", src, nil)
	if err != nil {
		b.Fatal(err)
	}
	f := globals["f"]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := starlark.Call(th, f, nil, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSmall_len(b *testing.B) {
	benchProgram(b, "l = [1, 2, 3, 4, 5]", "    x = len(l)")
}
func BenchmarkSmall_in_list(b *testing.B) {
	benchProgram(b, "l = [1, 2, 3, 4, 5]", "    x = 9 in l")
}
func BenchmarkSmall_dict_get(b *testing.B) {
	benchProgram(b, "d = {'a': 1, 'b': 2}", "    x = d.get('a')")
}
func BenchmarkSmall_dict_index(b *testing.B) {
	benchProgram(b, "d = {'a': 1, 'b': 2}", "    x = d['b']")
}
func BenchmarkSmall_dict_set(b *testing.B) {
	benchProgram(b, "d = {'a': 1, 'b': 2}", "    d['c'] = i")
}
func BenchmarkSmall_str_find(b *testing.B) {
	benchProgram(b, "s = 'hello world'", "    x = s.find('wor')")
}
func BenchmarkSmall_str_upper(b *testing.B) {
	benchProgram(b, "s = 'hello world'", "    x = s.upper()")
}
func BenchmarkSmall_eq_lists(b *testing.B) {
	benchProgram(b, "l = [1, 2, 3]\nm = [1, 2, 3]", "    x = l == m")
}
func BenchmarkSmall_append(b *testing.B) {
	benchProgram(b, "l = []", "    l.append(i)\n    l.pop()")
}
func BenchmarkSmall_sorted(b *testing.B) {
	benchProgram(b, "l = [3, 1, 2, 5, 4]", "    x = sorted(l)")
}
func BenchmarkSmall_set_add(b *testing.B) {
	benchProgram(b, "s = set([1, 2])", "    s.add(3)")
}

func BenchmarkSmall_percent_d(b *testing.B) {
	benchProgram(b, "", "    x = '%d' % i")
}
func BenchmarkSmall_percent_s(b *testing.B) {
	benchProgram(b, "s = 'abc'", "    x = 'n=%s' % s")
}
func BenchmarkSmall_str_int(b *testing.B) {
	benchProgram(b, "", "    x = str(i)")
}
func BenchmarkSmall_repr_str(b *testing.B) {
	benchProgram(b, "s = 'hello world'", "    x = repr(s)")
}
func BenchmarkSmall_format(b *testing.B) {
	benchProgram(b, "", "    x = 'n={}'.format(i)")
}
func BenchmarkSmall_str_list(b *testing.B) {
	benchProgram(b, "l = [1, 2, 3, 4, 5]", "    x = str(l)")
}
func BenchmarkSmall_join(b *testing.B) {
	benchProgram(b, "l = ['a', 'b', 'c']", "    x = ','.join(l)")
}
func BenchmarkSmall_split(b *testing.B) {
	benchProgram(b, "s = 'a,b,c,d'", "    x = s.split(',')")
}
func BenchmarkSmall_call(b *testing.B) {
	benchProgram(b, "def g(a, b=2): return a", "    x = g(i)")
}
func BenchmarkSmall_call_kw(b *testing.B) {
	benchProgram(b, "def g(a, b=2): return a", "    x = g(i, b=3)")
}

func BenchmarkSmall_int_arith(b *testing.B) {
	benchProgram(b, "", "    x = (i * 31 + i) % 1000003")
}
func BenchmarkSmall_int_add(b *testing.B) {
	benchProgram(b, "", "    x = i + 1")
}
func BenchmarkSmall_int_big32(b *testing.B) {
	benchProgram(b, "", "    x = (1 << 40) + i")
}
func BenchmarkSmall_int_shift(b *testing.B) {
	benchProgram(b, "", "    x = i << 3")
}
func BenchmarkSmall_cmp(b *testing.B) {
	benchProgram(b, "x = 1", "    y = x < i")
}
func BenchmarkSmall_neg(b *testing.B) {
	benchProgram(b, "x = 1", "    y = -i")
}
