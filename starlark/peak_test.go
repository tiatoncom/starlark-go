package starlark_test

// "Operation, charged, peak": the memory an operation allocates is what it
// charged, within a quarter. The bytes the process allocated during the
// operation (runtime TotalAlloc, an upper bound of the peak of the heap, and
// deterministic where a measure of the live heap is not) are compared with the
// bytes the operation charged: a result built in a buffer that grows, copied
// once more to become a string, made from a copy of its operand, is a result
// that is charged once and allocated three times.

import (
	"runtime"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type peakRow struct {
	name, setup, op string
}

// The tables of a dict or a set are made of entries that are allocated one by
// one and a table that is rebuilt as it doubles: what is allocated is up to
// twice what it holds. json.decode builds the elements of an array by append,
// which allocates up to five times the array, and charges its slots.
//
// peakMax is the ratio of the bytes allocated to the bytes charged that a row
// may reach, if it is not 1.25.
var peakMax = map[string]float64{
	"dict(zip)":             2,
	"set(l)":                2,
	"json.decode":           6,
	"json.decode (strings)": 6,
	"json.decode (dicts)":   6,
}

// peakRun returns the bytes charged and the bytes allocated by the process
// between the two calls of mark().
func peakRun(t *testing.T, extra starlark.StringDict, setup, op string) (charged, allocated uint64, err error) {
	t.Helper()
	th := &starlark.Thread{Name: "peak"}
	var marks, totals []uint64
	predeclared := starlark.StringDict{"mark": starlark.NewBuiltin("mark", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		marks = append(marks, th.AllocatedBytes())
		totals = append(totals, ms.TotalAlloc)
		return starlark.None, nil
	})}
	for k, v := range extra {
		predeclared[k] = v
	}
	_, err = starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", setup+"\nmark()\n"+op+"\nmark()\n", predeclared)
	if err != nil {
		return 0, 0, err
	}
	return marks[1] - marks[0], totals[1] - totals[0], nil
}

// peakTable is the table of operations of a few MiB, for the test and for the
// log of the table of the report.
var peakTable = []peakRow{
	{"str.join", "l = ['abcdefghij'] * 400000", "r = ','.join(l)"},
	{"str.replace", "s = 'abcdefghij' * 400000", "r = s.replace('abc', 'xyz12')"},
	{"str.replace (shrinks)", "s = 'abcdefghij' * 400000", "r = s.replace('abc', '')"},
	{"s % (a,)", "s = 'abcdefghij' * 400000", "r = '%s!' % s"},
	{"s % tuple", "t = tuple(['abcdefghij'] * 100000)", "r = ('%s,' * 100000) % t"},
	{"'{}'.format(s)", "s = 'abcdefghij' * 400000", "r = '{}!'.format(s)"},
	{"format of many", "t = ['abcdefghij'] * 100000", "r = ('{},' * 100000).format(*t)"},
	{"s * n", "s = 'abcdefghij' * 1000", "r = s * 400"},
	{"l * n", "l = list(range(1000))", "r = l * 400"},
	{"t * n", "t = tuple(range(1000))", "r = t * 400"},
	{"s + s", "s = 'abcdefghij' * 200000", "r = s + s"},
	{"sorted", "l = [(i * 7919) % 1000003 for i in range(200000)]", "r = sorted(l)"},
	{"sorted of strings", "l = [str((i * 7919) % 1000003) for i in range(200000)]", "r = sorted(l)"},
	{"bytes(list)", "l = [i % 256 for i in range(2000000)]", "r = bytes(l)"},
	{"bytes(str)", "s = 'abcdefghij' * 400000", "r = bytes(s)"},
	{"str(bytes)", "b = bytes('abcdefghij' * 400000)", "r = str(b)"},
	{"str(list)", "l = list(range(200000))", "r = str(l)"},
	{"repr(list of str)", "l = ['abcdefghij'] * 100000", "r = repr(l)"},
	{"print-like str of nested", "l = [[1, 2, 3]] * 100000", "r = str(l)"},
	{"s.upper()", "s = 'abcdefghij' * 400000", "r = s.upper()"},
	{"s.title()", "s = 'abcdefghij' * 400000", "r = s.title()"},
	{"s[::-1]", "s = 'abcdefghij' * 400000", "r = s[::-1]"},
	{"l[::2]", "l = list(range(2000000))", "r = l[::2]"},
	{"l[::-1]", "l = list(range(1000000))", "r = l[::-1]"},
	{"s.split", "s = 'abcdefghi,' * 200000", "r = s.split(',')"},
	{"s.splitlines", "s = 'abcdefghi\\n' * 200000", "r = s.splitlines()"},
	{"list(s.elems())", "s = 'abcdefghij' * 40000", "r = list(s.elems())"},
	{"list(range)", "", "r = list(range(1000000))"},
	{"l + l", "l = list(range(500000))", "r = l + l"},
	{"dict(zip)", "l = list(range(200000))", "r = dict(zip(l, l))"},
	{"set(l)", "l = list(range(300000))", "r = set(l)"},
	{"d | e", "d = {i: i for i in range(100000)}\ne = {i + 100000: i for i in range(100000)}", "r = d | e"},
	{"json.encode(list of int)", "l = list(range(500000))", "r = json.encode(l)"},
	{"json.encode(list of str)", "l = ['abcdefghij'] * 300000", "r = json.encode(l)"},
	{"json.encode(shared str)", "x = 'a' * 1000\nl = [x] * 4000", "r = json.encode(l)"},
	{"json.encode(dict)", "d = {'key%d' % i: i for i in range(100000)}", "r = json.encode(d)"},
	{"json.encode(nested)", "l = [[i, [i]] for i in range(100000)]", "r = json.encode(l)"},
	{"json.encode_indent", "l = [[i, [i]] for i in range(50000)]", "r = json.encode_indent(l)"},
	{"json.indent (flat)", "s = json.encode(list(range(400000)))", "r = json.indent(s)"},
	{"json.indent (nested)", "s = '[' * 2000 + '1,' * 100000 + '1' + ']' * 2000", "r = json.indent(s)"},
	{"json.decode", "s = json.encode(list(range(400000)))", "r = json.decode(s)"},
	{"json.decode (strings)", "s = json.encode(['abcdefghij'] * 200000)", "r = json.decode(s)"},
	{"json.decode (dicts)", "s = json.encode([{'a': i} for i in range(50000)])", "r = json.decode(s)"},
}

func TestPeak_AnOperationAllocatesWhatItCharged(t *testing.T) {
	jsonModule := starlark.StringDict{"json": json.Module}
	for _, row := range peakTable {
		charged, allocated, err := peakRun(t, jsonModule, row.setup, row.op)
		if err != nil {
			t.Errorf("%s: %v", row.name, err)
			continue
		}
		ratio := float64(allocated) / float64(max(charged, 1))
		t.Logf("%-28s charged %9d  allocated %9d  %.2fx", row.name, charged, allocated, ratio)
		limit := 1.25
		if m, ok := peakMax[row.name]; ok {
			limit = m
		}
		if float64(allocated) > float64(charged)*limit+64<<10 {
			t.Errorf("%s: %d bytes were allocated for %d charged (%.2fx, at most %.2fx)", row.name, allocated, charged, ratio, limit)
		}
	}
}

// "Idiom, charged, allocated": what an idiom is charged is not more than twice
// what the process allocated for it (garbage counts on both sides): the
// operations on keys that are there already (an update with the keys of the
// dict, a set of a list with many duplicates, an intersection that is the set
// itself) are charged by the entries they add, and values that exist (what
// d.get returns, a strip of nothing, a replace that changes nothing) are not
// charged again.
var chargedTable = []peakRow{
	{"d |= e, the same keys", "d = {i: i for i in range(100000)}\ne = dict(d)", "d |= e"},
	{"d.update(e), the same keys", "d = {i: i for i in range(100000)}\ne = dict(d)", "d.update(e)"},
	{"d.update(pairs), the same keys", "d = {i: i for i in range(100000)}\np = list(d.items())", "d.update(p)"},
	{"s.update(l), the same elements", "s = set(range(100000))\nl = list(range(100000))", "s.update(l)"},
	{"set(l), 100 distinct of 300000", "l = [i % 100 for i in range(300000)]", "r = set(l)"},
	{"s.intersection(l), all in", "s = set(range(100000))\nl = list(range(100000))", "r = s.intersection(l)"},
	{"s & t, a set with itself", "s = set(range(100000))\nt = set(s)", "r = s & t"},
	{"s | t, the same elements", "s = set(range(100000))\nt = set(s)", "r = s | t"},
	{"d | e, the same keys", "d = {i: i for i in range(100000)}\ne = dict(d)", "r = d | e"},
	{"s.symmetric_difference(l), the same", "s = set(range(100000))\nl = list(range(100000))", "r = s.symmetric_difference(l)"},
	{"s.difference(l), all removed", "s = set(range(100000))\nl = list(range(100000))", "r = s.difference(l)"},
	{"d.get x 1000 of a 10 KB value", "d = {'a': 'x' * 10000}", "for i in range(1000):\n    r = d.get('a')"},
	{"s.strip() of nothing x 1000", "s = 'x' * 10000", "for i in range(1000):\n    r = s.strip()"},
	{"s.replace(old, new, 0) x 1000", "s = 'x' * 10000", "for i in range(1000):\n    r = s.replace('x', 'y', 0)"},
	{"s.lower() of lowercase x 1000", "s = 'x' * 10000", "for i in range(1000):\n    r = s.lower()"},
	{"str(s) x 1000", "s = 'x' * 10000", "for i in range(1000):\n    r = str(s)"},
	{"l.pop() x 100000", "l = list(range(100000))", "for i in range(100000):\n    l.pop()"},
	{"min(l) / max(l) of lists", "l = [[i] for i in range(1000)]", "for i in range(100):\n    r = max(l)"},
}

func TestCharged_NotMoreThanTwiceWhatWasAllocated(t *testing.T) {
	jsonModule := starlark.StringDict{"json": json.Module}
	for _, row := range chargedTable {
		charged, allocated, err := peakRun(t, jsonModule, row.setup, row.op)
		if err != nil {
			t.Errorf("%s: %v", row.name, err)
			continue
		}
		t.Logf("CHARGED %-38s charged %9d  allocated %9d", row.name, charged, allocated)
		if charged > 2*allocated+64<<10 {
			t.Errorf("%s: charged %d bytes, the process allocated %d", row.name, charged, allocated)
		}
	}
}
