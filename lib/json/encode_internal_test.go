package json

import (
	stdjson "encoding/json"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func randJSONString(r *rand.Rand, n int) string {
	pieces := []string{"a", "b", " ", "\"", "\\", "\n", "\t", "\r", "\b", "\f", "\x00", "\x1f", "<", ">", "&", "é", "世", "\U0001F600", " ", " ", "\xff", "\xc3", "\xe2\x80"}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(pieces[r.Intn(len(pieces))])
	}
	return b.String()
}

// appendJSONString is what encoding/json writes for a string, and quotedLen is
// its length: the two passes of encode write and count the same bytes.
func TestEncode_StringsAreTheStandardLibrarys(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	for i := 0; i < 20000; i++ {
		s := randJSONString(r, r.Intn(300))
		want, _ := stdjson.Marshal(s)
		got := append(append([]byte{'"'}, appendJSONString(nil, s)...), '"')
		if string(got) != string(want) {
			t.Fatalf("appendJSONString(%q) = %q, want %q", s, got, want)
		}
		if quotedLen(s) != len(want) {
			t.Fatalf("quotedLen(%q) = %d, want %d", s, quotedLen(s), len(want))
		}
		var o outBuf
		o.writeQuoted(s, false)
		if o.b.String() != string(want) {
			t.Fatalf("writeQuoted(%q) = %q, want %q", s, o.b.String(), want)
		}
	}
}

// A large output, made in two passes, is what encoding/json makes (with the
// keys sorted): the same bytes, whatever is in the strings.
func TestEncode_LargeOutputIsTheStandardLibrarys(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	for iter := 0; iter < 20; iter++ {
		var goV []any
		l := starlark.NewList(nil)
		for i := 0; i < 400; i++ {
			s := "\u00e9" + randJSONString(r, r.Intn(60)) // (a string with a character that is not ASCII: encoding/json's quoting)
			d := new(starlark.Dict)
			d.SetKey(starlark.String("k"+s), starlark.String(s))
			d.SetKey(starlark.String("n"), starlark.MakeInt(i-200))
			d.SetKey(starlark.String("f"), starlark.Float(float64(i)*1.5))
			l.Append(starlark.Tuple{d, starlark.True, starlark.None})
			goV = append(goV, []any{map[string]any{"k" + s: s, "n": i - 200, "f": float64(i) * 1.5}, true, nil})
		}
		th := &starlark.Thread{}
		v, err := starlark.Call(th, Module.Members["encode"], starlark.Tuple{l}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := stdjson.Marshal(goV)
		got := string(v.(starlark.String))
		// (encoding/json writes 1.5 as 1.5 and 3 as 3: Starlark's floats are 3.0)
		if len(got) < 4096 {
			t.Fatalf("the output is %d bytes: one pass", len(got))
		}
		if strings.Count(got, "\"f\":") != strings.Count(string(want), "\"f\":") {
			t.Fatalf("the number of fields differs")
		}
		// the strings and the keys are the same: compare with the floats normalised
		norm := func(s string) string {
			return strings.NewReplacer(".0,", ",", ".0}", "}").Replace(s)
		}
		if g, w := norm(got), norm(string(want)); g != w {
			k := 0
			for k < len(g) && k < len(w) && g[k] == w[k] {
				k++
			}
			from := max(k-30, 0)
			t.Fatalf("iteration %d: the output differs from encoding/json at byte %d:\n got %.80q\nwant %.80q", iter, k, g[from:], w[from:])
		}
	}
}

// The two passes agree on ints (small, big), floats and nested containers.
func TestEncode_TwoPassesCountTheSameBytes(t *testing.T) {
	big := starlark.MakeBigInt(bigPow(200))
	l := starlark.NewList(nil)
	for i := 0; i < 3000; i++ {
		l.Append(starlark.Tuple{starlark.MakeInt(i * 7919), starlark.Float(float64(i) / 7), big, starlark.String("é" + strings.Repeat("x", i%50)), starlark.NewList([]starlark.Value{starlark.None})})
	}
	th := &starlark.Thread{}
	v, err := starlark.Call(th, Module.Members["encode"], starlark.Tuple{l}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.(starlark.String)) < 100000 {
		t.Fatalf("%d bytes", len(v.(starlark.String)))
	}
}

func bigPow(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(3), n) }

// DEL: as in v0.2.0, a string of printable ASCII is quoted as strconv does
// (\x7f, which is not JSON), and one with other characters as encoding/json does.
func TestEncode_DELAsInV020(t *testing.T) {
	th := &starlark.Thread{}
	for in, want := range map[string]string{"a\x7fb": `"a\x7fb"`, "\u00e9\x7f": "\"\u00e9\x7f\""} {
		v, err := starlark.Call(th, Module.Members["encode"], starlark.Tuple{starlark.String(in)}, nil)
		if err != nil || string(v.(starlark.String)) != want {
			t.Errorf("encode(%q) = %v (%v), want %s", in, v, err, want)
		}
	}
}
