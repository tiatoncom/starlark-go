package starlark

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sync/atomic"
	"unsafe"
)

// This file implements the accounting of the TIME that a program takes,
// beyond its interpreter steps, in units of WORK.
//
// A step (Thread.Steps) counts an opcode, and the step limit
// (SetMaxExecutionSteps) bounds the work of a program by bounding its opcodes.
// But one opcode, or one call of a built-in, can do work that is linear (or
// worse) in the size of its operand: `x in l` compares x with every element of
// l, `s.replace` scans the string, `d[k]` walks a chain of a hash table,
// `int(s)` is quadratic in the digits of s. Without a charge for that work,
// the number of steps is unrelated to the time taken: a program of a few dozen
// steps can run for hours, and cannot be interrupted, because the interpreter
// looks at the step limit and at cancellation only between opcodes.
//
// The steps are not changed for it: they are what they always were, for any
// program, whatever its operands. The work is a second counter, with its own
// limit, and the invariant of the second counter is one sentence:
//
//	a program that has done W units of work (Thread.Work) has used at most
//	C * W nanoseconds of CPU, where C is workNanoseconds
//
// A unit of work is about the time of one simple step of the interpreter. Every
// step is a unit, so the work is never less than the steps; the rest is what
// the operations charge for what they do (the table of prices in prices.go):
// the elements they visit, compare, probe, hash or copy, the bytes they scan,
// and the memory they allocate (garbage costs the allocator and the collector
// time too, and counts).
//
// An operation charges the work BEFORE it does it where its size is known in
// advance, so an operation that does not fit in what is left of the limit is
// refused instead of run; where the size is not known (an iterator, a
// comparison that stops at the first difference) it charges in chunks as it
// goes (a meter), and stops at the chunk where the limit is reached.
//
// When the limit is reached the operation fails with a *WorkBudgetError, the
// thread is cancelled (so that the host's built-ins, which may swallow an
// error, are stopped at the next opcode), and Work() is the limit, not more.
// The thread also notices a cancellation by the host (Thread.Cancel) at every
// charge, so a long operation is stopped as promptly as a long loop.
const workNanoseconds = 10

// workCancelReason is the reason of the cancellation of a thread that has done
// all the work it may.
const workCancelReason = "work budget exhausted"

// A WorkBudgetError is the error reported when an operation would take the
// work of the thread over the limit set by SetMaxWork. The operation did not
// run (or stopped at a chunk of work): the thread is cancelled, and Work() is
// the limit.
//
// A host recognizes the refusal with errors.As; it need not parse the text.
type WorkBudgetError struct {
	Limit     uint64 // the limit, as set by SetMaxWork
	Charged   uint64 // work charged before the request that was refused
	Requested uint64 // work the refused request needed (0: the steps reached the limit)
}

// Error reports the limit only: the other numbers derive from the data of the
// script.
func (e *WorkBudgetError) Error() string {
	return fmt.Sprintf("starlark: work budget exhausted: a thread may do at most %d units of work", e.Limit)
}

// SetMaxWork sets the limit of the work of this thread (see Work). When an
// operation would take it over the limit, the operation fails with a
// *WorkBudgetError and the thread is cancelled. A limit of 0, the default,
// means no limit: the work is counted all the same.
func (thread *Thread) SetMaxWork(max uint64) {
	thread.maxWork = max
	thread.regate()
}

// Work returns the work done by this thread so far, in units: its steps, and
// what the operations charged beyond them. It saturates at MaxUint64. It is
// counted whether or not there is a limit. After a refusal it is the limit.
func (thread *Thread) Work() uint64 {
	return satAdd(thread.Steps, thread.extraWork)
}

// ChargeWork charges n units of work to the thread. A built-in function of the
// host whose time is not constant calls it BEFORE it does the work, with the
// work it is about to do (an element visited or compared, a few bytes scanned,
// is a unit: see the table in prices.go); if it returns an error, the function
// must return it and stop, without doing the work. The error is a
// *WorkBudgetError if the limit would be exceeded, and the error of a
// cancellation if the thread was cancelled (by the host, or by the work or the
// step limit): in both the function must return it as it is.
//
// A function that does its work in a loop may charge as it goes (a chunk of
// a few hundred units at a time is as promptly stopped as a chunk of one).
// n = 0 is free.
//
// Like every method of Thread that changes it, ChargeWork must be called only
// from the goroutine that runs the thread.
func (thread *Thread) ChargeWork(n uint64) error {
	if thread == nil {
		return nil
	}
	if n != 0 {
		if thread.maxWork != 0 {
			if cur := thread.Work(); n > thread.maxWork-min(cur, thread.maxWork) {
				return thread.workExhausted(cur, n)
			}
		}
		thread.extraWork = satAdd(thread.extraWork, n)
		if thread.maxWork != 0 {
			thread.regate()
		}
	}
	if reason := atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&thread.cancelReason))); reason != nil {
		return thread.cancelError(*(*string)(reason))
	}
	return nil
}

// workExhausted is the refusal of a charge of n at the work cur: the work is
// the limit, and the thread is cancelled.
func (thread *Thread) workExhausted(cur, n uint64) error {
	// Work() = Steps + extraWork is the limit.
	if thread.maxWork > thread.Steps {
		thread.extraWork = thread.maxWork - thread.Steps
	} else {
		thread.extraWork = 0
	}
	e := &WorkBudgetError{Limit: thread.maxWork, Charged: cur, Requested: n}
	thread.workErr = e
	thread.Cancel(workCancelReason)
	thread.regate()
	return e
}

// regate sets the step at which the interpreter looks for the limits: the
// smaller of the host's step limit and the step at which the work would be the
// limit. (The interpreter compares one number at every step, as it did.)
func (thread *Thread) regate() {
	gate := thread.userMaxSteps
	if gate == 0 {
		gate = math.MaxUint64
	}
	if thread.maxWork != 0 {
		left := uint64(1)
		if thread.maxWork > thread.extraWork {
			left = max(thread.maxWork-thread.extraWork, 1)
		}
		gate = min(gate, left)
	}
	thread.maxSteps = gate
}

// stepGate is what the interpreter does when the steps reach the gate: the
// host's step limit (OnMaxSteps, or Cancel("too many steps")) if it is that,
// and the work limit if it is that.
func (thread *Thread) stepGate() {
	if thread.userMaxSteps != 0 && thread.Steps >= thread.userMaxSteps {
		if thread.OnMaxSteps != nil {
			thread.OnMaxSteps(thread)
		} else {
			thread.Cancel("too many steps")
		}
		return
	}
	if thread.maxWork != 0 && thread.Work() >= thread.maxWork && thread.workErr == nil {
		thread.workExhausted(thread.Work(), 0)
	}
}

// cancelError is the error of a cancelled thread, which an operation returns
// and the interpreter reports: the work error if that is the reason, and the
// text of the interpreter otherwise.
func (thread *Thread) cancelError(reason string) error {
	if reason == workCancelReason && thread.workErr != nil {
		return thread.workErr
	}
	return &cancelledError{reason}
}

// A cancelledError is the error of an operation that found the thread
// cancelled (by the step limit, or by the host). Its text is the one the
// interpreter gives; it is a type so that the built-ins pass it on unwrapped.
type cancelledError struct{ reason string }

func (e *cancelledError) Error() string {
	return "Starlark computation cancelled: " + e.reason
}

// chargeWork charges units of work. A nil thread (the exported functions that
// take none) is not charged.
func (thread *Thread) chargeWork(units uint64) error {
	if thread == nil {
		return nil
	}
	return thread.ChargeWork(units)
}

// flushWork is how much work a meter accumulates before it charges it.
const flushWork = 1024

// A meter accumulates the work of one operation whose size is not known in
// advance, and charges it as it grows (every flushWork units) and at the end,
// so that a long operation is stopped near the limit, not after it. A nil
// meter, or one of a nil thread, does nothing, so that the code of an
// operation is the same for the exported functions that have no thread.
type meter struct {
	thread  *Thread
	units   uint64 // work so far
	charged uint64 // units charged so far
	added   int    // entries added to hash tables so far (for chargeGrowth)
}

// add records n units of work.
func (m *meter) add(n uint64) error {
	if m == nil || m.thread == nil {
		return nil
	}
	m.units += n // (the units of one operation cannot overflow: they are sizes of memory that exists)
	if m.units-m.charged >= flushWork {
		return m.flush()
	}
	return nil
}

// flush charges the work recorded so far.
func (m *meter) flush() error {
	if m == nil || m.thread == nil {
		return nil
	}
	n := m.units - m.charged
	m.charged = m.units
	return m.thread.ChargeWork(n)
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

// iterWork is the work of one turn of the iterator of v, for an iterable whose
// length is not known (so that its work cannot be charged beforehand): a unit,
// and for the iterators that make a new string for each element (elems and
// codepoints of a string, elems of bytes: 15-30 ns of allocation and
// conversion) five more.
func iterWork(v Value) uint64 {
	switch it := v.(type) {
	case stringElems:
		if !it.ords {
			return 1
		}
	case stringCodepoints:
		if !it.ords {
			return 6
		}
	case bytesIterable:
		return 6
	}
	return 1
}

// words64 is the number of 64-bit words of the big integer b, whatever the size
// of the word of the platform (len(b.Bits()) would be twice as much on a
// platform of 32-bit words: the work must not depend on it).
func words64(b *big.Int) uint64 { return uint64(b.BitLen()+63) / 64 }

// bigWords returns the number of machine words of x if it is a big integer,
// and 0 for a small one.
func bigWords(x Int) uint64 {
	if _, big := x.get(); big != nil {
		return words64(big)
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
// into a table of a few entries takes ~30 ns, a cold one into a million 340 ns
// (and more under load): 1 unit up to 15 entries, so that a small table costs
// nothing beyond the free window, and one more unit for each doubling after
// that (18 units for two million entries).
func insertWork(n uint32) uint64 { return 1 + uint64(max(bits.Len32(n)-4, 0)) }

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
		return thread.chargeWork(words64(big))
	}
	d := uint64(big.BitLen()/3 + 1)
	return thread.chargeWork(d * d / 4096)
}
