package json_test

// Group G4d (round 2): the cache of the results of MarshalJSON (D1, D2) and
// json.indent of an empty text (D3), each held against v0.2.3, whose code is
// quoted in the comments.

import (
	"bytes"
	stdjson "encoding/json"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// g4Fn is a host value whose MarshalJSON is fn.
type g4Fn struct{ fn func() ([]byte, error) }

func (h *g4Fn) String() string               { return "g4Fn" }
func (h *g4Fn) Type() string                 { return "g4Fn" }
func (h *g4Fn) Freeze()                      {}
func (h *g4Fn) Truth() starlark.Bool         { return true }
func (h *g4Fn) Hash() (uint32, error)        { return 0, nil }
func (h *g4Fn) MarshalJSON() ([]byte, error) { return h.fn() }

// D1: v0.2.3 writes the bytes of a host into the output as it gets them
// (buf.Write(data)), so a host that returns a view of one scratch buffer that
// it reuses gives each of its values. The cache of the second pass kept the
// slice itself: every value was then the last one written.
func TestG4d_HostBufferReusedByTheHost(t *testing.T) {
	scratch := make([]byte, 0, 6000)
	mk := func(tag byte) *g4Fn {
		return &g4Fn{func() ([]byte, error) {
			scratch = append(scratch[:0], '"')
			scratch = append(scratch, strings.Repeat(string(tag), 3000)...)
			scratch = append(scratch, '"')
			return scratch, nil
		}}
	}
	v := starlark.NewList([]starlark.Value{mk('a'), mk('b')})
	got, err := g4Call(&starlark.Thread{Name: "g4d"}, "encode", v)
	if err != nil {
		t.Fatal(err)
	}
	want := `["` + strings.Repeat("a", 3000) + `","` + strings.Repeat("b", 3000) + `"]` // (v0.2.3)
	if string(got.(starlark.String)) != want {
		s := string(got.(starlark.String))
		t.Errorf("the output differs from v0.2.3's: len %d, want %d; second value starts %.12q", len(s), len(want), s[min(len(s), 3005):])
	}
}

// The clone is charged: a host that returns a megabyte is refused by a budget
// of a quarter of it, before the clone is made.
func TestG4d_HostBytesAreChargedWhenCached(t *testing.T) {
	big := []byte(`"` + strings.Repeat("x", 1<<20) + `"`)
	th := &starlark.Thread{Name: "g4d"}
	th.SetMaxAllocBytes(1 << 20 / 4)
	_, err := g4Call(th, "encode", starlark.NewList([]starlark.Value{&g4Fn{func() ([]byte, error) { return big, nil }}}))
	if err == nil {
		t.Fatal("a megabyte from a host passed a budget of 256 KiB")
	}
}

// D2: a host that changes a container that was written (the first pass has
// written it) makes the second pass meet a value more than the first: an error
// (v0.2.3 has one pass and writes [[],"x..."]), not a panic.
func TestG4d_HostThatMutatesTheValueIsNotAPanic(t *testing.T) {
	L := starlark.NewList(nil)
	h2 := &g4Fn{func() ([]byte, error) { return []byte(`"two"`), nil }}
	done := false
	h1 := &g4Fn{func() ([]byte, error) {
		if !done {
			done = true
			L.Append(h2)
		}
		return []byte(`"` + strings.Repeat("x", 5000) + `"`), nil
	}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	got, err := g4Call(&starlark.Thread{Name: "g4d"}, "encode", starlark.NewList([]starlark.Value{L, h1}))
	if err != nil {
		if len(err.Error()) > 300 {
			t.Errorf("the text of the error is %d bytes", len(err.Error()))
		}
		return
	}
	// (or the output is one of a consistent traversal)
	if s := string(got.(starlark.String)); !strings.HasPrefix(s, "[[") {
		t.Errorf("output %.40q", s)
	}
}

// D3: v0.2.3's indent is json.Indent of the text, which refuses an empty one:
// "json.indent: unexpected end of JSON input". So is encode_indent of a host
// that writes nothing.
func TestG4d_IndentOfEmptyTextIsAnError(t *testing.T) {
	var sink bytes.Buffer
	stdErr := stdjson.Indent(&sink, []byte(""), "", "\t")
	if stdErr == nil {
		t.Fatal("encoding/json accepts an empty text")
	}
	for _, s := range []string{"", " "} {
		_, err := g4Call(&starlark.Thread{Name: "g4d"}, "indent", starlark.String(s))
		var want string
		if e := stdjson.Indent(&sink, []byte(s), "", "\t"); e != nil {
			want = "json.indent: " + e.Error()
		}
		if want == "" || err == nil || err.Error() != want {
			t.Errorf("indent(%q): got %v, want error %q", s, err, want)
		}
	}
	_, err := g4Call(&starlark.Thread{Name: "g4d"}, "encode_indent", &g4Fn{func() ([]byte, error) { return nil, nil }})
	if err == nil || err.Error() != "json.encode_indent: "+stdErr.Error() {
		t.Errorf("encode_indent of empty bytes: got %v", err)
	}
}
