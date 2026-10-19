package json_test

// Group G2 (bytes): the charges of json.decode of a string that is unquoted by
// encoding/json (escapes, non-ASCII), and the calls of a host MarshalJSON.

import (
	"runtime"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// A string with an escape or a non-ASCII byte is unquoted by json.Unmarshal,
// which allocates about 2.2 bytes a byte of the raw string besides the result
// (and 152-176 bytes for a string of 3-6 bytes), more if the result outgrows
// the raw string (an invalid byte becomes 3). It is charged before:
// 3 * raw + 320, and after, the result and 4 bytes for each it adds to the raw.
func g2UnquoteBytes(raw, res int) uint64 {
	n := uint64(3*raw + 320 + res)
	if res > raw {
		n += uint64(4 * (res - raw))
	}
	return n
}

// g2Exec runs src with the Go string raw as the global x, and returns what
// exec does.
func g2Exec(t *testing.T, budget uint64, src, raw string) *run {
	t.Helper()
	r := &run{th: &starlark.Thread{Name: "t"}}
	r.th.SetMaxAllocBytes(budget)
	predeclared := starlark.StringDict{
		"json": json.Module,
		"x":    starlark.String(raw),
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

// the steps of the two calls of mark() and of the call of json.decode
const g2StepsOfTheProgram = 8

const g2Esc = "\\u" // a backslash and a u

func TestG2_DecodeUnquoteChargesExactly(t *testing.T) {
	for _, c := range []struct {
		name, src string
		raw, res  int
	}{
		{"escape", `"a\nb"`, 6, 3},
		{"non-ASCII", `"héllo"`, 8, 6},
		{"unicode escape", `"` + g2Esc + `0041"`, 8, 1},
		{"invalid UTF-8", "\"\xff\xff\"", 4, 6},
	} {
		src := "mark()\nr = json.decode(x)\nmark()\n"
		for _, budget := range []uint64{0, 1 << 30} {
			r := g2Exec(t, budget, src, c.src)
			if r.err != nil {
				t.Errorf("%s: %v", c.name, r.err)
				continue
			}
			if got, want := r.marks[1]-r.marks[0], g2UnquoteBytes(c.raw, c.res); got != want {
				t.Errorf("%s (budget %d): charged %d bytes, want %d", c.name, budget, got, want)
			}
			// 6 for the value, 4 for the call of Unmarshal and raw/4 for its scans,
			// and a sixteenth of a unit for each byte charged (by the charge of
			// the base, and by that of the result, whole units each).
			pre := uint64(3*c.raw + 320)
			want := uint64(g2StepsOfTheProgram+6+4+c.raw/4) + pre/16 + uint64(c.res)/16
			if c.res > c.raw {
				want += uint64(4*(c.res-c.raw)) / 16
			}
			if got := r.works[1] - r.works[0]; got != want {
				t.Errorf("%s (budget %d): charged %d work, want %d", c.name, budget, got, want)
			}
		}
	}
}

// A string that needs no unquoting is charged by its length, as before.
func TestG2_DecodeSafeStringUnchanged(t *testing.T) {
	r := g2Exec(t, 0, "mark()\nr = json.decode(x)\nmark()\n", `"abcdefgh"`)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.marks[1] - r.marks[0]; got != 8 {
		t.Errorf("charged %d bytes, want 8", got)
	}
	if got, want := r.works[1]-r.works[0], uint64(g2StepsOfTheProgram+6+8/4); got != want {
		t.Errorf("charged %d work, want %d", got, want)
	}
}

// A big integer is a big.Int and its digits: allocBaseInt + 8 * words (the
// table of alloc.go: 64 + 9 * words; 97 bits are 2 words).
func TestG2_DecodeBigIntChargedLikeABigInt(t *testing.T) {
	for _, c := range []struct {
		name, num string
		bits      int // of the magnitude, as the package charges it
	}{
		{"30 digits (97 bits)", "123456789012345678901234567890", 97},
		{"negative", "-123456789012345678901234567890", 97},
		{"4000 digits", strings.Repeat("9", 4000), 13288},
		{"10 digits, 31 bits: small", "2000000000", 31},
		{"10 digits, 33 bits", "8000000000", 33},
		{"18 digits (60 bits)", "999999999999999999", 60},
		{"19 digits (63 bits)", "5000000000000000000", 63},
		{"9 digits: small", "123456789", 27},
	} {
		r := g2Exec(t, 0, "mark()\nr = json.decode(x)\nmark()\n", c.num)
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		// exactly what the package charges for an Int of that many bits (the
		// 4000 digits are the upper bound of the bits: 13288 is 13290)
		want := starlark.BigIntAllocBytes(c.bits)
		if c.name == "4000 digits" {
			want = starlark.BigIntAllocBytes(4000*3322/1000 + 2)
		}
		if got := r.marks[1] - r.marks[0]; got != want {
			t.Errorf("%s: charged %d, want %d", c.name, got, want)
		}
	}
}

// A big integer that does not fit the budget is refused before it is made.
func TestG2_DecodeBigIntIsChargedBeforeItIsMade(t *testing.T) {
	// a budget of one word of a big integer: 64 + 9 = 73 bytes
	r := g2Exec(t, 73, "r = json.decode(x)\n", "123456789012345678901234567890123456789")
	wantBudgetError(t, "big int", r, 73)
}

// What the runtime allocates for a decode of such strings is not more than
// what it charged (and so not more than 6 times).
func TestG2_DecodeUnquotePeak(t *testing.T) {
	const bs = "\\" // a backslash
	for _, c := range []struct{ name, raw string }{
		{"escapes, one string", `"` + strings.Repeat(bs+"n", 100000) + `"`},
		{"unicode escapes, one string", `"` + strings.Repeat(bs+"u"+"0041", 100000) + `"`},
		{"surrogate pairs, one string", `"` + strings.Repeat(bs+"ud83d"+bs+"ude00", 50000) + `"`},
		{"non-ASCII, one string", `"` + strings.Repeat("é", 100000) + `"`},
		{"4-byte non-ASCII, one string", `"` + strings.Repeat("\U0001F600", 50000) + `"`},
		{"invalid UTF-8, one string", `"` + strings.Repeat("\xff", 50000) + `"`},
		{"one escape at the end", `"` + strings.Repeat("a", 100000) + bs + `n"`},
		{"many short escapes", `[` + strings.Repeat(`"a`+bs+`n",`, 20000) + `"a"]`},
		{"many short non-ASCII", `[` + strings.Repeat(`"é",`, 20000) + `"a"]`},
		{"many short invalid UTF-8", `[` + strings.Repeat("\"\xff\",", 20000) + `"a"]`},
	} {
		r := g2Exec(t, 0, "mark()\nr = json.decode(x)\nmark()\n", c.raw)
		if r.err != nil {
			t.Errorf("%s: %v", c.name, r.err)
			continue
		}
		charged := r.marks[1] - r.marks[0]
		allocated := r.memEnd - r.memMark
		t.Logf("%-32s charged %9d allocated %9d ratio %.2f", c.name, charged, allocated, float64(allocated)/float64(charged))
		if allocated > charged {
			t.Errorf("%s: allocated %d bytes, charged %d", c.name, allocated, charged)
		}
	}
}

// ---- a host value that marshals itself ----

type g2Host struct {
	n     int
	calls *int
}

func (h g2Host) String() string        { return "g2Host" }
func (h g2Host) Type() string          { return "g2Host" }
func (h g2Host) Freeze()               {}
func (h g2Host) Truth() starlark.Bool  { return true }
func (h g2Host) Hash() (uint32, error) { return 0, nil }
func (h g2Host) MarshalJSON() ([]byte, error) {
	*h.calls++
	return []byte(`"` + strings.Repeat("a", h.n) + `"`), nil
}

func TestG2_EncodeCallsAHostMarshalJSONOnce(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int // the length of the string it returns, the output is n+2
		wrap string
	}{
		{"small", 100, "[h]"},
		{"above 4096", 5000, "[h]"},
		{"above 4096, bare", 5000, "h"},
		{"above 4096, three values", 2000, "[h, h, {'k': h}]"},
	} {
		calls := 0
		th := &starlark.Thread{Name: "t"}
		th.SetMaxAllocBytes(1 << 30)
		predeclared := starlark.StringDict{
			"json": json.Module,
			"h":    g2Host{c.n, &calls},
		}
		opts := &syntax.FileOptions{GlobalReassign: true, TopLevelControl: true}
		g, err := starlark.ExecFileOptions(opts, th, "t.star", "x = "+c.wrap+"\nr = json.encode(x)\n", predeclared)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		out := string(g["r"].(starlark.String))
		hosts := strings.Count(out, `"a`)
		if hosts == 0 {
			hosts = 1
		}
		if calls != hosts {
			t.Errorf("%s: MarshalJSON was called %d times for %d values (output %d bytes)", c.name, calls, hosts, len(out))
		}
		// The output is charged once: its bytes, the items of a dict (64 B), and
		// the list the program built (the setup).
		setup := uint64(0)
		switch c.wrap {
		case "[h]":
			setup = starlark.ListAllocBytes(1)
		case "[h, h, {'k': h}]":
			setup = starlark.ListAllocBytes(3) + starlark.DictAllocBytes(1)
		}
		// and 24 bytes a host value, the slice header it is kept in till the second pass
		want := setup + uint64(len(out)) + 24*uint64(hosts)
		if c.wrap == "[h, h, {'k': h}]" {
			want += 2 * 64 // the items of the dict, made by each of the two passes
		}
		if got := th.AllocatedBytes(); got != want {
			t.Errorf("%s: charged %d, want %d (setup %d, output %d)", c.name, got, want, setup, len(out))
		}
	}
}
