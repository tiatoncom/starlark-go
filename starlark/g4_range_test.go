package starlark

import (
	"math"
	"math/big"
	"testing"
)

// v023RangeSlice is rangeValue.Slice of v0.2.3 (library.go), with the
// wrapping arithmetic of Go ints, and its rangeLen.
func v023RangeSlice(r rangeValue, start, end, step int) rangeValue {
	newStart := r.start + r.step*start
	newStop := r.start + r.step*end
	newStep := r.step * step
	n := 0
	switch {
	case newStep > 0:
		if newStop > newStart {
			n = (newStop-1-newStart)/newStep + 1
		}
	case newStep < 0:
		if newStart > newStop {
			n = (newStart-1-newStop)/-newStep + 1
		}
	}
	return rangeValue{start: newStart, stop: newStop, step: newStep, len: n}
}

// D4 (round 2): rangeValue.slice built the start, stop and step of the result
// from products r.step*index that can overflow although the result is fine, or
// is the empty range: range(-(1<<62), 1<<62, 1<<61)[2147483647:-10:1] is an
// error and was the empty range in v0.2.3. The result of a slice is that of
// v0.2.3 wherever v0.2.3 is right (the exact start, stop and step fit an int:
// then the same fields), and an empty range where it is empty, whatever the
// wrapping intermediates; a slice whose exact start, stop or step does not fit
// an int and that is not empty is still an error (v0.2.3 wraps it round to
// another range).
func TestG4RangeSliceEqualsV023(t *testing.T) {
	bi := func(x int) *big.Int { return new(big.Int).SetInt64(int64(x)) }
	edge := []int{0, 1, -1, 2, -2, 3, 7, -7, 1 << 31, -(1 << 31), 1 << 40, -(1 << 40), 1 << 61, -(1 << 61), 1 << 62, -(1 << 62), math.MaxInt, math.MinInt, math.MaxInt - 1, math.MinInt + 1}
	steps := []int{1, -1, 2, -2, 3, 1 << 31, -(1 << 31), 1 << 61, -(1 << 61), math.MaxInt, math.MinInt + 1}
	var cases, errs, empties int
	for _, rs := range edge {
		for _, re := range edge {
			for _, rst := range steps {
				n, ok := rangeLenChecked(rs, re, rst)
				if !ok {
					continue
				}
				r := rangeValue{start: rs, stop: re, step: rst, len: n}
				// the indices that the slice operation passes: clamped to the range
				idx := []int{0, 1, 2, 3, 4, n - 1, n, n + 1, 2147483647, -1}
				for _, st := range steps {
					for _, a := range idx {
						for _, b := range idx {
							if st > 0 && (a < 0 || b < 0 || a > n || b > n) {
								continue
							}
							if st < 0 && (a < -1 || b < -1 || a > n-1 || b > n-1) {
								continue
							}
							cases++
							exStart := new(big.Int).Add(bi(rs), new(big.Int).Mul(bi(rst), bi(a)))
							exStop := new(big.Int).Add(bi(rs), new(big.Int).Mul(bi(rst), bi(b)))
							exStep := new(big.Int).Mul(bi(rst), bi(st))
							fits := exStart.IsInt64() && exStop.IsInt64() && exStep.IsInt64()
							var empty bool
							if exStep.Sign() > 0 {
								empty = exStart.Cmp(exStop) >= 0
							} else {
								empty = exStart.Cmp(exStop) <= 0
							}
							got, err := r.slice(a, b, st)
							want := v023RangeSlice(r, a, b, st)
							switch {
							case fits:
								n, lok := rangeLenChecked(int(exStart.Int64()), int(exStop.Int64()), int(exStep.Int64()))
								if !lok {
									continue
								}
								exact := rangeValue{start: int(exStart.Int64()), stop: int(exStop.Int64()), step: int(exStep.Int64()), len: n}
								if err != nil || got != Value(exact) {
									t.Errorf("%v[%d:%d:%d]: got %v (err %v), want the exact %v", r, a, b, st, got, err, exact)
								}
								// (v0.2.3 itself wraps the length of a span of more than an int)
								if want.len == n && want != exact {
									t.Errorf("%v[%d:%d:%d]: v0.2.3 %v is not the exact %v", r, a, b, st, want, exact)
								}
							case empty:
								empties++
								if err != nil || got.(rangeValue).len != 0 {
									t.Errorf("%v[%d:%d:%d] is empty (v0.2.3: len %d): got %v, err %v", r, a, b, st, want.len, got, err)
								}
							default:
								errs++
								if err == nil {
									t.Errorf("%v[%d:%d:%d]: the exact start, stop or step does not fit and it is not empty: got %v, want an error", r, a, b, st, got)
								}
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d slices: %d empty with an overflow, %d errors", cases, empties, errs)
}

// The slice of the review, by the interpreter.
func TestG4RangeSliceOfTheReview(t *testing.T) {
	th := &Thread{}
	v, err := ExecFile(th, "d4.star", "r = range(-(1<<62), 1<<62, 1<<61)[2147483647:-10:1]\nn = len(r)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := v["n"]; n != MakeInt(0) {
		t.Errorf("len = %v, want 0", n)
	}
}
