// Package sizes is the arithmetic of sizes that never wraps: the sum and the
// product of lengths, counts and byte counts, which a script can make as large
// as its data allows (a range of 2**63-1 elements, a needle of a gigabyte on a
// platform of 32-bit ints), saturate at the largest value of the type instead
// of wrapping round to a small or negative one that every check lets through.
// A saturated size is then refused like any size that is too large.
//
// It is the one implementation: starlark's addSizes, satAdd and satMul and the
// size sums of the syntax and json packages use it.
package sizes

import (
	"math"
	"math/bits"
)

// Add returns a+b, saturated at math.MaxInt (and at math.MinInt for negative
// operands): it never wraps.
func Add(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}

// Sum returns the sum of its arguments, saturated as Add is.
func Sum(a int, rest ...int) int {
	for _, b := range rest {
		a = Add(a, b)
	}
	return a
}

// Mul returns a*b, saturated at math.MaxInt (math.MinInt if the product is
// negative and does not fit): it never wraps.
func Mul(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	// the magnitudes in uint (the magnitude of MinInt is 1 << (bits-1))
	ua, ub := uint(a), uint(b)
	if a < 0 {
		ua = -ua
	}
	if b < 0 {
		ub = -ub
	}
	hi, lo := bits.Mul(ua, ub)
	if neg {
		if hi != 0 || lo > uint(math.MaxInt)+1 {
			return math.MinInt
		}
		return int(-lo)
	}
	if hi != 0 || lo > uint(math.MaxInt) {
		return math.MaxInt
	}
	return int(lo)
}

// AddU64 returns a+b, saturated at math.MaxUint64.
func AddU64(a, b uint64) uint64 {
	s, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return math.MaxUint64
	}
	return s
}

// MulU64 returns a*b, saturated at math.MaxUint64.
func MulU64(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return math.MaxUint64
	}
	return lo
}
