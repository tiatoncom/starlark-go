package sizes

import (
	"math"
	"math/big"
	"testing"
)

// The edges of Add and Mul, against big.Int: the result is the exact value
// clamped to [MinInt, MaxInt], never a wrapped one. (The same code is the
// arithmetic of a platform of 32-bit ints: nothing in it depends on the width,
// and it is checked at the widths of int here through the clamp of math.MaxInt.)
func TestAddMulSaturate(t *testing.T) {
	vals := []int{0, 1, -1, 2, -2, 3, 15, 16, 1 << 20, math.MaxInt32, math.MinInt32,
		math.MaxInt / 16, math.MaxInt/16 + 1, math.MaxInt / 8, math.MaxInt/8 + 1, math.MaxInt / 2, math.MaxInt/2 + 1,
		math.MaxInt - 1, math.MaxInt, math.MinInt, math.MinInt + 1, math.MinInt / 2}
	clamp := func(x *big.Int) int {
		if x.Cmp(big.NewInt(math.MaxInt)) > 0 {
			return math.MaxInt
		}
		if x.Cmp(big.NewInt(math.MinInt)) < 0 {
			return math.MinInt
		}
		return int(x.Int64())
	}
	for _, a := range vals {
		for _, b := range vals {
			ba, bb := big.NewInt(int64(a)), big.NewInt(int64(b))
			if got, want := Add(a, b), clamp(new(big.Int).Add(ba, bb)); got != want {
				t.Errorf("Add(%d, %d) = %d, want %d", a, b, got, want)
			}
			if got, want := Mul(a, b), clamp(new(big.Int).Mul(ba, bb)); got != want {
				t.Errorf("Mul(%d, %d) = %d, want %d", a, b, got, want)
			}
		}
	}
}

// 16*m and 2*m of the search, and done+step, with the sizes of a haystack of
// a gigabyte on 32 bits (MaxInt32): the values that wrapped negative.
func TestTheSizesThatWrappedOn32Bits(t *testing.T) {
	// (on a platform of 32-bit ints MaxInt is MaxInt32, and 16 * 1<<28 does not
	// fit: Mul(16, MaxInt/16+1) is the same case at any width)
	if got := Mul(16, math.MaxInt/16+1); got != math.MaxInt {
		t.Errorf("Mul(16, MaxInt/16+1) = %d, want MaxInt", got)
	}
	if got := Add(math.MaxInt-10, 100); got != math.MaxInt {
		t.Errorf("Add(MaxInt-10, 100) = %d, want MaxInt", got)
	}
	if got := Sum(1, 2, 3); got != 6 {
		t.Errorf("Sum(1, 2, 3) = %d", got)
	}
	if got := Sum(math.MaxInt, 1, 1); got != math.MaxInt {
		t.Errorf("Sum(MaxInt, 1, 1) = %d", got)
	}
}

func TestU64(t *testing.T) {
	if got := AddU64(math.MaxUint64, 1); got != math.MaxUint64 {
		t.Errorf("AddU64 = %d", got)
	}
	if got := AddU64(1, 2); got != 3 {
		t.Errorf("AddU64(1, 2) = %d", got)
	}
	if got := MulU64(1<<32, 1<<32); got != math.MaxUint64 {
		t.Errorf("MulU64 = %d", got)
	}
	if got := MulU64(1<<31, 4); got != 1<<33 {
		t.Errorf("MulU64(1<<31, 4) = %d", got)
	}
}
