package starlark

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"go.starlark.net/syntax"
)

// The string forms that the built-ins make (str, repr, print, %, format) are
// built in two passes when they are large, so that a form of n bytes allocates
// n bytes: one pass that only counts, charges the work and enforces the limit
// (it allocates nothing), and one that writes into a buffer of the exact size.
// A strings.Builder that grows by append allocates 5 times the size of what it
// holds in total, and as much as twice at its peak; the copy of a buffer into
// a string doubles it again. A small form (formProbe bytes or less) is made in
// the first pass, as it was.

// formProbe is the size up to which a form is made in one pass: the garbage of
// a form that outgrows it is at most 5 * formProbe bytes.
const formProbe = 4096

// A sink is where a form is written. It counts the bytes written; and keeps
// them, unless it is counting only (a form that outgrew formProbe, whose bytes
// are written by the second pass). It is an io.Writer.
type sink struct {
	b        strings.Builder
	ext      *strings.Builder // if not nil, the bytes go there (and n starts at its length)
	n        int              // bytes written or counted
	probe    int              // if > 0, a first pass: count only once n would exceed it
	counting bool
	markEnd  int // n after the last overflow mark (0: none)
}

func newSinkOn(ext *strings.Builder) *sink {
	return &sink{ext: ext, n: ext.Len()}
}

func (s *sink) target() *strings.Builder {
	if s.ext != nil {
		return s.ext
	}
	return &s.b
}

func (s *sink) Len() int { return s.n }

// room makes the sink count only, if len more bytes outgrow the probe.
func (s *sink) room(n int) {
	if s.probe > 0 && !s.counting && s.n+n > s.probe {
		s.counting = true
		s.b = strings.Builder{} // the garbage is at most 5 * probe
	}
}

func (s *sink) WriteString(str string) (int, error) {
	s.room(len(str))
	s.n += len(str)
	if !s.counting {
		s.target().WriteString(str)
	}
	return len(str), nil
}

func (s *sink) WriteByte(c byte) error {
	s.room(1)
	s.n++
	if !s.counting {
		s.target().WriteByte(c)
	}
	return nil
}

func (s *sink) Write(p []byte) (int, error) {
	s.room(len(p))
	s.n += len(p)
	if !s.counting {
		s.target().Write(p)
	}
	return len(p), nil
}

// String returns what was written; the sink must not be counting.
func (s *sink) String() string { return s.target().String() }

// recentMark reports whether the last overflow mark ends within the last 64
// bytes (a form that went on after reaching the limit marks it once).
func (s *sink) recentMark(mark string) bool {
	return s.markEnd > 0 && s.n-s.markEnd+len(mark) <= 64
}

func (s *sink) writeMark(mark string) {
	s.WriteString(mark)
	s.markEnd = s.n
}

// buildForm makes a string by running f twice if it is large. f writes the
// form to out, charging its work to m (nil: none) and its thread's steps to
// th (nil: none); it must write the same bytes in each run. charge is called
// with the size of the form before it is allocated: its error is returned.
func (thread *Thread) buildForm(f func(th *Thread, out *sink, m *meter) error, charge func(n int) error) (string, error) {
	out := &sink{probe: formProbe}
	m := thread.meter()
	if err := f(thread, out, &m); err != nil {
		return "", err
	}
	if err := m.flush(); err != nil {
		return "", err
	}
	n := out.n
	if charge != nil {
		if err := charge(n); err != nil {
			return "", err
		}
	}
	if !out.counting {
		return out.String(), nil
	}
	// The second pass: the work was charged by the first.
	w := &sink{}
	w.b.Grow(n)
	if err := f(nil, w, nil); err != nil {
		return "", err
	}
	if w.n != n {
		return "", fmt.Errorf("internal error: a form of %d bytes was counted as %d", w.n, n)
	}
	return w.String(), nil
}

func (s *sink) writeRune(r rune) {
	var tmp [utf8.UTFMax]byte
	s.Write(tmp[:utf8.EncodeRune(tmp[:], r)])
}

// writeQuoted writes syntax.Quote(str, bytes), in pieces, so that no copy of
// the quoted string is made.
func (s *sink) writeQuoted(str string, bytes bool) {
	if bytes {
		s.WriteByte('b')
	}
	s.WriteByte('"')
	var tmp [256]byte // a piece of 64 bytes is quoted to at most 4 * 64
	for len(str) > 0 {
		n := min(len(str), 64)
		for k := n; k > 0 && k < len(str) && !utf8.RuneStart(str[k]); k-- {
			n = k - 1 // not in the middle of a rune
		}
		if n == 0 {
			n = min(len(str), 64)
		}
		s.Write(syntax.AppendQuoted(tmp[:0], str[:n]))
		str = str[n:]
	}
	s.WriteByte('"')
}
