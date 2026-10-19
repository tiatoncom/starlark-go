package json

// Group G4b (M5): the sums of sizes saturate instead of wrapping: a string of
// a gigabyte of "\x00" quotes to 6 GB, which is a negative int on 32 bits. The
// sums are called near the largest int (as a platform of 32-bit ints reaches
// them with the data that it can hold).

import (
	"math"
	"testing"
)

func TestM5_QuotedLenSaturates(t *testing.T) {
	for _, c := range []struct {
		name string
		s    string
	}{
		{"control byte (6)", "\x00"},
		{"quote (2)", "\""},
		{"html (6)", "<"},
		{"invalid UTF-8 (6)", "\xff"},
		{"U+2028 (6)", " "},
		{"plain (1)", "a"},
		{"4 bytes (4)", "\U0001F600"},
	} {
		for _, from := range []int{math.MaxInt - 3, math.MaxInt - 1, math.MaxInt} {
			got := quotedLenFrom(from, c.s)
			if got < from {
				t.Errorf("%s: quotedLenFrom(MaxInt-%d) = %d: wrapped", c.name, math.MaxInt-from, got)
			}
		}
	}
	for _, from := range []int{math.MaxInt - 1, math.MaxInt} {
		for _, s := range []string{"\"", "\x7f", "\\"} {
			if got := quotedLenASCIIFrom(from, s); got < from {
				t.Errorf("quotedLenASCIIFrom(MaxInt-%d, %q) = %d: wrapped", math.MaxInt-from, s, got)
			}
		}
	}
	// the sums that are in range are what they were
	if got := quotedLenFrom(2, "a\x00\""); got != 2+1+6+2 {
		t.Errorf("quotedLenFrom(2, ...) = %d", got)
	}
}

func TestM5_OutBufCountSaturates(t *testing.T) {
	o := &outBuf{probe: 10, counting: true}
	o.n = math.MaxInt - 1
	o.skip(100)
	if o.n != math.MaxInt {
		t.Errorf("the count wrapped: %d", o.n)
	}
	// and the probe is passed by a count that would wrap
	o = &outBuf{probe: 10}
	o.n = math.MaxInt - 1
	o.WriteString("abc")
	if !o.counting || o.n != math.MaxInt {
		t.Errorf("counting %v, n %d", o.counting, o.n)
	}
}

func TestM5_UnquoteGrowthTerminatesAtTheLargestSizes(t *testing.T) {
	for _, m := range []int{math.MaxInt/2 - 5, math.MaxInt - 20, math.MaxInt - 1, math.MaxInt} {
		got := unquoteGrowthBytes(m, math.MaxInt)
		if got == 0 {
			t.Errorf("unquoteGrowthBytes(%d, MaxInt) = 0", m)
		}
	}
}
