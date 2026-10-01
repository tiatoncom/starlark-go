package json_test

// Tests of the allocation accounting of the json module (see alloc.go in
// package starlark): the strings and values it builds are charged to the
// thread's budget before they are built.

import (
	"errors"
	"os"
	osexec "os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	budget1MiB   = 1 << 20
	budgetPrefix = "starlark: allocation budget exhausted: "
)

type run struct {
	th      *starlark.Thread
	err     error
	marks   []uint64
	works   []uint64 // Work() at each mark()
	memMark uint64   // runtime TotalAlloc at the first mark()
	memEnd  uint64   // and when the program ended
}

func exec(t *testing.T, budget uint64, src string) *run {
	t.Helper()
	r := &run{th: &starlark.Thread{Name: "t"}}
	r.th.SetMaxAllocBytes(budget)
	predeclared := starlark.StringDict{
		"json": json.Module,
		"mark": starlark.NewBuiltin("mark", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			r.marks = append(r.marks, th.AllocatedBytes())
			r.works = append(r.works, th.Work())
			if len(r.marks) == 1 {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				r.memMark = ms.TotalAlloc
			}
			return starlark.None, nil
		}),
	}
	opts := &syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}
	_, r.err = starlark.ExecFileOptions(opts, r.th, "t.star", src, predeclared)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.memEnd = ms.TotalAlloc
	return r
}

func TestJSON_ChargesExactly(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		want            uint64
	}{
		{"encode", "x = [1, 2, 3]", "r = json.encode(x)", 7},                                                // [1,2,3]
		{"encode/string", "x = 'abc'", "r = json.encode(x)", 5},                                             // "abc"
		{"encode_indent", "x = [1, 2]", "r = json.encode_indent(x, indent=' ')", 5 + 10},                    // encode + the indented form
		{"indent", "x = '[1,2]'", "r = json.indent(x, indent=' ')", 10},                                     // [\n 1,\n 2\n]
		{"decode/list", "x = '[1,2,3]'", "r = json.decode(x)", 48 + 3*16},                                   // a slot per element
		{"decode/string", "x = '\"abc\"'", "r = json.decode(x)", 3},                                         // by length
		{"decode/dict", "x = '{\"a\":1,\"b\":2}'", "r = json.decode(x)", 512 + 2},                           // an entry per key, the key strings
		{"decode/nested", "x = '[[1],[2,3]]'", "r = json.decode(x)", (48 + 2*16) + (48 + 16) + (48 + 2*16)}, // slots of the outer and inner lists
		{"decode/dict of 10", "x = '{\"a\":1,\"b\":2,\"c\":3,\"d\":4,\"e\":5,\"f\":6,\"g\":7,\"h\":8,\"i\":9,\"j\":0}'", "r = json.decode(x)", 512 + 3*128 + 10},
		{"decode/bigint", "x = '123456789012345678901234567890'", "r = json.decode(x)", 13},               // 97 bits
		{"decode/scalars are free", "x = 'true'", "r = json.decode(x)", 0},                                // not a container
		{"decode/invalid with default", "x = '[1,'", "r = json.decode(x, 5)", 48 + 16},                    // a syntax error: the default; charged as built
		{"decode/invalid with default, partly built", "x = '[1,2,x'", "r = json.decode(x, 5)", 48 + 2*16}, // charged as it was built
	} {
		src := c.setup + "\nmark()\n" + c.op + "\nmark()\n"
		for _, budget := range []uint64{0, 1 << 30} {
			r := exec(t, budget, src)
			if r.err != nil {
				t.Errorf("%s: %v", c.name, r.err)
				continue
			}
			if got := r.marks[1] - r.marks[0]; got != c.want {
				t.Errorf("%s (budget %d): charged %d, want %d", c.name, budget, got, c.want)
			}
		}
	}
}

func wantBudgetError(t *testing.T, name string, r *run, budget uint64) {
	t.Helper()
	if r.err == nil {
		t.Errorf("%s: succeeded (charged %d of %d)", name, r.th.AllocatedBytes(), budget)
		return
	}
	var be *starlark.AllocBudgetError
	if !errors.As(r.err, &be) || !strings.HasPrefix(r.err.Error(), budgetPrefix) {
		t.Errorf("%s: not the budget error: %v", name, r.err)
		return
	}
	if r.th.AllocatedBytes() > budget {
		t.Errorf("%s: charged %d over the budget %d", name, r.th.AllocatedBytes(), budget)
	}
}

func TestJSON_RefusesOverBudget(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		// shared substructure: the output is exponential in the repetitions
		{"encode/shared", "x = [1]\nfor i in range(22): x = [x, x]\nr = json.encode(x)"},
		{"encode_indent/shared", "x = [1]\nfor i in range(22): x = [x, x]\nr = json.encode_indent(x)"},
		{"encode/large", "s = 'a' * 400000\nr = json.encode([s, s, s])"},
		{"encode/large dict", "s = 'a' * 400000\nr = json.encode({'a': s, 'b': s, 'c': s})"},
		// the indent string is repeated per nesting level and line
		{"indent/long indent", "s = '[' + '1,' * 2000 + '1]'\nr = json.indent(s, indent='x' * 1000)"},
		{"indent/nesting", "s = '[' * 400 + ']' * 400\nr = json.indent(s, indent='x' * 100)"},
		{"encode_indent/long indent", "r = json.encode_indent([1] * 2000, indent='x' * 1000)"},
		// decode: slots and entries per element
		{"decode/list", "s = '[' + '1,' * 100000 + '1]'\nr = json.decode(s)"},
		{"decode/dict", "s = '{' + ','.join(['\"k%d\":1' % i for i in range(10000)]) + '}'\nr = json.decode(s)"},
		{"decode/with default", "s = '[' + '1,' * 100000 + '1]'\nr = json.decode(s, 5)"}, // not swallowed as a syntax error
	} {
		r := exec(t, budget1MiB, c.src)
		wantBudgetError(t, c.name, r, budget1MiB)
	}
}

func TestJSON_DecodeStillReturnsDefaultOnSyntaxError(t *testing.T) {
	r := exec(t, budget1MiB, "r = json.decode('[1,', 5)\nif r != 5: fail('no default')\ns = json.decode('{\"a\": [1, {\"b\": null}]}')\nif s != {'a': [1, {'b': None}]}: fail('wrong value')")
	if r.err != nil {
		t.Fatal(r.err)
	}
}

func TestJSON_RoundTripWithinBudget(t *testing.T) {
	src := `
x = {"a": [1, 2, {"b": "c"}], "d": None, "e": 1.5}
s = json.encode(x)
y = json.decode(s)
if y != x: fail("round trip")
if json.indent(s) != json.encode_indent(x, prefix="", indent="\t"): fail("indent")
`
	if r := exec(t, budget1MiB, src); r.err != nil {
		t.Fatal(r.err)
	}
}

// ---- deep nesting: a stack overflow is fatal, so it is tested in a child ----

func TestJSON_DeepNesting_ChildProcess(t *testing.T) {
	if os.Getenv("STARLARK_JSON_DEEP_CHILD") == "1" {
		debug.SetMaxStack(48 << 20) // 60000 levels would need ~60 MiB (165 MiB under the race detector), MaxValueDepth levels ~10 MiB (28 MiB)
		const depth = 60000
		th := &starlark.Thread{}
		deepList := func() starlark.Value {
			v := starlark.NewList(nil)
			for i := 0; i < depth; i++ {
				v = starlark.NewList([]starlark.Value{v})
			}
			return v
		}
		deepTuple := func() starlark.Value {
			var v starlark.Value = starlark.Tuple{}
			for i := 0; i < depth; i++ {
				v = starlark.Tuple{v}
			}
			return v
		}
		deepDict := func() starlark.Value {
			var v starlark.Value = new(starlark.Dict)
			for i := 0; i < depth; i++ {
				d := new(starlark.Dict)
				d.SetKey(starlark.String("k"), v)
				v = d
			}
			return v
		}
		for name, v := range map[string]starlark.Value{"list": deepList(), "tuple": deepTuple(), "dict": deepDict()} {
			_, err := starlark.Call(th, json.Module.Members["encode"], starlark.Tuple{v}, nil)
			if err == nil || !strings.Contains(err.Error(), "nested more than") {
				t.Errorf("encode of a deep %s: %v", name, err)
			} else if len(err.Error()) > 1000 {
				t.Errorf("encode of a deep %s: the error is %d bytes", name, len(err.Error()))
			}
		}
		for _, doc := range []string{strings.Repeat("[", depth) + strings.Repeat("]", depth), strings.Repeat("{\"a\":", depth) + "1" + strings.Repeat("}", depth)} {
			_, err := starlark.Call(th, json.Module.Members["decode"], starlark.Tuple{starlark.String(doc)}, nil)
			if err == nil || !strings.Contains(err.Error(), "nested more than") {
				t.Errorf("decode of a deep document: %v", err)
			}
			// ... also with a default: the nesting is not a syntax error.
			_, err = starlark.Call(th, json.Module.Members["decode"], starlark.Tuple{starlark.String(doc), starlark.MakeInt(5)}, nil)
			if err == nil {
				t.Errorf("decode of a deep document with a default succeeded")
			}
		}
		t.Log("DEEP-JSON-OK")
		return
	}
	cmd := osexec.Command(os.Args[0], "-test.run=^TestJSON_DeepNesting_ChildProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "STARLARK_JSON_DEEP_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DEEP-JSON-OK") {
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[:1000] + "\n...\n" + tail[len(tail)-1000:]
		}
		t.Fatalf("the child process failed: %v\n%s", err, tail)
	}
}

func TestJSON_NestingAtTheLimit(t *testing.T) {
	// MaxValueDepth levels encode and decode; one more is refused.
	ok := strings.Repeat("[", starlark.MaxValueDepth) + strings.Repeat("]", starlark.MaxValueDepth)
	r := exec(t, 0, "x = json.decode('"+ok+"')\ny = json.encode(x)\nif y != '"+ok+"': fail('round trip')\n")
	if r.err != nil {
		t.Errorf("at the limit: %v", r.err)
	}
	over := strings.Repeat("[", starlark.MaxValueDepth+1) + strings.Repeat("]", starlark.MaxValueDepth+1)
	r = exec(t, 0, "x = json.decode('"+over+"')\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "nested more than") {
		t.Errorf("past the limit: %v", r.err)
	}
}

// ---- B4, B5: the peak is before the allocation ----

func TestJSON_EncodeOfALargeStringIsRefusedBeforeItIsQuoted(t *testing.T) {
	// 16 MiB of NUL quote to 96 MiB (\u0000 each); the budget is 32 MiB.
	const budget = 32 << 20
	r := exec(t, budget, "s = '\\x00' * (16 << 20)\nmark()\nr = json.encode(s)\n")
	wantBudgetError(t, "encode", r, budget)
	if spent := r.memEnd - r.memMark; spent > 1<<20 {
		t.Errorf("the refused encode allocated %d bytes", spent)
	}
}

func TestJSON_DecodeOfEmptyObjectsIsCharged(t *testing.T) {
	// 400000 empty objects are 200 MB: {} is 512 bytes, not 16.
	const budget = 8 << 20
	r := exec(t, budget, "s = '[' + '{},' * 400000 + '{}]'\nmark()\nr = json.decode(s)\n")
	wantBudgetError(t, "decode", r, budget)
	if spent := r.memEnd - r.memMark; spent > 4*budget {
		t.Errorf("the refused decode allocated %d bytes (budget %d)", spent, budget)
	}
	r = exec(t, budget, "s = '[' + '[],' * 400000 + '[]]'\nmark()\nr = json.decode(s)\n")
	wantBudgetError(t, "decode of empty lists", r, budget)
}

func TestJSON_EncodeExactlyTheRemainingBudget(t *testing.T) {
	// json.encode of n 'a's is n+2 bytes; the setup charges n.
	for _, c := range []struct {
		n  int
		ok bool
	}{{100, true}, {101, false}} {
		r := exec(t, 202, "s = 'a' * "+itoa(c.n)+"\nr = json.encode(s)\n")
		if c.ok && (r.err != nil || r.th.AllocatedBytes() != 202) {
			t.Errorf("n=%d: err %v, charged %d, want exactly the budget 202", c.n, r.err, r.th.AllocatedBytes())
		}
		if !c.ok {
			wantBudgetError(t, "encode past the budget", r, 202)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// ---- time: the work of encode, decode and indent is charged as work ----

func TestJSON_WorkIsChargedAsWork(t *testing.T) {
	// 20000 numbers: ~20000 nodes to write, quoted bytes, a list: a unit a node
	// at least, besides the steps of the program.
	r := exec(t, 0, "x = [i for i in range(20000)]\nmark()\ns = json.encode(x)\nmark()\ny = json.decode(s)\nmark()\nz = json.indent(s)\nmark()\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.works[1] - r.works[0]; got < 20000 {
		t.Errorf("encode of 20000 numbers was charged %d units of work", got)
	}
	if got := r.works[2] - r.works[1]; got < 20000 {
		t.Errorf("decode of 20000 numbers was charged %d units of work", got)
	}
	if got := r.works[3] - r.works[2]; got < 20000/4 {
		t.Errorf("indent of 20000 numbers was charged %d units of work", got)
	}
	// The steps are those of the program alone, as without the calls.
	if base := exec(t, 0, "x = [i for i in range(20000)]\nmark()\ns = 1\nmark()\ny = 1\nmark()\nz = 1\nmark()\n"); r.th.Steps-base.th.Steps > 20 {
		t.Errorf("%d steps for the three calls: the work of the module is not in them", r.th.Steps-base.th.Steps)
	}
	// With a limit of work they are stopped: encode of a large value is
	// refused, not run to the end.
	th := &starlark.Thread{}
	th.SetMaxWork(300_000)
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, th, "t.star",
		"x = [i for i in range(20000)]\nfor i in range(100): s = json.encode(x)\n", starlark.StringDict{"json": json.Module})
	var we *starlark.WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v (work %d)", err, th.Work())
	}
	if th.Work() != 300_000 {
		t.Errorf("work %d after the refusal, want the limit", th.Work())
	}
}

// A value of the host that counts the nodes that encode asks it to write.
type countingMarshaler struct{ n *int }

func (c countingMarshaler) String() string               { return "counting" }
func (c countingMarshaler) Type() string                 { return "counting" }
func (c countingMarshaler) Freeze()                      {}
func (c countingMarshaler) Truth() starlark.Bool         { return true }
func (c countingMarshaler) Hash() (uint32, error)        { return 0, nil }
func (c countingMarshaler) MarshalJSON() ([]byte, error) { *c.n++; return []byte("1"), nil }

// One operation is stopped near the limit of work, not at its end: encode of a
// list of 400000 values stops after about the units that the limit allows (a
// chunk of metering past it), and writes no more of the list.
func TestJSON_AnOperationIsStoppedNearTheLimit(t *testing.T) {
	var n int
	l := starlark.NewList(nil)
	for i := 0; i < 400000; i++ {
		l.Append(countingMarshaler{&n})
	}
	th := &starlark.Thread{}
	th.SetMaxWork(5000)
	_, err := starlark.Call(th, json.Module.Members["encode"], starlark.Tuple{l}, nil)
	var we *starlark.WorkBudgetError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	// a node is at least a unit: at most 5000 + a chunk of them were written
	if n > 5000+2048 {
		t.Errorf("%d of 400000 nodes were written under a limit of 5000 units", n)
	}
	if th.Work() != 5000 {
		t.Errorf("work %d, want the limit", th.Work())
	}
}

// A value with shared substructure writes an output exponential in its depth:
// encode stops when the output outgrows the headroom of the thread, not after
// it has written it. A DAG of 2^22 leaves is 8 MiB of text: with 1 MiB, refused.
func TestJSON_EncodeOfASharedDAGIsStoppedAtTheHeadroom(t *testing.T) {
	r := exec(t, budget1MiB, "t = [1]\nfor i in range(22):\n  t = [t, t]\ns = json.encode(t)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), budgetPrefix) {
		t.Fatalf("err = %v", r.err)
	}
	// ... and the nodes visited are those of the headroom (a node is 2 bytes of
	// the output, at least), not the 2^22 of the whole DAG.
	if r.th.Steps > 100_000 {
		t.Errorf("%d steps: the encoding went on after the headroom was used", r.th.Steps)
	}
	// With room for it, the output is what it is.
	r = exec(t, 64<<20, "t = [1]\nfor i in range(10):\n  t = [t, t]\ns = json.encode(t)\nn = len(s)\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
}

// An integer of more than MaxIntDigits digits is not written, as it is not by
// str(), and not read by decode.
func TestJSON_BigIntegersHaveTheDigitLimit(t *testing.T) {
	r := exec(t, 0, "x = 1 << 511\nfor i in range(6):\n  x = x * x\ns = json.encode(x)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "decimal digits") {
		t.Errorf("encode of a 32000-bit integer: %v", r.err)
	}
	r = exec(t, 0, "x = 1 << 511\nfor i in range(4):\n  x = x * x\ns = json.encode(x)\ny = json.decode(s)\n")
	if r.err != nil {
		t.Errorf("encode of a 8000-bit integer: %v", r.err)
	}
	r = exec(t, 0, "y = json.decode('1' * 5000)\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "digits") {
		t.Errorf("decode of 5000 digits: %v", r.err)
	}
	r = exec(t, 0, "y = json.decode('1' * 4300)\n")
	if r.err != nil {
		t.Errorf("decode of 4300 digits: %v", r.err)
	}
}
