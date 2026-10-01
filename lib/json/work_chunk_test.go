package json

import (
	"testing"

	"go.starlark.net/starlark"
)

// spy is an iterable that records the work of the thread at each element that
// it gives to json.encode.
type spy struct {
	th   *starlark.Thread
	n    int
	seen []uint64
}

func (s *spy) String() string             { return "spy" }
func (s *spy) Type() string               { return "spy" }
func (s *spy) Freeze()                    {}
func (s *spy) Truth() starlark.Bool       { return true }
func (s *spy) Hash() (uint32, error)      { return 0, nil }
func (s *spy) Iterate() starlark.Iterator { return &spyIter{s: s} }

type spyIter struct {
	s *spy
	i int
}

func (it *spyIter) Next(p *starlark.Value) bool {
	if it.i >= it.s.n {
		return false
	}
	it.s.seen = append(it.s.seen, it.s.th.Work())
	*p = starlark.MakeInt(it.i)
	it.i++
	return true
}
func (it *spyIter) Done() {}

// The encoder charges its work a chunk at a time (flushWork units), so that the
// work of the thread seen from inside the call grows in steps of a chunk: not
// at every element (a call per unit), and not once at the end (a long call that
// cannot be stopped).
func TestJSON_TheWorkIsChargedInChunks(t *testing.T) {
	th := &starlark.Thread{}
	s := &spy{th: th, n: 20000}
	if _, err := starlark.Call(th, Module.Members["encode"], starlark.Tuple{s}, nil); err != nil {
		t.Fatal(err)
	}
	var jumps []uint64
	for i := 1; i < len(s.seen); i++ {
		if d := s.seen[i] - s.seen[i-1]; d != 0 {
			jumps = append(jumps, d)
		}
	}
	if len(jumps) < 5 {
		t.Fatalf("the work changed %d times in %d elements: not charged as it goes", len(jumps), s.n)
	}
	jumps = jumps[:len(jumps)-1] // the last is the output, which is charged at the end
	for i, d := range jumps {
		// each element is an integer: a unit (and a few more) each
		if d < 1024 || d > 1024+16 {
			t.Errorf("jump %d of the work is %d units, want a chunk of %d (and at most 16 over)", i, d, flushWork)
		}
	}
}
