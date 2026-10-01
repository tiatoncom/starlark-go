package starlark

import (
	"fmt"
	"math/bits"
	"sync/atomic"
	"unsafe"
)

// This file implements the accounting of the TIME that a built-in function or
// operator takes, in interpreter steps.
//
// A step (Thread.Steps) counts an opcode, and the step limit
// (SetMaxExecutionSteps) bounds the work of a program by bounding its
// opcodes. But one opcode, or one call of a built-in, can do work that is
// linear (or worse) in the size of its operand: `x in l` compares x with every
// element of l, `s.replace` scans the string, `d[k]` walks a chain of a hash
// table, `int(s)` is quadratic in the digits of s. Without a charge for that
// work, the number of steps is unrelated to the time taken: a program of a few
// dozen steps can run for hours, and cannot be interrupted, because the
// interpreter looks at the step limit and at cancellation only between
// opcodes.
//
// So an operation charges the steps for the work it does, in units of work
// declared per primitive (the table in prices.go):
//
//	1 unit of work = one element visited, compared, probed or hashed
//	                 (~8 ns: measured 3-10 ns for an int or string compare)
//	WorkPerStep units of work = 1 step
//
// An operation whose work is below FreeWork units costs no step beyond those
// of the opcodes, and the remainder is not carried to the next operation: the
// number of steps of a program whose operands are all small (under
// WorkPerStep elements, whatever the weight of an element: at most 4 units) is
// exactly what it was before the work was accounted. The charge is made BEFORE the work where
// its size is known in advance, so an operation that does not fit in the
// remaining steps is refused instead of run; where the size is not known (an
// iterator, a comparison that stops at the first difference) it is made in
// chunks as the work goes on, and the operation stops at the chunk where the
// limit is reached.
//
// The charge is made in exactly the way the interpreter makes it at an opcode:
// Steps is increased, OnMaxSteps (or Cancel("too many steps")) is called if the
// limit is reached, and a cancelled thread makes the operation fail with the
// error of the interpreter. A host that arms OnMaxSteps to poll its context
// every N steps (the engine does, every 1024) therefore also polls it during a
// long operation, at most N steps late.
//
// WorkPerStep is a measured trade-off (see prices.go): one step of an opcode
// takes 2.5-9 ns, one unit of work ~8 ns, so equal time per step would be
// 1 unit; but the steps of every program whose operands are below
// WorkPerStep must not change, and the engine has handlers with lists of a few
// dozen elements. At 16 the free window of an operation is 15 units
// (~120 ns, within an order of magnitude of what the opcodes around it cost),
// and the most time that a program can get out of one step of the limit is
// WorkPerStep * 8 ns = 128 ns: the 10M steps of the engine's default limit
// are at most ~1.3 s of work.
const WorkPerStep = 16

// FreeWork is the work, in units, under which an operation is not charged:
// 4 steps' worth, ~0.5 microsecond. An operation that does more work is
// charged all of it (units / WorkPerStep steps), not the excess.
const FreeWork = 4 * WorkPerStep

// ChargeSteps adds n steps to the thread, as if the interpreter had executed n
// more opcodes: it calls OnMaxSteps (or cancels the thread with "too many
// steps") if the limit is reached, and returns the error of a cancelled thread.
// A built-in function of the host whose time is not constant calls it, with
// the work it is about to do divided by WorkPerStep, before it does the work.
// n = 0 is free.
func (thread *Thread) ChargeSteps(n uint64) error {
	if n == 0 {
		return nil
	}
	thread.Steps = satAdd(thread.Steps, n)
	// (maxSteps is 0, "no limit", until the first call on the thread.)
	if thread.maxSteps != 0 && thread.Steps >= thread.maxSteps {
		if thread.OnMaxSteps != nil {
			thread.OnMaxSteps(thread)
		} else {
			thread.Cancel("too many steps")
		}
	}
	if reason := atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&thread.cancelReason))); reason != nil {
		return &cancelledError{*(*string)(reason)}
	}
	return nil
}

// A cancelledError is the error of an operation that found the thread
// cancelled (by the step limit, or by the host). Its text is the one the
// interpreter gives; it is a type so that the built-ins pass it on unwrapped.
type cancelledError struct{ reason string }

func (e *cancelledError) Error() string {
	return "Starlark computation cancelled: " + e.reason
}

// chargeWork charges units of work, if they make at least one step. A nil
// thread (the exported functions that take none) is not charged.
func (thread *Thread) chargeWork(units uint64) error {
	if units < FreeWork || thread == nil {
		return nil
	}
	return thread.ChargeSteps(units / WorkPerStep)
}

// flushWork is how much work a meter accumulates before it charges it.
const flushWork = 4096

// A meter accumulates the work of one operation whose size is not known in
// advance, and charges it as it grows (every flushWork units) and at the end,
// so that a long operation is stopped near the limit, not after it. The
// remainder below one step is dropped at the end of the operation. A nil
// meter, or one of a nil thread, does nothing, so that the code of an
// operation is the same for the exported functions that have no thread.
type meter struct {
	thread  *Thread
	units   uint64 // work so far
	charged uint64 // steps charged so far
}

// add records n units of work.
func (m *meter) add(n uint64) error {
	if m == nil || m.thread == nil {
		return nil
	}
	m.units += n // (the units of one operation cannot overflow: they are sizes of memory that exists)
	if m.units < FreeWork {
		return nil // the common case: a small operation
	}
	return m.addSlow()
}

// addSlow is add past the free window: charge when a chunk has built up.
func (m *meter) addSlow() error {
	if m.units-m.charged*WorkPerStep >= flushWork {
		return m.flush()
	}
	return nil
}

// flush charges the work recorded so far.
func (m *meter) flush() error {
	if m == nil || m.units < FreeWork || m.thread == nil {
		return nil
	}
	return m.flushSlow()
}

func (m *meter) flushSlow() error {
	if steps := m.units / WorkPerStep; steps > m.charged {
		n := steps - m.charged
		m.charged = steps
		return m.thread.ChargeSteps(n)
	}
	return nil
}

// meter returns a meter for the work of one operation of thread.
func (thread *Thread) meter() meter { return meter{thread: thread} }

// Units of work. Each is the measured cost of the primitive in units of ~8 ns
// (prices.go has the table of measurements).

// workFast is the work of copying, comparing, searching or hashing n bytes
// (memmove, memcmp, IndexByte: 0.02-0.1 ns a byte).
func workFast(n int) uint64 { return uint64(max(n, 0)) / 64 }

// workSlow is the work of transforming n bytes rune by rune (case mapping,
// is* predicates, quoting: 1-4 ns a byte).
func workSlow(n int) uint64 { return uint64(max(n, 0)) / 4 }

// workSlots is the work of copying n Value slots (16 bytes each, ~1 ns).
func workSlots(n int) uint64 { return uint64(max(n, 0)) / 4 }

// bigWords returns the number of machine words of x if it is a big integer,
// and 0 for a small one.
func bigWords(x Int) uint64 {
	if _, big := x.get(); big != nil {
		return uint64(len(big.Bits()))
	}
	return 0
}

// chargeIntLinear charges the work of an operation on two integers that is
// linear in their length (add, subtract, shift, and, or, xor, compare): the
// words of the longer. Small integers cost nothing.
func (thread *Thread) chargeIntLinear(x, y Int) error {
	w := max(bigWords(x), bigWords(y))
	if w == 0 {
		return nil
	}
	return thread.chargeWork(w)
}

// chargeIntQuadratic charges the work of an operation on two integers that is
// quadratic in their length (multiply, divide, modulo): the product of their
// words, a word product being ~1 ns (an eighth of a unit). It is an upper
// bound for the Karatsuba multiplication that Go uses for long operands.
func (thread *Thread) chargeIntQuadratic(x, y Int) error {
	wx, wy := bigWords(x), bigWords(y)
	if wx == 0 && wy == 0 {
		return nil
	}
	return thread.chargeWork(satMul(max(wx, 1), max(wy, 1)) / 8)
}

// insertWork is the work of adding an entry to a hash table of n entries,
// beyond hashing the key and walking the chain: the entry and its links, and
// the cache misses of a large table, which grow with its size. A warm insert
// into a table of a few entries takes ~30 ns, a cold one into a million 340 ns:
// 1 unit and half a unit for each doubling (3 units for 15 entries, 11 for a
// million).
func insertWork(n uint32) uint64 { return 1 + uint64(bits.Len32(n))/2 }

// chargeIntToString checks and charges the conversion of the integer x to a
// string: refused if it has more than MaxIntBits bits, and else charged the
// work, quadratic in its decimal digits for base 10 and linear for the other
// bases (power of two bases are linear).
func (thread *Thread) chargeIntToString(x Int, decimal bool) error {
	_, big := x.get()
	if big == nil {
		return nil
	}
	if big.BitLen() > maxIntBits {
		return fmt.Errorf("an integer of more than %d decimal digits is not converted to a string", MaxIntDigits)
	}
	if !decimal {
		return thread.chargeWork(uint64(len(big.Bits())))
	}
	d := uint64(big.BitLen()/3 + 1)
	return thread.chargeWork(d * d / 4096)
}
