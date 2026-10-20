package json_test

// Group G6 (the survivors of the full mutant run on 0b76469).

import (
	"math/big"
	"strings"
	"testing"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// struct-0254: json.decode charges a big number before it makes it, by an
// upper bound of its bits, len(digits)*3322/1000 + 2 (log2(10) < 3.322, and a
// number of d digits has up to floor(d*log2 10)+1 bits). The "+2" matters at a
// number of d nines whose bits are the first of a new word of 64 while the
// bound without it is at the word below: the charge would be less than the
// integer that is made. The test finds such lengths (there are some, below the
// limit of the digits) and holds the charge at or above the integer's.
func TestG6DecodeChargesAtLeastTheBigIntItMakes(t *testing.T) {
	charged := func(raw string) uint64 {
		th := &starlark.Thread{Name: "g6"}
		if _, err := starlark.Call(th, json.Module.Members["decode"], starlark.Tuple{starlark.String(raw)}, nil); err != nil {
			t.Fatal(err)
		}
		return th.AllocatedBytes()
	}
	// the part of the charge that does not depend on the digits
	base := charged(strings.Repeat("9", 20)) - starlark.BigIntAllocBytes(20*3322/1000+2)
	var tight []int
	for d := 20; d <= 4000; d++ {
		n := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)
		n.Sub(n, big.NewInt(1)) // d nines: the most bits of d digits
		if starlark.BigIntAllocBytes(d*3322/1000) < starlark.BigIntAllocBytes(n.BitLen()) {
			tight = append(tight, d)
		}
	}
	if len(tight) == 0 {
		t.Fatal("no length of digits where the bound without the +2 is below the integer: the test is vacuous")
	}
	for _, d := range tight {
		n := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)
		n.Sub(n, big.NewInt(1))
		if got, min := charged(strings.Repeat("9", d)), base+starlark.BigIntAllocBytes(n.BitLen()); got < min {
			t.Errorf("%d nines: charged %d bytes, less than the %d of the integer it makes (%d bits)", d, got, min, n.BitLen())
		}
	}
	t.Logf("%d lengths of digits are tight, the first %d", len(tight), tight[0])
}
