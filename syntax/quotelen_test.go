package syntax

import (
	"math/rand"
	"testing"
)

func TestQuoteLenMatchesQuote(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) // fixed seed: deterministic
	alphabet := []string{"a", "Z", "0", " ", "\"", "\\", "\n", "\t", "\a", "\x00", "\x7f", "\xff", "\xc3", "é", "世", " ", "\U0001F600", "�", "\U0010FFFF", "­", "\x1b", "'"}
	for i := 0; i < 20000; i++ {
		n := rng.Intn(12)
		s := ""
		for j := 0; j < n; j++ {
			s += alphabet[rng.Intn(len(alphabet))]
		}
		for _, b := range []bool{false, true} {
			if got, want := QuoteLen(s, b), len(Quote(s, b)); got != want {
				t.Fatalf("QuoteLen(%q, %v) = %d, want %d", s, b, got, want)
			}
		}
	}
}
