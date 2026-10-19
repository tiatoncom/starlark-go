package json_test

// Group G4b (round 2, M3): the growth of the buffer of encoding/json when it
// unquotes a string (a buffer of the body and 8 bytes, and when the result
// reaches its capacity less 8 bytes a new one of (cap+4)*2) is charged so that
// what is allocated is never more than what is charged, for every length.

import (
	"runtime"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// g4bDecode returns what json.decode of raw charged to the thread and what the
// runtime allocated for it.
func g4bDecode(t *testing.T, raw string) (charged, allocated uint64) {
	t.Helper()
	th := &starlark.Thread{Name: "g4b"}
	th.SetMaxAllocBytes(1 << 40)
	args := starlark.Tuple{starlark.String(raw)}
	fn := json.Module.Members["decode"]
	// (a first call allocates the stack of the thread: not the decoder's)
	if _, err := starlark.Call(th, fn, starlark.Tuple{starlark.String("1")}, nil); err != nil {
		t.Fatal(err)
	}
	// (the least of 3 runs: another goroutine, a collection, may allocate in between)
	allocated = ^uint64(0)
	for i := 0; i < 3; i++ {
		var m1, m2 runtime.MemStats
		runtime.ReadMemStats(&m1)
		a1 := th.AllocatedBytes()
		if _, err := starlark.Call(th, fn, args, nil); err != nil {
			t.Fatalf("decode(%.30q): %v", raw, err)
		}
		a2 := th.AllocatedBytes()
		runtime.ReadMemStats(&m2)
		charged = a2 - a1
		allocated = min(allocated, m2.TotalAlloc-m1.TotalAlloc)
	}
	return charged, allocated
}

// Every length of the body from 1 to 4096, for results shorter than the raw
// string, as long, and up to three times longer, and the lengths where the
// result is within a few bytes of the capacity of a buffer (the edge of the
// doubling: the result is as long as the raw string, or as the buffer that
// the first growth makes, less 8).
func TestG4b_DecodeUnquoteGrowthIsChargedForEveryLength(t *testing.T) {
	if raceOn {
		t.Skip("the race detector inflates the allocations")
	}
	const bs = "\\"
	type kase struct{ name, body string }
	var bad []string
	check := func(c kase) {
		raw := `"` + c.body + `"`
		charged, allocated := g4bDecode(t, raw)
		if allocated > charged {
			bad = append(bad, c.name)
			if len(bad) <= 12 {
				t.Errorf("%s: body of %d bytes: allocated %d, charged %d", c.name, len(c.body), allocated, charged)
			}
		}
	}
	for m := 1; m <= 4096; m++ {
		// invalid bytes: j of them, then plain; the result is m + 2j
		for _, j := range []int{m / 4, m / 2, 3 * m / 4, m} {
			if j == 0 {
				continue
			}
			check(kase{"invalid", strings.Repeat("\xff", j) + strings.Repeat("a", m-j)})
		}
		// escapes only: the result is shorter
		if m%2 == 0 {
			check(kase{"escapes", strings.Repeat(bs+"n", m/2)})
		}
		// non-ASCII: the result is as long as the raw
		if m%2 == 0 {
			check(kase{"non-ASCII", strings.Repeat("é", m/2)})
		}
	}
	// the result d bytes over the body: j invalid bytes (3 each) and 2j-d
	// escapes (1 byte for 2): body j + 2(2j-d) = 5j-2d, result 3j + 2j-d = 5j-d
	for j := 1; 5*j <= 4096; j++ {
		for d := 0; d <= 8 && 2*j-d >= 0; d++ {
			check(kase{"edge", strings.Repeat("\xff", j) + strings.Repeat(bs+"n", 2*j-d)})
		}
	}
	if len(bad) > 0 {
		t.Errorf("%d lengths allocate more than they are charged", len(bad))
	}
}
