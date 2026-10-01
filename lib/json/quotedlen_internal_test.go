package json

import (
	stdjson "encoding/json"
	"strconv"
	"testing"
)

// The sizes of the quoted forms that encode computes before it writes are those
// of the encoders it uses.
func TestQuotedLenMatchesTheEncoders(t *testing.T) {
	alphabet := []string{"a", "Z", " ", "\"", "\\", "\n", "\t", "\b", "\f", "\r", "\x00", "\x1f", "\x7f", "<", ">", "&", "\xff", "\xc3", "é", "世", " ", " ", "\U0001F600", "�"}
	seed := uint32(1) // fixed: deterministic
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for i := 0; i < 20000; i++ {
		s := ""
		for j := next(10); j > 0; j-- {
			s += alphabet[next(len(alphabet))]
		}
		data, _ := stdjson.Marshal(s)
		if got, want := quotedLen(s), len(data); got != want {
			t.Fatalf("quotedLen(%q) = %d, json.Marshal gives %d", s, got, want)
		}
		if isPrintableASCII(s) {
			if got, want := quotedLenASCII(s), len(strconv.AppendQuote(nil, s)); got != want {
				t.Fatalf("quotedLenASCII(%q) = %d, want %d", s, got, want)
			}
		}
	}
}
