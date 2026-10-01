package starlark

// Calibration of the formulas of alloc.go against the memory the real
// structures retain, and the measured bound on the growth that is not charged.
// The measurements are of the Go heap after a collection, so they allow for a
// tolerance; the assertions are one-sided where an error in one direction is
// harmless.

import (
	"runtime"
	"testing"

	"go.starlark.net/syntax"
)

func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

type measured struct{ retained, charged, steps float64 }

// measure runs setup, then op, calling hm() between them; retained is the
// growth of the live heap across op, charged the bytes charged by op.
func measure(t *testing.T, setup, op string) measured {
	t.Helper()
	var heaps, charged, steps []uint64
	th := &Thread{}
	hm := NewBuiltin("hm", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
		heaps = append(heaps, heapAfterGC())
		charged = append(charged, th.AllocatedBytes())
		steps = append(steps, th.Steps)
		return None, nil
	})
	opts := &syntax.FileOptions{GlobalReassign: true, Set: true, TopLevelControl: true}
	if _, err := ExecFileOptions(opts, th, "t.star", setup+"\nhm()\n"+op+"\nhm()\n", StringDict{"hm": hm}); err != nil {
		t.Fatalf("%s / %s: %v", setup, op, err)
	}
	return measured{
		retained: float64(int64(heaps[1]) - int64(heaps[0])),
		charged:  float64(charged[1] - charged[0]),
		steps:    float64(steps[1] - steps[0]),
	}
}

// The formula charges at least 0.8 of what the structure retains (it may charge
// more: the formulas are upper bounds, and garbage counts), and no more than
// 4 times (a formula that drifts far above the structure is a bug as well).
// y and y2 are integers of ~4000 bits (500 bytes, 63 words).
// z is an integer of 64000 bits (8000 bytes): a size class of its own.
const hugeIntSetup = "z = 1 << 500\nfor i in range(7): z = z * z\n"

const bigIntSetup = "y = 1 << 500\nfor i in range(3): y = y * y\ny = y >> 20\ny2 = y + 12345\n"

func TestAllocFormulaCalibration(t *testing.T) {
	for _, c := range []struct {
		name, setup, op string
		n               int // elements the op makes
	}{
		{"list(range)", "", "r = list(range(20000))", 20000},
		{"split", "s = 'a,' * 20000", "r = s.split(',')", 20001},
		{"splitlines", "s = 'a\\n' * 20000", "r = s.splitlines()", 20000},
		{"list(s.elems())", "s = 'a' * 20000", "r = list(s.elems())", 20000},
		{"list(s.codepoints())", "s = 'a' * 20000", "r = list(s.codepoints())", 20000},
		{"dict.items", "d = {}\nfor i in range(20000): d[i] = i", "r = d.items()", 20000},
		{"dict.keys", "d = {}\nfor i in range(20000): d[i] = i", "r = d.keys()", 20000},
		{"zip", "x = list(range(20000))", "r = zip(x, x)", 20000},
		{"enumerate", "x = list(range(20000))", "r = enumerate(x)", 20000},
		{"sorted", "x = list(range(20000))", "r = sorted(x)", 20000},
		{"[{} ...]", "", "r = [{} for i in range(2000)]", 2000},
		{"[set() ...]", "", "r = [set() for i in range(2000)]", 2000},
		{"[[] ...]", "", "r = [[] for i in range(20000)]", 20000},
		{"[(i, i) ...]", "", "r = [(i, i) for i in range(20000)]", 20000},
		{"[lambda ...]", "", "r = [(lambda: i) for i in range(20000)]", 20000},
		{"dict with int keys", "", "r = {}\nfor i in range(20000): r[i] = i", 20000},
		{"dict with 1000 int keys", "", "r = {}\nfor i in range(1000): r[i] = i", 1000},
		{"dict with str keys", "x = [str(i) for i in range(20000)]", "r = {}\nfor k in x: r[k] = 1", 20000},
		{"dict with tuple keys", "x = [(i, i) for i in range(20000)]", "r = {}\nfor k in x: r[k] = 1", 20000},
		{"set of ints", "", "r = set()\nfor i in range(20000): r.add(i)", 20000},
		{"set(range)", "", "r = set(range(20000))", 20000},
		{"dict(x)", "x = [(i, i) for i in range(20000)]", "r = dict(x)", 20000},
		{"ints of 33 bits", "", "r = [(1 << 32) + i for i in range(20000)]", 20000},
		{"[y + i], y of 4000 bits", bigIntSetup, "r = [y + i for i in range(2000)]", 2000},
		{"[y - i], y of 4000 bits", bigIntSetup, "r = [y - i for i in range(2000)]", 2000},
		{"[y * i], y of 4000 bits", bigIntSetup, "r = [y * (i + 5) for i in range(2000)]", 2000},
		{"[y << 1], y of 4000 bits", bigIntSetup, "r = [y << (i % 64) for i in range(2000)]", 2000},
		{"[y >> 1], y of 4000 bits", bigIntSetup, "r = [y >> (i % 64) for i in range(2000)]", 2000},
		{"[y & y2]", bigIntSetup, "r = [y & (y2 + i) for i in range(2000)]", 2000},
		{"[y | i]", bigIntSetup, "r = [y | i for i in range(2000)]", 2000},
		{"[y ^ i]", bigIntSetup, "r = [y ^ i for i in range(2000)]", 2000},
		{"[~y]", bigIntSetup, "r = [~y for i in range(2000)]", 2000},
		{"[-y]", bigIntSetup, "r = [-y for i in range(2000)]", 2000},
		{"[abs(-y)]", bigIntSetup, "r = [abs(-y) for i in range(2000)]", 2000},
		{"[y // 3]", bigIntSetup, "r = [y // (i + 3) for i in range(2000)]", 2000},
		{"[y2 % y]", bigIntSetup, "r = [(y * y2) % (y + i) for i in range(2000)]", 2000},
		{"[z + i], z of 64000 bits", hugeIntSetup, "r = [z + i for i in range(300)]", 300},
		{"[z >> 100], z of 64000 bits", hugeIntSetup, "r = [z >> (i % 200) for i in range(300)]", 300},
		{"[int(float)]", "", "r = [int(1e300 * (i + 1)) for i in range(2000)]", 2000},
		{"[int(str)]", "s = '9' * 1000", "r = [int(s) + i for i in range(2000)]", 2000},
	} {
		m := measure(t, c.setup, c.op)
		ratio := m.charged / m.retained
		t.Logf("%-24s retained %8.0f B  charged %8.0f B  charged/retained %.2f", c.name, m.retained, m.charged, ratio)
		if ratio < 0.8 {
			t.Errorf("%s: charged %.0f bytes for %.0f retained (%.2f of it, want at least 0.8)", c.name, m.charged, m.retained, ratio)
		}
		if ratio > 4 {
			t.Errorf("%s: charged %.0f bytes for %.0f retained (%.1fx, want at most 4x)", c.name, m.charged, m.retained, ratio)
		}
	}
}

// A program that runs S steps without a charged operation retains at most
// allocUnchargedBytesPerStep * S bytes beyond what it was charged: the claim
// of alloc.go that bounds what the step limit leaves unaccounted for. The
// worst measured is a list of big integers (~5 bytes a step); the constant
// is 8.
func TestAllocUnchargedGrowthPerStep(t *testing.T) {
	var worst float64
	for _, c := range []struct{ name, setup, op string }{
		{"append(i)", "r = []", "for i in range(100000): r.append(i)"},
		{"append(big int)", "r = []", "for i in range(100000): r.append(i << 40)"},
		{"append(str(i))", "r = []", "for i in range(100000): r.append(str(i))"},
		{"append(float)", "r = []", "for i in range(100000): r.append(i * 1.5)"},
		{"append((i,))", "r = []", "for i in range(100000): r.append((i,))"},
		{"append({})", "r = []", "for i in range(20000): r.append({})"},
		{"append([])", "r = []", "for i in range(100000): r.append([])"},
		{"append(lambda)", "r = []", "for i in range(100000): r.append(lambda: i)"},
		{"d[i] = i", "r = {}", "for i in range(100000): r[i] = i"},
		{"d[str] = i", "r = {}", "for i in range(100000): r['k%d' % i] = i"},
		{"s.add(i)", "r = set()", "for i in range(100000): r.add(i)"},
		{"comprehension", "", "r = [i for i in range(100000)]"},
		{"r += [i]", "r = []", "for i in range(100000): r += [i]"},
		{"insert(0, i)", "r = []", "for i in range(3000): r.insert(0, i)"},
		{"tuple of ints", "r = []", "for i in range(100000): r.append(i); a, b = i, i"},
		{"append(i + y), 33 bit", "r = []\ny = 1 << 32", "for i in range(100000): r.append(y + i)"},
		{"append(y + i), 4000 bits", bigIntSetup + "r = []", "for i in range(20000): r.append(y + i)"},
		{"append(y - i)", bigIntSetup + "r = []", "for i in range(20000): r.append(y - i)"},
		{"append(y << 1)", bigIntSetup + "r = []", "for i in range(20000): r.append(y << 1)"},
		{"append(y >> 1)", bigIntSetup + "r = []", "for i in range(20000): r.append(y >> 1)"},
		{"append(y & y2)", bigIntSetup + "r = []", "for i in range(20000): r.append(y & y2)"},
		{"append(~y)", bigIntSetup + "r = []", "for i in range(20000): r.append(~y)"},
		{"append(-y)", bigIntSetup + "r = []", "for i in range(20000): r.append(-y)"},
		{"append(y // 3)", bigIntSetup + "r = []", "for i in range(20000): r.append(y // 3)"},
		{"append(y * 3)", bigIntSetup + "r = []", "for i in range(20000): r.append(y * 3)"},
		{"append(int(float))", "r = []", "for i in range(20000): r.append(int(1e300))"},
		{"append(z + i), 64000 bits", hugeIntSetup + "r = []", "for i in range(300): r.append(z + i)"},
		{"append(z >> 1), 64000 bits", hugeIntSetup + "r = []", "for i in range(300): r.append(z >> 1)"},
		{"+= with y", bigIntSetup + "r = 0", "for i in range(20000): r += y"},
	} {
		m := measure(t, c.setup, c.op)
		uncharged := (m.retained - m.charged) / m.steps
		t.Logf("%-18s %8.0f steps  retained %.2f B/step  charged %.2f B/step  uncharged %.2f B/step", c.name, m.steps, m.retained/m.steps, m.charged/m.steps, uncharged)
		if uncharged > worst {
			worst = uncharged
		}
		if uncharged > allocUnchargedBytesPerStep {
			t.Errorf("%s: %.2f uncharged bytes a step, more than allocUnchargedBytesPerStep = %d", c.name, uncharged, allocUnchargedBytesPerStep)
		}
	}
	t.Logf("worst uncharged growth: %.2f B/step; at 10M steps: %.0f MiB", worst, worst*1e7/(1<<20))
	if allocUnchargedBytesPerStep*10_000_000 > 100<<20 {
		t.Errorf("allocUnchargedBytesPerStep = %d: over 100 MiB unaccounted for at the engine's 10M steps", allocUnchargedBytesPerStep)
	}
}
