package starlark

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// cmpIntFloat is the comparison by rational numbers, which it replaced, for
// integers of every size and floats of every kind.
func TestCompare_IntFloatIsExact(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	randInt := func() Int {
		switch r.Intn(6) {
		case 0:
			return MakeInt(r.Intn(100) - 50)
		case 1:
			return MakeInt64(r.Int63n(1<<54) - 1<<53)
		case 2:
			return MakeInt64(int64(r.Uint64()))
		case 3:
			// near a power of two
			b := new(big.Int).Lsh(big.NewInt(1), uint(r.Intn(1100)))
			return MakeBigInt(b.Add(b, big.NewInt(int64(r.Intn(5)-2))))
		case 4:
			x := new(big.Int).Rand(r, new(big.Int).Lsh(big.NewInt(1), uint(r.Intn(2000)+1)))
			if r.Intn(2) == 0 {
				x.Neg(x)
			}
			return MakeBigInt(x)
		default:
			return MakeInt64(r.Int63() >> uint(r.Intn(63)))
		}
	}
	randFloat := func(x Int) float64 {
		switch r.Intn(8) {
		case 0:
			f, _ := new(big.Float).SetInt(x.BigInt()).Float64() // equal, if it fits
			if math.IsInf(f, 0) {
				return 1e300
			}
			return f
		case 1:
			f, _ := new(big.Float).SetInt(x.BigInt()).Float64()
			if math.IsInf(f, 0) {
				return -1e300
			}
			return math.Nextafter(f, math.Inf(1))
		case 2:
			f, _ := new(big.Float).SetInt(x.BigInt()).Float64()
			if math.IsInf(f, 0) {
				return 1e308
			}
			return math.Nextafter(f, math.Inf(-1))
		case 3:
			return r.NormFloat64() * 100
		case 4:
			return math.Trunc(r.NormFloat64()*1e6) + 0.5
		case 5:
			return r.NormFloat64() * math.Pow(10, float64(r.Intn(600)-300))
		case 6:
			return 0
		default:
			return float64(r.Int63()) * float64(1-2*r.Intn(2))
		}
	}
	for i := 0; i < 200000; i++ {
		x := randInt()
		f := randFloat(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		want := x.rational().Cmp(Float(f).rational())
		if got := cmpIntFloat(x, f); got != want {
			t.Fatalf("cmpIntFloat(%v, %v) = %d, want %d", x, f, got, want)
		}
	}
}

// The comparison of a list of integers with a float makes no rational number:
// no allocation per element.
func TestCompare_IntFloatDoesNotAllocate(t *testing.T) {
	big := MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 100000))
	n := testing.AllocsPerRun(100, func() {
		for i := 0; i < 100; i++ {
			cmpIntFloat(MakeInt(i), 1.5)
			cmpIntFloat(big, 1.5)
		}
	})
	if n > 0 {
		t.Errorf("%v allocations for 300 comparisons", n)
	}
}
