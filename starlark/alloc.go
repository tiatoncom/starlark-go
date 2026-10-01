package starlark

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"

	"go.starlark.net/syntax"
)

// This file implements the allocation accounting of a Thread.
//
// The step limit (SetMaxExecutionSteps) counts interpreter opcodes, so it
// cannot observe an operation whose result is large relative to the number
// of steps it takes: s.replace(a, b) is one call but produces len(s) * len(b)
// bytes, list(range(N)) is one call but produces N elements, x = x + x
// doubles a list in one step. Two mechanisms bound such "amplifiers":
//
//   - a hard ceiling on the size in BYTES of one result (maxAlloc, see
//     eval.go), applied whether or not the thread has a budget: no single
//     operation asks Go for more than 1 GiB (a request of terabytes is a
//     fatal "out of memory", which recover cannot catch, and a request out of
//     range is a makeslice panic);
//
//   - a per-thread byte budget (SetMaxAllocBytes) that is charged for every
//     allocation the amplifiers make, for every container, string and function
//     the interpreter creates, and for every container or string a built-in
//     function returns (see Call).
//
// Every amplifier computes the size of its result by arithmetic BEFORE it
// allocates (with protection against overflow), checks the ceiling and the
// budget, and only then allocates. The exceptions are the string forms
// (str, repr, print, %, format, fail), whose size cannot be known without
// building them: they are built up to the limit of the thread (the smaller of
// the ceiling and the remaining budget, see stringLimit) and stop there, and a
// leaf (a string, bytes or int) is measured before it is written.
//
// The unit is the byte, by a declared formula rather than by measuring the
// heap, so that the count is deterministic: the same program on the same
// input is charged the same number of bytes in every run and on every
// platform. The formulas were calibrated against the retained size of the
// real structures (TestAllocFormulaCalibration keeps charged >= 0.8 x
// retained):
//
//	String, Bytes   len(s)
//	List            allocBaseList + 16 * len          (header + a Value per element)
//	Tuple           allocBaseTuple + 16 * len         (0 for the empty tuple)
//	Dict, Set       allocBaseDict + allocBytesPerEntry * max(len - 7, 0)
//	                (the base holds the inline bucket: 7 entries before the
//	                table grows)
//	Function        allocBaseFunction                 (a def or lambda value)
//	Int (big)       allocBaseInt + 8 * words          (every operation that makes a big integer)
//
// A list of NEW values (the strings of split, the values an iterator of
// unknown length produces) is charged allocBytesPerNewValue per element: the
// slot and the header of the value.
//
// What is NOT charged, and why. Growth in place by one element at a time,
// when it is not a dict or set entry: list.append and list.insert (the slot of
// 16 bytes; the value stored in it was charged when it was made, a big integer
// included). Each costs the interpreter several steps (loop, load, call,
// store) for 16 bytes, which is bounded by the step limit: measured
// (TestAllocUnchargedGrowthPerStep) the worst retained-but-uncharged growth
// is allocUnchargedBytesPerStep bytes per step, so a program that runs S steps
// without any amplifier retains at most allocUnchargedBytesPerStep * S bytes
// beyond what was charged (~20-50 MiB at the engine default of 10M steps). The
// operations that create a container or a function, add an entry to a dict or
// set, or copy many existing elements in one step (x += x, x.extend(x),
// f(*x)) are charged. Slicing a string, bytes or tuple with step 1 shares the
// memory of the operand and allocates nothing; reading, indexing, iterating
// and comparing allocate nothing.
//
// The counter is monotonic: it measures the total allocated by the thread,
// not the memory still retained, so garbage counts. This keeps it
// deterministic (no dependence on the garbage collector) at the price of
// charging idioms that build a value by repeated copying by the sum of the
// copies: s = s + piece in a loop is quadratic (at a budget of 256 MiB it
// stops after ~3300 pieces of 50 bytes), and so is x = x + [i] (after ~5800
// elements). This is a property of the count, not a defect; the linear forms
// are "".join(parts), list.append and x += [i].
//
// An operation that fails part way is not undone. A list extended with an
// iterator that is refused after 65 elements keeps those 65 elements, and a
// dict updated from an endless iterator keeps the entries inserted so far;
// the refusal ends the execution (it is an error), so no script observes the
// partial state, but a host that recovers a value across the refusal does.
const (
	// allocBytesPerValue is the size charged for one List or Tuple slot: one
	// Value, an interface of two words.
	allocBytesPerValue = 16

	// allocBytesPerNewValue is charged per element for a sequence of values
	// created by the operation itself: the slot (16) and the 16-byte header
	// that a string value needs when it is stored in an interface.
	allocBytesPerNewValue = 32

	// allocBaseList is the List header: the elems slice (24), frozen and
	// itercount (8), rounded up to the 48-byte size class (measured: an empty
	// list is 51 bytes with its slot in the containing list).
	allocBaseList = 48

	// allocBaseTuple is the header of a non-empty Tuple: a slice header
	// boxed in an interface (24, rounded to 32). The empty tuple is nil.
	allocBaseTuple = 32

	// allocBaseDict is a Dict or Set: the hashtable (hashtable.go) holds its
	// first bucket inline, 8 entries of 56 bytes and a link = 456 bytes, plus
	// the table slice, head, tailLink and counters, = 512 bytes (measured:
	// 531 with the slot of the containing list). An empty dict or set costs
	// this much, which is why {} in a loop is not free.
	allocBaseDict = 512

	// allocDictInline is the number of entries the inline bucket holds before
	// the table grows (it grows when len >= 8).
	allocDictInline = 7

	// allocBytesPerEntry is charged for each entry beyond allocDictInline: an
	// entry is hash(4, padded to 8) + key(16) + value(16) + next(8) +
	// prevLink(8) = 56 bytes in a bucket of 8 entries plus a link. A table
	// grows at 6.5 entries per bucket and then has 3.25, so the buckets cost
	// 70-140 bytes per entry; the boxed key adds 8-16. 128 covers the worst
	// of a doubling cycle for small and large keys.
	allocBytesPerEntry = 128

	// allocBaseFunction is a Function value (def, lambda, closure): the
	// struct, its freevars slice, measured 83-99 bytes.
	allocBaseFunction = 96

	// allocBytesPerItem is the size charged for one (key, value) pair that a
	// method such as dict.items materializes: a list slot and a 2-tuple.
	allocBytesPerItem = allocBytesPerValue + allocBaseTuple + 2*allocBytesPerValue

	// allocBytesPerWork is the memory that takes one unit of work (~8 ns) to
	// allocate and zero: measured 0.25-0.4 ns a byte for the large allocations
	// (a list of a million slots takes ~6 ms to copy into new memory).
	allocBytesPerWork = 32

	// allocWorkFree is the size under which an allocation costs no work: a
	// dict or set base (512), a list of a few dozen slots.
	allocWorkFree = 1024

	// allocUnchargedBytesPerStep is the measured upper bound of the memory a
	// program retains per interpreter step through the growth that is not
	// charged (see above). It is asserted by TestAllocUnchargedGrowthPerStep.
	allocUnchargedBytesPerStep = 8
)

func listBytes(n int) uint64 {
	return satAdd(allocBaseList, satMul(uint64(max(n, 0)), allocBytesPerValue))
}

func tupleBytes(n int) uint64 {
	if n <= 0 {
		return 0
	}
	return satAdd(allocBaseTuple, satMul(uint64(n), allocBytesPerValue))
}

// dictBytes is the size of a Dict or Set of n entries.
func dictBytes(n int) uint64 {
	return satAdd(allocBaseDict, satMul(uint64(max(n-allocDictInline, 0)), allocBytesPerEntry))
}

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
// whether or not there is a budget. n = 0 always succeeds, even on a thread
// whose budget was lowered below what it has already been charged. Charging
// exactly up to the budget succeeds.
//
// A host built-in that builds its result incrementally (a loop over an
// iterator of unknown length) may charge as it goes; if a later charge is
// refused the earlier ones stay charged and what it has built is not undone:
// the refusal is an error that ends the execution (see the note at the top of
// alloc.go).
//
// A built-in that returns a string, bytes, list, tuple, dict or set need not
// charge for the shallow size of the result: Call charges whatever the
// built-in did not charge itself (see Call).
func (thread *Thread) ChargeAlloc(n uint64) error {
	if n == 0 {
		return nil
	}
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
	return thread.room()
}

// charge accounts for one allocation. size is the size in bytes of the result
// the operation builds (what the ceiling is applied to), bytes what is charged
// to the budget: the same, except when the operation adds to a value that
// exists (x += y charges the size of y and checks the size of x+y). It fails,
// and charges nothing, if the budget would be exceeded (an *AllocBudgetError)
// or if size reaches maxAlloc (errExcessive).
//
// A nil thread has no budget: only the ceiling applies. This is how the
// exported functions that take no thread (Binary) are bounded.
func (thread *Thread) charge(size, bytes uint64) error {
	if thread != nil && thread.maxAllocBytes != 0 {
		if err := thread.overBudget(bytes); err != nil {
			return err
		}
	}
	if size >= uint64(maxAlloc) {
		return errExcessive
	}
	if thread != nil {
		// Allocating and zeroing memory takes time, and the collector's, and
		// the memory that is allocated is garbage soon: a unit of work for each
		// allocBytesPerWork bytes.
		if err := thread.chargeWork(bytes / allocBytesPerWork); err != nil {
			return err
		}
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

// room returns the largest size in bytes such that charge(size, size)
// succeeds. It lets a loop that builds a result stop before it overshoots
// instead of computing the size first.
func (thread *Thread) room() uint64 {
	n := uint64(maxAlloc) - 1
	if thread != nil && thread.maxAllocBytes != 0 {
		var left uint64
		if thread.allocated < thread.maxAllocBytes {
			left = thread.maxAllocBytes - thread.allocated
		}
		if left < n {
			n = left
		}
	}
	return n
}

// chargeValues charges for a new List of n elements.
func (thread *Thread) chargeValues(n int) error {
	b := listBytes(n)
	return thread.charge(b, b)
}

// chargeTuple charges for a new Tuple of n elements.
func (thread *Thread) chargeTuple(n int) error {
	b := tupleBytes(n)
	return thread.charge(b, b)
}

// chargeEntries charges for a new Dict or Set of n entries.
func (thread *Thread) chargeEntries(n int) error {
	b := dictBytes(n)
	return thread.charge(b, b)
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
	switch err.(type) {
	case *AllocBudgetError, *WorkBudgetError, *cancelledError:
		return err
	}
	return fmt.Errorf("%s: %v", prefix, err)
}

// stringLimit returns the length at which a string being built must be
// treated as too large: the smaller of the ceiling and one more than the
// remaining budget. A form that reaches it fails charge.
func (thread *Thread) stringLimit() int {
	return int(thread.room()) + 1
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
		return listBytes(len(v.elems))
	case Tuple:
		return tupleBytes(len(v))
	case *Dict:
		return dictBytes(int(v.ht.len))
	case *Set:
		return dictBytes(int(v.ht.len))
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

// A big integer (one that does not fit in 32 bits) is a big.Int, 32 bytes, and
// a slice of words whose capacity is 4 words more than its length (math/big
// allocates so) and is rounded up to a size class of the allocator (at most
// an eighth): allocBaseInt bytes and allocBytesPerWord for each word of the
// digits cover both. A list of integers of 33 bits retains 51 bytes an integer
// besides its slot, one of 4000 bits 612, one of 64000 bits 9 KiB
// (TestAllocFormulaCalibration).
const (
	allocBaseInt      = 64
	allocBytesPerWord = 9
)

// intBytesOfWords is the size charged for a big integer of w words.
func intBytesOfWords(w uint64) uint64 { return satAdd(allocBaseInt, satMul(allocBytesPerWord, w)) }

// bigIntBytes returns the size charged for x if it is a big integer (one that
// does not fit in 32 bits), and 0 for a small one.
func bigIntBytes(x Int) uint64 {
	if _, big := x.get(); big != nil {
		return intBytesOfWords(uint64(len(big.Bits())))
	}
	return 0
}

// checkRoom refuses what charge(size, bytes) would refuse, and charges
// nothing: the check of an operation whose result is charged when it exists
// (its size is known only then), made with an upper bound before the result is
// allocated.
func (thread *Thread) checkRoom(size, bytes uint64) error {
	if thread != nil && thread.maxAllocBytes != 0 {
		if err := thread.overBudget(bytes); err != nil {
			return err
		}
	}
	if size >= uint64(maxAlloc) {
		return errExcessive
	}
	return nil
}

// intRoom refuses, before it is made, a big integer of at most w words that
// would not fit in the budget or under the ceiling. w = 0 (both operands
// small) is not checked: its result is under 80 bytes.
func (thread *Thread) intRoom(w uint64) error {
	if w == 0 {
		return nil
	}
	b := intBytesOfWords(w)
	return thread.checkRoom(b, b)
}

// intDone charges the integer z that an operation made, if it is a big one,
// and returns it as the value of the operation. An operation that makes a big
// integer charges it: a list that holds a million of them holds a million
// boxes of their own size, which the steps that made them do not bound (the
// words of the longer operand are charged as time, not as memory).
func (thread *Thread) intDone(z Int) (Value, error) {
	if thread != nil {
		if _, big := z.get(); big != nil {
			if err := thread.chargeBudget(intBytesOfWords(uint64(len(big.Bits())))); err != nil {
				return nil, err
			}
		}
	}
	return z, nil
}

// Upper bounds of the words of the result of an operation on integers, for
// intRoom (0: both operands are small, nothing to check).
func wordsAdd(x, y Int) uint64 {
	if w := max(bigWords(x), bigWords(y)); w != 0 {
		return w + 1
	}
	return 0
}

func wordsMul(x, y Int) uint64 {
	if wx, wy := bigWords(x), bigWords(y); wx != 0 || wy != 0 {
		return max(wx, 1) + max(wy, 1)
	}
	return 0
}

// chargeOne charges one more element of unit bytes to a
// result of unknown final length that holds have elements: the check of a loop
// that cannot compute the size of its result before it starts.
func (thread *Thread) chargeOne(have int, unit uint64) error {
	return thread.charge(satMul(uint64(have)+1, unit), unit)
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

// chargeNewEntry charges the entry just added to d if d grew from before
// entries and is past the inline bucket that the base of a dict covers.
func (thread *Thread) chargeNewEntry(d *Dict, before int) error {
	if n := d.Len(); n > before && n > allocDictInline {
		return thread.charge(dictBytes(n), allocBytesPerEntry)
	}
	return nil
}

// chargeNewSetEntry is chargeNewEntry for a set.
func (thread *Thread) chargeNewSetEntry(s *Set, before int) error {
	if n := s.Len(); n > before && n > allocDictInline {
		return thread.charge(dictBytes(n), allocBytesPerEntry)
	}
	return nil
}

// formErr is the error for a string form that writeValueLimit (or writeValueMeter) reported as
// other than complete: the budget error (or the ceiling error, with the
// message limitMsg) if it reached the limit, and a nesting error if the value
// is deeper than MaxValueDepth.
func (thread *Thread) formErr(code int, werr error, limit int, what, limitMsg string) error {
	if werr != nil {
		return werr // the steps ran out
	}
	switch code {
	case writeDeep:
		return fmt.Errorf("%s: value is nested more than %d levels deep", what, MaxValueDepth)
	case writeBigInt:
		return fmt.Errorf("%s: an integer of more than %d decimal digits is not converted to a string", what, MaxIntDigits)
	}
	if strings.Contains(limitMsg, "%d") {
		return thread.refuseBytes(limit, limitMsg, maxAlloc)
	}
	return thread.refuseBytes(limit, "%s", limitMsg)
}

// chargeItems charges for a new List of n (key, value) pairs, each a 2-tuple:
// what dict.items and enumerate build.
func (thread *Thread) chargeItems(n int) error {
	b := satAdd(allocBaseList, satMul(uint64(max(n, 0)), allocBytesPerItem))
	return thread.charge(b, b)
}

// chargeNewValues charges for a new List of n values that the operation
// creates itself (the strings of split): the slot and the header of each.
func (thread *Thread) chargeNewValues(n int) error {
	b := satAdd(allocBaseList, satMul(uint64(max(n, 0)), allocBytesPerNewValue))
	return thread.charge(b, b)
}

// zipRowBytes is the size of a row of zip(...) of cols iterables: a list slot
// and a tuple of cols values.
func zipRowBytes(cols int) uint64 {
	return allocBytesPerValue + allocBaseTuple + satMul(uint64(cols), allocBytesPerValue)
}

// A chargedIter wraps the iterator of an iterable of unknown length that
// feeds a set or dict operation which allocates one entry per element:
// each element is charged before it is returned, and the iteration stops,
// with err set, when the charge is refused. The caller checks err.
type chargedIter struct {
	Iterator
	thread *Thread
	have   int    // entries the result already holds
	unit   uint64 // bytes per element
	err    error
}

func (c *chargedIter) Next(p *Value) bool {
	if c.err != nil || !c.Iterator.Next(p) {
		return false
	}
	if err := c.thread.chargeOne(c.have, c.unit); err != nil {
		c.err = excess(err, "excessive size (over %d elements)", maxAlloc)
		return false
	}
	c.have++
	return true
}

// ListAllocBytes, TupleAllocBytes and DictAllocBytes return the size by the
// formula of alloc.go of a new list, tuple, or dict or set of n elements. A
// host built-in that builds one of them can charge it with ChargeAlloc before
// it allocates.
func ListAllocBytes(n int) uint64  { return listBytes(n) }
func TupleAllocBytes(n int) uint64 { return tupleBytes(n) }
func DictAllocBytes(n int) uint64  { return dictBytes(n) }

// elemUnit is the size charged per element when a sequence is built from the
// iterable it: the elements of string.elems() and string.codepoints() are
// new strings (a slot and a header), the elements of anything else exist, or
// are small values.
func elemUnit(it Value) uint64 {
	switch it := it.(type) {
	case stringElems:
		if !it.ords {
			return allocBytesPerNewValue
		}
	case stringCodepoints:
		if !it.ords {
			return allocBytesPerNewValue
		}
	}
	return allocBytesPerValue
}

// chargeListOf charges for a new List of n elements built from it.
func (thread *Thread) chargeListOf(n int, it Value) error {
	b := satAdd(allocBaseList, satMul(uint64(max(n, 0)), elemUnit(it)))
	return thread.charge(b, b)
}

// unaryInt is -x or ~x of an integer, charged as the binary operations are.
func (thread *Thread) unaryInt(op syntax.Token, x Int) (Value, error) {
	if w := bigWords(x); w != 0 {
		if err := thread.chargeWork(w); err != nil {
			return nil, err
		}
		if err := thread.intRoom(w + 1); err != nil {
			return nil, excess(err, "excessive integer operation")
		}
	}
	z, err := x.Unary(op)
	if err != nil {
		return nil, err
	}
	return thread.intDone(z.(Int))
}

// frameFreeSlots is the size of a frame (locals and operand stack, in values)
// under which a call charges nothing for it: the memory of an ordinary
// function is garbage as soon as it returns, and its time is that of the
// opcodes of the call. A function whose frame is larger charges all of it.
const frameFreeSlots = 64

// chargeFrame charges the frame of a call of nspace values: its memory as
// allocation, and a unit of work for each of its values besides the memory's
// (the defaults are bound to the parameters one by one).
func (thread *Thread) chargeFrame(nspace int) error {
	b := satMul(uint64(nspace), allocBytesPerValue)
	if err := thread.charge(b, b); err != nil {
		return excess(err, "excessive frame (%d values)", nspace)
	}
	return thread.chargeWork(uint64(nspace))
}

// growthBytes is the memory that a dict or set of before entries gains by
// growing by added entries.
func growthBytes(before, added int) uint64 {
	return dictBytes(before+added) - dictBytes(before)
}

// roomGrowth refuses, before anything is done, a growth of a dict or set of
// before entries by at most bound entries that would not fit: the check of an
// operation that charges what it actually adds (the keys that are in the table
// already are not new), made with the most it could add.
func (thread *Thread) roomGrowth(before, bound int) error {
	if bound <= 0 {
		return nil
	}
	return thread.checkRoom(dictBytes(before+bound), growthBytes(before, bound))
}

// chargeGrowth charges the entries that the operation of m added to a table
// that had before entries, and not the keys that it only looked up: a table
// that is updated with its own keys does not grow, and is not charged.
func (thread *Thread) chargeGrowth(before int, m *meter) error {
	n := m.added
	m.added = 0
	if n == 0 || thread == nil {
		return nil
	}
	b := growthBytes(before, n)
	if err := thread.chargeBudget(b); err != nil {
		return err
	}
	return thread.chargeWork(b / allocBytesPerWork)
}

// finishGrowth ends an operation that made a new dict or set (before = 0) or
// grew one: it flushes the meter, and charges the entries that were added.
func (thread *Thread) finishGrowth(before int, m *meter, err error) error {
	if err == nil {
		err = m.flush()
	}
	// (the entries that were added are charged even if the operation failed
	// part way: they are there)
	if gerr := thread.chargeGrowth(before, m); err == nil {
		err = gerr
	}
	return err
}

// roomEntries refuses, before anything is done, a new dict or set of up to n
// entries that would not fit (nothing is charged).
func (thread *Thread) roomEntries(n int) error {
	b := dictBytes(n)
	return thread.checkRoom(b, b)
}
