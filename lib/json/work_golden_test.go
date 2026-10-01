package json_test

// The work that encode, decode and indent cost, beyond the steps, on large and small
// operands, exactly, in testdata/work.golden: every price of the module
// (a unit a value, a quarter a byte quoted, the digits of an integer squared)
// is a line that changes if it is dropped. STARLARK_WRITE_STEPS=1 writes it.

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const jsonStepsSetup = `
N = $N
x = [i for i in range(N)]
sx = ['abc%d' % i for i in range(N // 10)]
d = {'k%d' % i: i for i in range(N // 10)}
bn = 1 << 511
for i in range(4):
    bn = bn * bn
s = json.encode(x)
sd = json.encode(d)
ss = json.encode(sx)
fl = '[' + '1.5,' * N + '1.5]'
ls = 'a' * (N * 4)
q = '"' + ls + '"'
qe = '"' + 'a\\n' * (N * 2) + '"'
nested = [[i, [i]] for i in range(N // 10)]
`

var jsonStepsOps = []struct{ name, op string }{
	{"encode(x)", "r = json.encode(x)"},
	{"encode(sx)", "r = json.encode(sx)"},
	{"encode(d)", "r = json.encode(d)"},
	{"encode(nested)", "r = json.encode(nested)"},
	{"encode(bn)", "r = json.encode(bn)"},
	{"encode(long string)", "r = json.encode(ls)"},
	{"encode(string with escapes)", "r = json.encode('a\\n' * (N * 2))"},
	{"encode_indent(x)", "r = json.encode_indent(x)"},
	{"encode_indent(d)", "r = json.encode_indent(d)"},
	{"decode(s)", "r = json.decode(s)"},
	{"decode(sd)", "r = json.decode(sd)"},
	{"decode(ss)", "r = json.decode(ss)"},
	{"decode(floats)", "r = json.decode(fl)"},
	{"decode(4000 digits)", "r = json.decode('1' * 4000)"},
	{"decode(long string)", "r = json.decode(q)"},
	{"decode(string with escapes)", "r = json.decode(qe)"},
	{"indent(s)", "r = json.indent(s)"},
	{"indent(sd)", "r = json.indent(sd)"},
	{"indent(ss)", "r = json.indent(ss)"},
}

func jsonSteps(t *testing.T, setup, op string) (uint64, error) {
	t.Helper()
	th := &starlark.Thread{Name: "steps"}
	var base uint64
	extra := starlark.StringDict{
		"json": json.Module,
		"reset": starlark.NewBuiltin("reset", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			base = th.Work() - th.Steps
			return starlark.None, nil
		}),
	}
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}, th, "t.star", setup+"\nreset()\n"+op+"\n", extra)
	return th.Work() - th.Steps - base, err
}

func TestJSON_WorkGolden(t *testing.T) {
	var got strings.Builder
	for _, scale := range []struct {
		name string
		n    int
	}{{"", 100000}, {"small: ", 1000}} {
		setup := strings.ReplaceAll(jsonStepsSetup, "$N", fmt.Sprint(scale.n))
		for _, o := range jsonStepsOps {
			n, err := jsonSteps(t, setup, o.op)
			if err != nil {
				t.Errorf("%s%s: %v", scale.name, o.name, err)
				continue
			}
			fmt.Fprintf(&got, "%s%s\t%d\n", scale.name, o.name, n)
			if n < 50 && scale.name == "" && o.name != "encode(bn)" && o.name != "decode(4000 digits)" {
				t.Errorf("%s: %d units of work for an operation on a large operand", o.name, n)
			}
		}
	}
	const file = "testdata/work.golden"
	if os.Getenv("STARLARK_WRITE_STEPS") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wl, gl := strings.Split(strings.TrimSpace(string(want)), "\n"), strings.Split(strings.TrimSpace(got.String()), "\n")
	if len(wl) != len(gl) {
		t.Errorf("%d operations, the file has %d", len(gl), len(wl))
	}
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			t.Errorf("steps changed:\n  was %s\n  now %s", wl[i], gl[i])
		}
	}
}
