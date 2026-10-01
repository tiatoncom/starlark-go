package starlark

// The steps of a program, and the bytes it is charged, are a function of the
// program and its input, the same in every process: they are recorded in the
// audit of the host and decide a verdict at the border of a budget. Nothing
// the process picks (the seed of a hash, an address, the order of a Go map)
// may reach them. Each program of the battery is run in several child
// processes, and the numbers of all of them must be equal to the unit.

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

var determinismPrograms = []struct{ name, src string }{
	{"insert long string keys", `
d = {}
for i in range(60000):
    d['key-long-prefix-' + str(i)] = i
r = len(d)
`},
	{"dict(d) and a comprehension", `
d = {}
for i in range(40000):
    d['key-long-prefix-' + str(i)] = i
e = dict(d)
f = {k: v for k, v in d.items()}
r = len(e) + len(f)
`},
	{"d == e", `
d = {}
e = {}
for i in range(40000):
    d['key-long-prefix-' + str(i)] = i
    e['key-long-prefix-' + str(39999 - i)] = 39999 - i
r = d == e
`},
	{"x in d and d.get", `
d = {}
for i in range(40000):
    d['key-long-prefix-' + str(i)] = i
n = 0
for i in range(0, 80000, 2):
    if ('key-long-prefix-' + str(i)) in d:
        n += 1
    n += d.get('key-long-prefix-' + str(i + 1), 0) % 2
r = n
`},
	{"set operations", `
s = set(['key-long-prefix-' + str(i) for i in range(30000)])
t = set(['key-long-prefix-' + str(i) for i in range(15000, 45000)])
r = len(s | t) + len(s & t) + len(s - t) + len(s ^ t) + len(s.union(t)) + len(s.intersection(t)) + len(s.difference(t))
`},
	{"tuple keys of strings", `
d = {}
for i in range(20000):
    d[('first-long-component-' + str(i), 'second-long-component-' + str(i % 100))] = i
r = len(d)
`},
	{"bytes keys and update", `
d = {}
for i in range(20000):
    d[bytes('key-long-prefix-' + str(i))] = i
e = {}
for i in range(10000, 30000):
    e[bytes('key-long-prefix-' + str(i))] = i
d.update(e)
d |= e
r = len(d)
`},
	{"order of iteration", `
d = {}
for i in range(5000):
    d['key-long-prefix-' + str(i * 7919 % 5003)] = i
h = 0
for k in d:
    h = (h * 31 + len(k) + ord(k[-1])) % 1000003
r = h
`},
}

func runDeterminismProgram(src string) (steps, alloc uint64, result Value, err error) {
	th := &Thread{Name: "det"}
	th.SetMaxAllocBytes(1 << 30)
	g, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, nil)
	if err != nil {
		return 0, 0, nil, err
	}
	return th.Steps, th.AllocatedBytes(), g["r"], nil
}

func TestDeterminism_StepsAreTheSameInEveryProcess(t *testing.T) {
	if os.Getenv("STARLARK_DET_CHILD") == "1" {
		for _, p := range determinismPrograms {
			steps, alloc, r, err := runDeterminismProgram(p.src)
			if err != nil {
				fmt.Printf("DET %s: error %v\n", p.name, err)
				continue
			}
			fmt.Printf("DET %s: steps %d alloc %d result %v\n", p.name, steps, alloc, r)
		}
		return
	}
	const children = 8
	outputs := make([]map[string]string, children)
	for i := range outputs {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDeterminism_StepsAreTheSameInEveryProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "STARLARK_DET_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, out)
		}
		outputs[i] = map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if rest, ok := strings.CutPrefix(line, "DET "); ok {
				name, val, _ := strings.Cut(rest, ": ")
				outputs[i][name] = val
			}
		}
	}
	var names []string
	for _, p := range determinismPrograms {
		names = append(names, p.name)
	}
	sort.Strings(names)
	for _, name := range names {
		first := outputs[0][name]
		if first == "" || strings.HasPrefix(first, "error") {
			t.Errorf("%s: %q", name, first)
			continue
		}
		for i := 1; i < children; i++ {
			if outputs[i][name] != first {
				t.Errorf("%s: process 0 gives\n  %s\nprocess %d gives\n  %s", name, first, i, outputs[i][name])
				break
			}
		}
	}
}

// The hash of a string is a fixed function of it: the same values in every
// process, and (for the built-in hash(), which a script sees) the same as v0.2.0.
func TestDeterminism_HashOfStringsIsFixed(t *testing.T) {
	for s, want := range map[string]uint32{
		"key-long-prefix-1":                      1602521325,
		"0123456789abcdef":                       1888491190,
		"hello world!":                           2619588429,
		"x1234567890123456789012345678901234567": 1059773078,
	} {
		if got := hashString(s); got != want {
			t.Errorf("hashString(%q) = %d, want %d", s, got, want)
		}
		if got, _ := String(s).Hash(); got != want {
			t.Errorf("String(%q).Hash() = %d, want %d", s, got, want)
		}
		if got, _ := Bytes(s).Hash(); got != want {
			t.Errorf("Bytes(%q).Hash() = %d, want %d", s, got, want)
		}
	}
	// The built-in hash() is Java's String.hashCode, as in v0.2.0 (values
	// taken from v0.2.0).
	g, err := ExecFileOptions(&syntax.FileOptions{}, &Thread{}, "t.star", `
r = [hash(s) for s in ["", "a", "abc", "hello world", "key-long-prefix-1", "x" * 1000, "é" * 50, "\\x00\\x01" * 20, "0123456789abcdef" * 3]]
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := g["r"].String(), "[0, 97, 96354, 1794106052, -560553735, -1715418112, 783841312, -1084464524, 1555276792]"; got != want {
		t.Errorf("hash() = %s, want %s (v0.2.0)", got, want)
	}
}

// The hash spreads the keys that programs make: names with a counter, a long
// common prefix or suffix, the same character repeated, keys that differ in
// one byte at any place. A bad hash is a long chain, which costs steps.
func TestDeterminism_HashSpreadsTheKeys(t *testing.T) {
	families := map[string]func(i int) string{
		"prefix+counter": func(i int) string { return fmt.Sprintf("key-long-prefix-%d", i) },
		"counter+suffix": func(i int) string { return fmt.Sprintf("%d-long-suffix-for-a-key", i) },
		"repeated":       func(i int) string { return strings.Repeat("a", 12+i%2000) + fmt.Sprint(i/2000) },
		"one byte differs at the end": func(i int) string {
			return strings.Repeat("x", 100) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26))
		},
		"one byte differs at the start": func(i int) string {
			return string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26)) + strings.Repeat("x", 100)
		},
		"16-byte blocks": func(i int) string { return fmt.Sprintf("%016d%016d", i, i*7) },
		"12 bytes":       func(i int) string { return fmt.Sprintf("%012d", i) },
		"13 to 15 bytes": func(i int) string { return fmt.Sprintf("%015d", i)[:13+i%3] },
	}
	for name, mk := range families {
		const n = 17000
		d := new(Dict)
		for i := 0; i < n; i++ {
			d.SetKey(String(mk(i)), None)
		}
		if d.Len() < n*9/10 && name != "13 to 15 bytes" && name != "repeated" {
			t.Errorf("%s: only %d distinct keys", name, d.Len())
		}
		if c := longestChain(&d.ht); c > 12 {
			t.Errorf("%s: the longest chain of %d keys has %d buckets (a good hash has ~8 at most)", name, d.Len(), c)
		}
	}
}
