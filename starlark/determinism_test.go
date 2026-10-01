package starlark

// The steps of a program, its work, and the bytes it is charged, are a function of the
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
	{"uuids - set(ids), 100", `
ids = ['%x-%x-4%x-a%x-%x' % (i * 2654435761 % 4294967296, i % 65536, i % 4096, i * 7 % 4096, i * 1103515245 % 281474976710656) for i in range(100)]
r = len(set(ids))
`},
	{"uuids - set(ids), 1000", `
ids = ['%x-%x-4%x-a%x-%x' % (i * 2654435761 % 4294967296, i % 65536, i % 4096, i * 7 % 4096, i * 1103515245 % 281474976710656) for i in range(1000)]
r = len(set(ids))
`},
	{"e-mails - dict(zip), s|t, d==e, update", `
ids = ['user.%d@example-company-%d.org' % (i, i % 37) for i in range(1000)]
d = dict(zip(ids, range(1000)))
e = dict(zip(reversed(ids), reversed(range(1000))))
s = set(ids[:600])
t = set(ids[400:])
u = {}
u.update(d)
u.update(e)
r = (len(d), d == e, len(s | t), len(s & t), len(s - t), len(s ^ t), len(u))
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

func runDeterminismProgram(src string) (steps, work, alloc uint64, result Value, err error) {
	th := &Thread{Name: "det"}
	th.SetMaxAllocBytes(1 << 30)
	g, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}, th, "t.star", src, nil)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	return th.Steps, th.Work(), th.AllocatedBytes(), g["r"], nil
}

func TestDeterminism_StepsAreTheSameInEveryProcess(t *testing.T) {
	if os.Getenv("STARLARK_DET_CHILD") == "1" {
		for _, p := range determinismPrograms {
			steps, work, alloc, r, err := runDeterminismProgram(p.src)
			if err != nil {
				fmt.Printf("DET %s: error %v\n", p.name, err)
				continue
			}
			fmt.Printf("DET %s: steps %d work %d alloc %d result %v\n", p.name, steps, work, alloc, r)
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

// hashByLength are the hashes of the prefixes of a text, by length: FNV-1a up to
// 11 bytes (as in v0.2.0), and from 12 bytes the fixed-key hash of hashtable.go
// (v0.2.0 had one with a random seed there). A change of the function, of the
// length at which it begins, or of the way it takes the bytes at the end, is a
// change of the steps of every program that keeps such keys in a dict.
var hashByLength = []uint32{
	2166136261, 3507227459, 1089836209, 379144636, 793698452, 1579944063, 1230022590, 1090008949,
	1556002722, 3376107883, 325122321, 4104695817, 1541095329, 1880947173, 1252006628, 879963087,
	1554180710, 597779906, 4163389171, 4286725217, 2091802317, 329542255, 211147958, 3975057898,
	4269330920, 2260966766, 1785926101, 943013459, 1978570197, 3008203101, 2670924539, 186583636,
	1398587218, 917064843, 1439953342, 2958933362, 252502126, 4125134233, 130905657, 759191180,
	2397680512, 1913793607, 1097170107, 1091321398, 3890381201, 1321695116, 2738376740, 1650508382,
	1362493484, 4088720849,
}

func TestDeterminism_HashOfEveryLength(t *testing.T) {
	base := "The quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJ"
	for n, want := range hashByLength {
		if got := hashString(base[:n]); got != want {
			t.Errorf("hashString of %d bytes = %d, want %d", n, got, want)
		}
	}
	for n := 0; n < 12; n++ {
		if got, want := hashString(base[:n]), softHashString(base[:n]); got != want {
			t.Errorf("hashString of %d bytes = %d, not the FNV-1a hash %d", n, got, want)
		}
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
