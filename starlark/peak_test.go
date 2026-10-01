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
