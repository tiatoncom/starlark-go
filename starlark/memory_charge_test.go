package starlark

// The charges of memory accounting that no table pins exactly: the work (and
// the bytes) that an operation is charged, compared with the number that its
// formula in the code gives. Each assertion is an equality, on operands large
// enough that a price that is halved, or dropped, changes the number.
//
// What an operation was charged is measured as the work beyond the steps of its
// opcodes. The charges that do not depend on the operand (the call of a
// built-in, the statement) are taken out by comparing with the same operation
// on a different operand, or on a list that differs only in the capacity.

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

// mcMeasure runs setup, then op, and returns what op alone charged: the work
// beyond the steps of its opcodes, and the bytes of the allocation budget.
// mclist(n, spare) is a list of the integers 0..n-1 with room for spare more.
func mcMeasure(t *testing.T, setup, op string) (extra, alloc uint64) {
	t.Helper()
	th := &Thread{Name: "mc"}
	var w0, s0, a0 uint64
	predeclared := StringDict{
		"reset": NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
			w0, s0, a0 = th.Work(), th.Steps, th.AllocatedBytes()
			return None, nil
		}),
		"mclist": NewBuiltin("mclist", func(_ *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
			var n, spare int
			if err := UnpackPositionalArgs(b.Name(), args, kwargs, 2, &n, &spare); err != nil {
				return nil, err
			}
			elems := make([]Value, n, n+spare)
			for i := range elems {
				elems[i] = MakeInt(i)
			}
			return NewList(elems), nil
		}),
	}
	opts := &syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}
	if _, err := ExecFileOptions(opts, th, "mc.star", setup+"\nreset()\n"+op+"\n", predeclared); err != nil {
		t.Fatalf("%v\n%s\n%s", err, setup, op)
	}
	return (th.Work() - w0) - (th.Steps - s0), th.AllocatedBytes() - a0
}

func mcExtra(t *testing.T, setup, op string) uint64 {
	t.Helper()
	extra, _ := mcMeasure(t, setup, op)
	return extra
}

// mcDiff is the work that op charged on the operand of setup, beyond what it
// charged on the operand of base.
func mcDiff(t *testing.T, setup, base, op string) uint64 {
	t.Helper()
	return mcExtra(t, setup, op) - mcExtra(t, base, op)
}

// The work of x += y: a unit for each element added (three for a dict or a set,
// which are read by following pointers), a unit for each element that is there
// if the slice grows (it is copied), and the unit of each 16 bytes of the slots
// that are added.
func TestMCListExtend_Work(t *testing.T) {
	const n = 1000
	for _, c := range []struct {
		name string
		y    string
		per  uint64 // the work of taking an element of y
	}{
		{"list", "mclist(1000, 0)", 1},
		{"tuple", "tuple(range(1000))", 1},
		{"dict", "dict(zip(range(1000), range(1000)))", 3},
		{"set", "set(range(1000))", 3},
	} {
		setup := "y = " + c.y + "\ns = mclist(1000, 1000)" // room for y: it does not grow
		base := "y = " + strings.Replace(c.y, "1000", "0", -1) + "\ns = mclist(1000, 1000)"
		// n elements added, and the slots of them charged as memory (16 bytes
		// each, a unit for each 16)
		if got, want := mcDiff(t, setup, base, "s += y"), c.per*n+n; got != want {
			t.Errorf("list += %s of %d, the list has room: %d units, want %d", c.name, n, got, want)
		}
		// the same list without room: the slice grows, and the 1000 slots that
		// are there are copied
		grown := "y = " + c.y + "\ns = mclist(1000, 0)"
		if got, want := mcDiff(t, grown, setup, "s += y"), c.per*n; got != want {
			t.Errorf("list += %s of %d: growing the slice cost %d units more, want %d", c.name, n, got, want)
		}
	}
	// list.extend is the same code
	if got, want := mcDiff(t, "y = mclist(1000, 0)\ns = mclist(1000, 0)", "y = mclist(1000, 0)\ns = mclist(1000, 1000)", "s.extend(y)"), uint64(n); got != want {
		t.Errorf("list.extend: growing the slice cost %d units more, want %d", got, want)
	}
}

// A slice with a step of a list or a tuple is charged 4 units for each element
// of the result (a cache miss for each) that a slice with the step 1, of the
// same length, is not.
func TestMCSlice_StrideWork(t *testing.T) {
	x := "x = mclist(1000, 0)"
	plain := mcExtra(t, x, "r = x[:500]")
	for _, op := range []string{"r = x[::2]", "r = x[::-2]"} {
		if got, want := mcExtra(t, x, op)-plain, uint64(500*4); got != want {
			t.Errorf("list %s charged %d units more than a slice of the same length with step 1, want %d", op, got, want)
		}
	}
	// a tuple sliced with step 1 shares the memory of the operand: it is free,
	// and a stride makes a tuple of 500 (the 4 units each, and a unit for each
	// 16 bytes of its memory)
	t1 := "x = tuple(range(1000))"
	if got := mcExtra(t, t1, "r = x[:500]"); got != 0 {
		t.Errorf("tuple[:500] was charged %d units, want 0", got)
	}
	for _, op := range []string{"r = x[::2]", "r = x[::-2]"} {
		if got, want := mcExtra(t, t1, op), uint64(500*4)+tupleBytes(500)/allocBytesPerWork; got != want {
			t.Errorf("tuple %s was charged %d units, want %d", op, got, want)
		}
	}
	if got := mcExtra(t, t1, "r = x[0:0:2]"); got != 0 {
		t.Errorf("an empty stride slice of a tuple was charged %d units, want 0", got)
	}
}

// A new set that is made by a method, and a dict(), are charged the memory of
// their base (512 bytes: 32 units of work) before any entry is: with nothing in
// either operand that is all that there is to charge, besides the call.
func TestMCEmptyContainerBase(t *testing.T) {
	for _, op := range []string{
		"r = a.difference(b)",
		"r = a.intersection(b)",
		"r = dict()",
		"r = dict(a)",
	} {
		extra, alloc := mcMeasure(t, "a = set(); b = set()", op)
		if want := uint64(baseCall) + dictBytes(0)/allocBytesPerWork; extra != want {
			t.Errorf("%s: %d units of work, want %d", op, extra, want)
		}
		if alloc != dictBytes(0) {
			t.Errorf("%s: %d bytes, want %d", op, alloc, dictBytes(0))
		}
	}
}

// A keyword argument is looked up among the parameters: 3 units each, that a
// positional argument is not charged.
func TestMCSetArgs_KeywordWork(t *testing.T) {
	const k = 40 // (a frame of this size is free)
	var params, pos, kw []string
	for i := 0; i < k; i++ {
		params = append(params, fmt.Sprintf("a%d", i))
		pos = append(pos, fmt.Sprint(i))
		kw = append(kw, fmt.Sprintf("a%d=%d", i, i))
	}
	setup := "def f(" + strings.Join(params, ", ") + "): return 0"
	positional := mcExtra(t, setup, "f("+strings.Join(pos, ", ")+")")
	keyword := mcExtra(t, setup, "f("+strings.Join(kw, ", ")+")")
	if got, want := keyword-positional, uint64(3*k); got != want {
		t.Errorf("%d keyword arguments were charged %d units more than %d positional, want %d", k, got, k, want)
	}
}

// list.insert into a list whose slice is full copies every slot to new memory
// (2 units a slot); one that has room for it does not.
func TestMCListInsert_GrowthWork(t *testing.T) {
	const n = 1000
	full := mcExtra(t, "s = mclist(1000, 0)", "s.insert(0, 1)")
	room := mcExtra(t, "s = mclist(1000, 1)", "s.insert(0, 1)")
	if got, want := full-room, uint64(2*n); got != want {
		t.Errorf("insert into a full list of %d charged %d units more than into one with room, want %d", n, got, want)
	}
}

// list.remove moves the slots after the element down: a unit for 4 slots.
func TestMCListRemove_MemmoveWork(t *testing.T) {
	// the element that is found first, in lists of 1000 and of 2 (the memmove of
	// 2 slots is under a unit): the scan and the call are the same
	long := mcExtra(t, "s = mclist(1000, 0)", "s.remove(0)")
	short := mcExtra(t, "s = mclist(2, 0)", "s.remove(0)")
	if got, want := long-short, workSlots(1000)-workSlots(2); got != want {
		t.Errorf("remove(0) of a list of 1000 charged %d units more than of a list of 2, want %d", got, want)
	}
	if want := uint64(250); workSlots(1000) != want {
		t.Fatalf("workSlots(1000) = %d, want %d", workSlots(1000), want)
	}
}

// The scan of a string for the bytes that are not UTF-8: a unit for 64 bytes,
// whether or not there is anything to allocate. bytes(str) is charged by its
// price besides (len/64 too); str(bytes) by the scan alone.
func TestMCBytesStr_ScanWork(t *testing.T) {
	const n = 6400
	scan := workFast(n)
	if scan != 100 {
		t.Fatalf("workFast(%d) = %d, want 100", n, scan)
	}
	for _, c := range []struct {
		name, setup, empty, op string
		want                   uint64
	}{
		{"bytes(str)", "s = 'a' * 6400", "s = ''", "r = bytes(s)", 2 * scan},
		{"str(bytes)", "s = b'a' * 6400", "s = b''", "r = str(s)", scan},
	} {
		if got := mcDiff(t, c.setup, c.empty, c.op); got != c.want {
			t.Errorf("%s of %d bytes was charged %d units more than of none, want %d", c.name, n, got, c.want)
		}
	}
}

// A function whose frame (locals and operand stack) is more than 64 values is
// charged all of it: 16 bytes of allocation, and 2 units of work, a value;
// a smaller one nothing.
func TestMCFrame_ChargedAboveTheFreeSlots(t *testing.T) {
	for _, p := range []int{1, 10, 30, 62, 63, 64, 65, 66, 100, 300} {
		var params, args []string
		var argv Tuple
		for i := 0; i < p; i++ {
			params = append(params, fmt.Sprintf("a%d", i))
			args = append(args, "0")
			argv = append(argv, MakeInt(0))
		}
		th := &Thread{Name: "mc"}
		opts := &syntax.FileOptions{}
		globals, err := ExecFileOptions(opts, th, "f.star", "def f("+strings.Join(params, ", ")+"):\n    return 0\n", nil)
		if err != nil {
			t.Fatal(err)
		}
		fn := globals["f"].(*Function)
		nspace := len(fn.funcode.Locals) + fn.funcode.MaxStack
		w0, s0, a0 := th.Work(), th.Steps, th.AllocatedBytes()
		if _, err := Call(th, fn, argv, nil); err != nil {
			t.Fatal(err)
		}
		extra, alloc := (th.Work()-w0)-(th.Steps-s0), th.AllocatedBytes()-a0
		var wantExtra, wantAlloc uint64
		if nspace > frameFreeSlots {
			wantExtra, wantAlloc = 2*uint64(nspace), 16*uint64(nspace)
		}
		if alloc != wantAlloc || extra != wantExtra {
			t.Errorf("a call of a function of %d parameters (a frame of %d values): %d bytes and %d units of work, want %d and %d", p, nspace, alloc, extra, wantAlloc, wantExtra)
		}
	}
	if frameFreeSlots != 64 {
		t.Errorf("frameFreeSlots = %d, want 64", frameFreeSlots)
	}
}

// chargeFrame: the memory of the frame as allocation (16 bytes a value) and a
// unit of work for each value besides the memory's (a unit for 16 bytes).
func TestMCChargeFrame_Exact(t *testing.T) {
	for _, n := range []int{65, 100, 1000} {
		th := &Thread{}
		if err := th.chargeFrame(n); err != nil {
			t.Fatal(err)
		}
		if got, want := th.AllocatedBytes(), 16*uint64(n); got != want {
			t.Errorf("chargeFrame(%d): %d bytes, want %d", n, got, want)
		}
		if got, want := th.Work(), 2*uint64(n); got != want {
			t.Errorf("chargeFrame(%d): %d units of work, want %d", n, got, want)
		}
	}
}

// chargeGrowth: the entries that an operation added to a table of before
// entries are charged as the table grew (128 bytes an entry past the 7 of the
// inline bucket), and a unit of work for each 16 of those bytes.
func TestMCChargeGrowth_Exact(t *testing.T) {
	for _, c := range []struct{ before, added int }{{0, 1000}, {100, 1000}, {7, 3}, {0, 5}} {
		th := &Thread{}
		m := meter{thread: th, added: c.added}
		if err := th.chargeGrowth(c.before, &m); err != nil {
			t.Fatal(err)
		}
		bytes := dictBytes(c.before+c.added) - dictBytes(c.before)
		if got := th.AllocatedBytes(); got != bytes {
			t.Errorf("chargeGrowth(%d, +%d): %d bytes, want %d", c.before, c.added, got, bytes)
		}
		if got, want := th.Work(), bytes/16; got != want {
			t.Errorf("chargeGrowth(%d, +%d): %d units of work, want %d", c.before, c.added, got, want)
		}
		if m.added != 0 {
			t.Errorf("chargeGrowth left %d entries to charge", m.added)
		}
	}
	// 1000 entries into an empty table: 993 are past the inline bucket
	th := &Thread{}
	m := meter{thread: th, added: 1000}
	_ = th.chargeGrowth(0, &m)
	if got, want := th.Work(), uint64(993*128/16); got != want {
		t.Errorf("1000 entries: %d units of work, want %d", got, want)
	}
}
