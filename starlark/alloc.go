package starlark

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// This file implements the allocation accounting of a Thread.
//
// The step limit (SetMaxExecutionSteps) counts interpreter opcodes, so it
// cannot observe an operation whose result is large relative to the number
// of steps it takes: s.replace(a, b) is one call but produces len(s) * len(b)
// bytes, list(range(N)) is one call but produces N elements, x = x + x
// doubles a list in one step. Two mechanisms bound such "amplifiers":
//
//   - a hard ceiling on the size of one result (maxAlloc, see eval.go), applied
//     whether or not the thread has a budget, so that no operation can ask Go
//     for an allocation that crashes the process (out of memory is a fatal
//     error, not a panic that recover can catch);
//
//   - a per-thread byte budget (SetMaxAllocBytes) that is charged for every
//     allocation the amplifiers make, and for every container or string a
//     built-in function returns (see Call).
//
// Every amplifier computes the size of its result by arithmetic BEFORE it
// allocates (with protection against overflow), checks the ceiling and the
// budget, and only then allocates.
//
// The unit is the byte, by a declared formula rather than by measuring the
// heap, so that the count is deterministic: the same program on the same
// input is charged the same number of bytes in every run and on every
// platform.
//
//	String, Bytes   len(s)
//	List, Tuple     allocBytesPerValue * len        (one Value slot per element)
//	Dict, Set       allocBytesPerEntry * len        (one hash table entry per key)
//	Int (big)       (bit length + 7) / 8
//
// What is deliberately NOT charged, and why:
//
//   - Growth in place: append, d[k] = v, set.add, comprehension and literal
//     construction. Each element added costs the interpreter at least ~7 steps
//     (loop, load, call, store), so the step limit already bounds this growth
//     to a small multiple of the step budget; the host chooses that limit.
//     (Operations that copy or replicate many existing elements in one step,
//     such as x += x or x.extend(x), are amplifiers and are charged.)
//   - Slicing a string, bytes or tuple with step 1: the result shares the
//     operand's memory and allocates nothing.
//   - Values that already exist: reading, indexing, iterating, comparing.
//
// The counter is monotonic: it measures the total allocated by the thread,
// not the memory still retained, so garbage counts. This keeps it
// deterministic (no dependence on the garbage collector) at the price of
// over-counting idioms that build a value by repeated copying
// (x = x + [i] is charged 16*n bytes at iteration n).
const (
	// allocBytesPerValue is the size charged for one List or Tuple element:
	// one Value, an interface of two words.
	allocBytesPerValue = 16

	// allocBytesPerEntry is the size charged for one Dict or Set entry.
	//
	// An entry (hashtable.go) is hash(4, padded to 8) + key(16) + value(16) +
	// next(8) + prevLink(8) = 56 bytes, held in buckets of 8 entries plus a
	// link, 456 bytes. A table grows when it holds 6.5 entries per bucket on
	// average and then has 3.25, so the bucket arrays cost between 70 and 140
	// bytes per entry, ~93 on average over a doubling cycle. 96 (six Values)
	// is that average rounded to a multiple of the Value size; it is a flat
	// constant, not exact accounting. (A value of 64 would be below the 70
	// byte minimum.)
	allocBytesPerEntry = 96

	// allocBytesPerItem is the size charged for one (key, value) pair that a
	// method such as dict.items materializes: a list slot plus a 2-tuple.
	allocBytesPerItem = allocBytesPerValue + 2*allocBytesPerValue
)

// An AllocBudgetError is the error reported when an allocation would exceed
// the thread's budget set by SetMaxAllocBytes. The operation that returned it
// allocated nothing and was not charged.
//
// A host recognizes the refusal with errors.As; it need not parse the text.
type AllocBudgetError struct {
	Limit     uint64 // the budget, as set by SetMaxAllocBytes
	Allocated uint64 // bytes charged to the thread before the refused request
	Requested uint64 // bytes the refused request needed
}

// Error reports the limit only. The sizes of the refused request are fields,
// not text, because they derive from the script's data.
func (e *AllocBudgetError) Error() string {
	return fmt.Sprintf("starlark: allocation budget exhausted: a thread may allocate at most %d bytes", e.Limit)
}

// errExcessive is returned by charge when a single result reaches the hard
// ceiling maxAlloc. Callers translate it to an error that names the
// operation.
var errExcessive = errors.New("excessive allocation")

// SetMaxAllocBytes sets the budget, in bytes, of allocations that this thread
// may make (see the formula at the top of alloc.go). When an allocation would
// take the total over the budget, the operation fails with an
// *AllocBudgetError and allocates nothing. A budget of 0, the default, means
// no budget; the per-operation ceiling still applies.
func (thread *Thread) SetMaxAllocBytes(max uint64) {
	thread.maxAllocBytes = max
}

// AllocatedBytes returns the number of bytes charged to this thread so far.
// It is counted whether or not there is a budget. It saturates at MaxUint64.
func (thread *Thread) AllocatedBytes() uint64 {
	return thread.allocated
}

// ChargeAlloc charges n bytes to the thread's allocation budget. A built-in
// function implemented by the host calls it BEFORE allocating a result of n
// bytes; if it returns an error (an *AllocBudgetError when the budget would
// be exceeded), the function must return that error without allocating.
//
// ChargeAlloc fails if the thread has a budget and AllocatedBytes()+n exceeds
// it, or if n is not smaller than the ceiling of one operation (1<<30 bytes),
// whether or not there is a budget. n = 0 always succeeds. Charging exactly
// up to the budget succeeds.
//
// A built-in that returns a string, bytes, list, tuple, dict or set need not
// charge for the shallow size of the result: Call charges whatever the
// built-in did not charge itself (see Call).
func (thread *Thread) ChargeAlloc(n uint64) error {
	if err := thread.charge(n, n); err != nil {
		if err == errExcessive {
			return fmt.Errorf("excessive allocation (%d bytes)", n)
		}
		return err
	}
	return nil
}

// AllocHeadroom returns the largest number of bytes that one operation could
// be charged right now: the smaller of the per-operation ceiling and the
// remaining budget. A built-in that builds its result incrementally can stop
// as soon as the output exceeds it, and report the failure with ChargeAlloc.
func (thread *Thread) AllocHeadroom() uint64 {
	return thread.room(1)
}

// charge accounts for one allocation. units is its size in the unit the
// ceiling is expressed in (elements for lists and tuples, bytes for strings),
// bytes its size by the formula. It fails, and charges nothing, if the budget
// would be exceeded (an *AllocBudgetError) or if units reaches maxAlloc
// (errExcessive).
//
// A nil thread has no budget: only the ceiling applies. This is how the
// exported functions that take no thread (Binary) are bounded.
func (thread *Thread) charge(units, bytes uint64) error {
	if thread != nil && thread.maxAllocBytes != 0 {
		if err := thread.overBudget(bytes); err != nil {
			return err
		}
	}
	if units >= uint64(maxAlloc) {
		return errExcessive
	}
	if thread != nil {
		thread.allocated = satAdd(thread.allocated, bytes)
	}
	return nil
}

// chargeBudget charges bytes that were already allocated, so there is no
// ceiling to check; it only enforces the budget.
func (thread *Thread) chargeBudget(bytes uint64) error {
	if thread.maxAllocBytes != 0 {
		if err := thread.overBudget(bytes); err != nil {
			return err
		}
	}
	thread.allocated = satAdd(thread.allocated, bytes)
	return nil
}

// overBudget returns an error if charging bytes would exceed the budget.
// The comparison cannot overflow.
func (thread *Thread) overBudget(bytes uint64) error {
	max := thread.maxAllocBytes
	if bytes > max || thread.allocated > max-bytes {
		return &AllocBudgetError{Limit: max, Allocated: thread.allocated, Requested: bytes}
	}
	return nil
}

// room returns the largest number n of units, of unit bytes each, such that
// charge(n, n*unit) succeeds. It lets a loop that builds a result stop before
// it overshoots instead of computing the size first.
func (thread *Thread) room(unit uint64) uint64 {
	n := uint64(maxAlloc) - 1
	if thread != nil && thread.maxAllocBytes != 0 {
		var left uint64
		if thread.allocated < thread.maxAllocBytes {
			left = thread.maxAllocBytes - thread.allocated
		}
		if b := left / unit; b < n {
			n = b
		}
	}
	return n
}

// chargeValues charges for a new List or Tuple of n elements.
func (thread *Thread) chargeValues(n int) error {
	return thread.charge(uint64(max(n, 0)), satMul(uint64(max(n, 0)), allocBytesPerValue))
}

// chargeEntries charges for a new Dict or Set of n entries.
func (thread *Thread) chargeEntries(n int) error {
	return thread.charge(uint64(max(n, 0)), satMul(uint64(max(n, 0)), allocBytesPerEntry))
}

// chargeBytes charges for a new String or Bytes of n bytes.
func (thread *Thread) chargeBytes(n int) error {
	return thread.charge(uint64(max(n, 0)), uint64(max(n, 0)))
}

// excess translates the error of charge into the error of an operation: a
// budget error is returned as is (so that its text and type are the contract),
// and errExcessive becomes the formatted message.
//
// It is called only on the failure path, so formatting costs nothing when
// the operation succeeds.
func excess(err error, format string, args ...any) error {
	if err == errExcessive {
		return fmt.Errorf(format, args...)
	}
	return err
}

// prefixErr returns the error "prefix: err", except that a budget error is
// returned unchanged, so that its text always begins with the fixed prefix
// and the host can also recognize it by type through any wrapping.
func prefixErr(prefix string, err error) error {
	if _, ok := err.(*AllocBudgetError); ok {
		return err
	}
	return fmt.Errorf("%s: %v", prefix, err)
}

// stringLimit returns the length at which a string being built must be
// treated as too large: the smaller of the ceiling and one more than the
// remaining budget. A form that reaches it fails charge.
func (thread *Thread) stringLimit() int {
	return int(thread.room(1)) + 1
}

// shallowSize returns the size, by the formula, of the value itself: the
// bytes of a string, the slots of a list or tuple, the entries of a dict or
// set. Elements are not followed: they exist already, or were charged when
// they were made. Other values (ints, floats, functions, host types) are 0.
func shallowSize(v Value) uint64 {
	switch v := v.(type) {
	case String:
		return uint64(len(v))
	case Bytes:
		return uint64(len(v))
	case *List:
		return satMul(uint64(len(v.elems)), allocBytesPerValue)
	case Tuple:
		return satMul(uint64(len(v)), allocBytesPerValue)
	case *Dict:
		return satMul(uint64(v.ht.len), allocBytesPerEntry)
	case *Set:
		return satMul(uint64(v.ht.len), allocBytesPerEntry)
	}
	return 0
}

func satAdd(a, b uint64) uint64 {
	s, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return math.MaxUint64
	}
	return s
}

func satMul(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return math.MaxUint64
	}
	return lo
}

// sliceLen returns the number of elements of the slice [start:end:step]
// after slice has normalized its indices.
func sliceLen(start, end, step int) int {
	if step > 0 {
		if end <= start {
			return 0
		}
		return (end - start + step - 1) / step
	}
	if start <= end {
		return 0
	}
	return (start - end - step - 1) / -step
}

// bigIntBytes returns the size of the digits of x if it is a big integer,
// and 0 for a small one.
func bigIntBytes(x Int) uint64 {
	if _, big := x.get(); big != nil {
		return uint64(big.BitLen()+7) / 8
	}
	return 0
}

// chargeOne charges one more element, of the given size by the formula, to a
// result of unknown final length that holds have elements: the check of a loop
// that cannot compute the size of its result before it starts.
func (thread *Thread) chargeOne(have int, bytes uint64) error {
	return thread.charge(uint64(have)+1, bytes)
}

// refuseBytes returns the error for a string result of n bytes that reached
// the limit (stringLimit): the budget error if the budget is the reason, and
// else the formatted ceiling error.
func (thread *Thread) refuseBytes(n int, format string, args ...any) error {
	err := thread.chargeBytes(n)
	if err == nil {
		err = errExcessive // unreachable: n is at the limit, so it is refused
	}
	return excess(err, format, args...)
}
