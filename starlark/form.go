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

// A formJob is one of the string forms that are made in two passes when they
// are large: what it is (kind), and what it is made of. It is a struct with a
// method for each kind, not a function value, so that the sink and the meter of
// the first pass stay on the stack: a form that is a few bytes long allocates
// no more than its string.
type formJob struct {
	kind   int
	limit  int
	x      Value // str, repr: the value; interpolate: the argument
	args   Tuple // print, format: the arguments
	kwargs []Tuple
	sep    string // print: the separator
	format string // interpolate, format: the format
}

const (
	jobStr = iota
	jobRepr
	jobPrint
	jobInterpolate
	jobFormat
)

// run writes the form to out, charging its work to m (nil: none) and its steps
// to th (nil: none). It writes the same bytes in each of the two runs.
func (j *formJob) run(th *Thread, out *sink, m *meter) error {
	switch j.kind {
	case jobStr:
		if code, werr := writeValueMeter(out, j.x, j.limit, m); code != writeOK {
			return th.formErr(code, werr, j.limit, "str", "str: value's string form exceeds the size limit")
		}
	case jobRepr:
		switch code, werr := writeValueMeter(out, j.x, j.limit, m); {
		case werr != nil || code == writeDeep || code == writeBigInt:
			return th.formErr(code, werr, j.limit, "repr", "")
		case code == writeLimit && j.limit < maxAlloc:
			// The budget is what the form outgrew.
			return th.formErr(code, werr, j.limit, "repr", "repr: excessive result size")
		}
	case jobPrint:
		for i, v := range j.args {
			if i > 0 {
				out.WriteString(j.sep)
			}
			if s, ok := AsString(v); ok {
				if out.Len()+len(s) >= j.limit {
					return th.refuseBytes(out.Len()+len(s), "print: excessive output size")
				}
				out.WriteString(s)
			} else if b, ok := v.(Bytes); ok {
				if out.Len()+len(b) >= j.limit {
					return th.refuseBytes(out.Len()+len(b), "print: excessive output size")
				}
				out.WriteString(string(b))
			} else if code, werr := writeValueMeter(out, v, j.limit, m); code != writeOK {
				return th.formErr(code, werr, j.limit, "print", "print: excessive output size")
			}
		}
	case jobInterpolate:
		return interpolateTo(th, out, m, j.limit, j.format, j.x)
	case jobFormat:
		return stringFormatTo(th, out, m, j.limit, j.format, j.args, j.kwargs)
	}
	return nil
}

// charge charges the n bytes of the form, before it is allocated if it is
// large.
func (j *formJob) charge(thread *Thread, n int) error {
	switch j.kind {
	case jobStr:
		return excess(thread.chargeBytes(n), "str: value's string form exceeds the size limit")
	case jobRepr:
		return thread.chargeBudget(uint64(n)) // (at the ceiling the bounded form is returned: the budget only)
	case jobPrint:
		return excess(thread.chargeBytes(n), "print: excessive output size")
	case jobInterpolate:
		return excess(thread.chargeBytes(n), "excessive string interpolation (over %d bytes)", maxAlloc)
	case jobFormat:
		return excess(thread.chargeBytes(n), "format: excessive result size")
	}
	return nil
}

// buildForm makes the string of j, running it twice if it is large: the first
// pass counts the bytes, charges the work and the limit, and allocates nothing
// but the first formProbe bytes; the second writes into a buffer of the exact
// size.
func (thread *Thread) buildForm(j *formJob) (string, error) {
	// The sink and the meter of the first pass are the thread's, reused: made
	// afresh they would be allocated (they are passed to code that the compiler
	// cannot see through), a few dozen bytes for each short string. A form made
	// inside a form (a String method of a host value) finds them taken.
	var out *sink
	var m *meter
	if thread != nil && !thread.formBusy {
		thread.formBusy = true
		defer thread.releaseForm()
		out, m = &thread.formSink, &thread.formMeter
		*out = sink{probe: formProbe}
		*m = meter{thread: thread}
	} else {
		out, m = &sink{probe: formProbe}, &meter{thread: thread}
	}
	if err := j.run(thread, out, m); err != nil {
		return "", err
	}
	if err := m.flush(); err != nil {
		return "", err
	}
	n := out.n
	if err := j.charge(thread, n); err != nil {
		return "", err
	}
	if !out.counting {
		return out.String(), nil
	}
	// The second pass: the work was charged by the first.
	var w sink
	w.b.Grow(n)
	if err := j.run(nil, &w, nil); err != nil {
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
	if !s.counting && s.n+len(str)+3 <= s.probe {
		// A form that is small is made in one piece: the quotes and the text
		// (an escape or a rune that is not ASCII makes it grow, once).
		s.target().Grow(len(str) + 3)
	}
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

// releaseForm gives the thread's sink and meter back, and lets go of what the
// sink held.
func (thread *Thread) releaseForm() {
	thread.formSink = sink{}
	thread.formMeter = meter{}
	thread.formBusy = false
}
