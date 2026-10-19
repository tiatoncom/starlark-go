package starlark

// Property tests of the charges of the work that the cluster "iterWork" and
// the cluster "hashtable / workcmp" of the mutation run left unpinned (see
// work.go, workcmp.go, hashtable.go). Every expected number is derived from the
// text of the code, and every assertion is an equality: a charge that is
// dropped or halved changes it.
//
// Every top-level identifier of this file starts with "ih".

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

// ---- the unit of measure ----

// ihWork returns the work that f charges to a new thread.
func ihWork(t *testing.T, f func(th *Thread) error) uint64 {
	t.Helper()
	th := &Thread{Name: "ih"}
	if err := f(th); err != nil {
		t.Fatal(err)
	}
	return th.Work()
}

// ihMeterWork returns the work that f charges to a meter of a new thread, once
// the meter is flushed.
func ihMeterWork(t *testing.T, f func(m *meter) error) uint64 {
	t.Helper()
	return ihWork(t, func(th *Thread) error {
		m := th.meter()
		if err := f(&m); err != nil {
			return err
		}
		return m.flush()
	})
}

func ihCheck(t *testing.T, name string, got, want uint64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: charged %d units of work, want %d", name, got, want)
	}
}

// ihLookupWork is lookupWork (work.go) written again: nothing up to 1023
// entries, two units for each doubling after that.
func ihLookupWork(n int) uint64 {
	if d := bits.Len(uint(n)) - 10; d > 0 {
		return 2 * uint64(d)
	}
	return 0
}

// ihFast is workFast written again: a unit for each 64 bytes.
func ihFast(n int) uint64 { return uint64(n / 64) }

// ---- K2: iterables without a length are charged a unit and more per element ----

// TestIhIterWork_PerElementOfUnknownLength: for each consumer of an iterable
// whose length is not known, the work of f(lazy(n)) beyond that of f(lazy(0)) is
// n times the work of one element, exactly. The work of an element is
//
//	iterWork (1: the iterator of a value that is not a string or bytes)
//	+ the allocation of the element charged as it comes (bytes / 16 units,
//	  with the bytes per element of the consumer, alloc.go)
//
// consumer              alloc bytes     work per element (iter + alloc)
// list.extend           32 (value)      1 + 2 = 3
// bytes                 1               1 + 0 = 1
// enumerate             80 (item)       1 + 5 = 6
// reversed              32              1 + 2 = 3
// tuple                 32              1 + 2 = 3
// zip(a, b)             16+32+2*16=80   2 (cols) + 5 = 7
// zip(a, b, c)          16+32+3*16=96   3 (cols) + 6 = 9
//
// Every consumer flushes its meter when the loop ends, so the work is exactly
// n times that (the units of iterWork that are under a chunk of flushWork are
// charged too); n = 1024 and 5000 cross the chunks of the meter.
func TestIhIterWork_PerElementOfUnknownLength(t *testing.T) {
	for _, c := range []struct {
		name string
		fn   Value
		args func(n int) Tuple
		per  uint64 // work of an element that is not iterWork
		iter uint64 // iterWork of an element
		_    bool   // (the meter is flushed at the end of the loop: all of them)
	}{
		{"list.extend", nil, func(n int) Tuple { return Tuple{lazyIter{n: n}} }, 2, 1, true},
		{"list", Universe["list"], func(n int) Tuple { return Tuple{lazyIter{n: n}} }, 2, 1, true},
		{"bytes", Universe["bytes"], func(n int) Tuple { return Tuple{lazyIter{n: n, bytes: true}} }, 0, 1, false},
		{"enumerate", Universe["enumerate"], func(n int) Tuple { return Tuple{lazyIter{n: n}} }, 5, 1, false},
		{"reversed", Universe["reversed"], func(n int) Tuple { return Tuple{lazyIter{n: n}} }, 2, 1, false},
		{"tuple", Universe["tuple"], func(n int) Tuple { return Tuple{lazyIter{n: n}} }, 2, 1, false},
		{"zip2", Universe["zip"], func(n int) Tuple { return Tuple{lazyIter{n: n}, lazyIter{n: n}} }, 5, 2, true},
		{"zip3", Universe["zip"], func(n int) Tuple { return Tuple{lazyIter{n: n}, lazyIter{n: n}, lazyIter{n: n}} }, 6, 3, true},
	} {
		run := func(n int) uint64 {
			return ihWork(t, func(th *Thread) error {
				// a fresh receiver for list.extend: its growth is part of the element
				fn := c.fn
				if c.name == "list.extend" {
					fn, _ = NewList(nil).Attr("extend")
				}
				_, err := Call(th, fn, c.args(n), nil)
				return err
			})
		}
		base := run(0)
		for _, n := range []int{1, 10, 1000, 1023, 1024, 1025, 5000} {
			iter := uint64(n) * c.iter
			ihCheck(t, c.name+" n="+ihItoa(n), run(n)-base, uint64(n)*c.per+iter)
		}
	}
}

// TestIhIterWork_PriceOfAnElement: the work of one turn of the iterator of each
// kind of iterable. The iterables of a string and of bytes have a length, so no
// consumer (all of them ask iterWork only for an iterable without one) reaches
// the cases for them: the table is the only observation of them. In particular
// the `return 1` of the elems of a string (work.go) is not reachable through
// list, tuple or reversed of s.elems(), which charge it up front.
func TestIhIterWork_PriceOfAnElement(t *testing.T) {
	for _, c := range []struct {
		name string
		v    Value
		want uint64
	}{
		{"s.elems()", stringElems{"abc", false}, 1},
		{"s.elem_ords()", stringElems{"abc", true}, 1},
		{"s.codepoints()", stringCodepoints{"abc", false}, 6},
		{"s.codepoint_ords()", stringCodepoints{"abc", true}, 1},
		{"b.elems()", bytesIterable{Bytes("abc")}, 6},
		{"lazy", lazyIter{n: 3}, 1},
		{"list", NewList(nil), 1},
	} {
		ihCheck(t, "iterWork("+c.name+")", iterWork(c.v), c.want)
	}
}

func ihItoa(n int) string { return strconv.Itoa(n) }

// ---- K3: hash table ----

// ihKey is a Value whose hash is the same for all, so that a table of k of them
// is one chain of k entries, in buckets of eight. Comparing two of them is a
// unit (the generic case of compareM).
type ihKey struct{ id int }

func (k ihKey) String() string        { return "ihKey" }
func (k ihKey) Type() string          { return "ihKey" }
func (k ihKey) Freeze()               {}
func (k ihKey) Truth() Bool           { return True }
func (k ihKey) Hash() (uint32, error) { return 7, nil }
func (k ihKey) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	eq := k.id == y.(ihKey).id
	switch op {
	case syntax.EQL:
		return eq, nil
	case syntax.NEQ:
		return !eq, nil
	}
	return false, errors.New("ihKey: unordered")
}

// ihProbe is the walk of a lookup of the key with hash h in ht, from the text of
// the structure (buckets of entries in a chain): the buckets visited and the
// entries with the same hash that were compared, until the entry for which
// match is true (never: to the end of the chain).
func ihProbe(ht *hashtable, h uint32, match func(Value) bool) (buckets, compares uint64) {
	for p := &ht.table[h&uint32(len(ht.table)-1)]; p != nil; p = p.next {
		buckets++
		for i := range p.entries {
			e := &p.entries[i]
			if e.hash != h {
				continue
			}
			compares++
			if match != nil && match(e.key) {
				return
			}
		}
	}
	return
}

func ihCollidingTable(t *testing.T, k int) *hashtable {
	t.Helper()
	ht := new(hashtable)
	for i := 0; i < k; i++ {
		if err := ht.insert(ihKey{i}, None); err != nil {
			t.Fatal(err)
		}
	}
	if ht.len != uint32(k) {
		t.Fatalf("table has %d entries, want %d", ht.len, k)
	}
	return ht
}

var ihChainSizes = []int{1, 7, 8, 9, 20, 1024, 2048}

// TestIhHashtable_ProbeWork: lookup, delete and count of a key on a chain of k
// colliding keys charge the lookupWork of the table, a unit for each bucket of
// the chain walked and a unit for each key compared (all of them for a key that
// is not there, up to the match for one that is).
func TestIhHashtable_ProbeWork(t *testing.T) {
	for _, k := range ihChainSizes {
		// the chain really is ceil(k/8) buckets: the model of ihProbe is the
		// one of the comment of the test
		ht := ihCollidingTable(t, k)
		if b, c := ihProbe(ht, 7, nil); c != uint64(k) || b != uint64((k+7)/8) {
			t.Fatalf("k=%d: the chain has %d buckets and %d entries, want %d and %d", k, b, c, (k+7)/8, k)
		}
		lw := ihLookupWork(k)
		if k == 2048 && lw != 4 || k == 1024 && lw != 2 || k < 1024 && lw != 0 {
			t.Fatalf("ihLookupWork(%d) = %d", k, lw)
		}

		// lookup of a key that is not there, and of the first and last key
		for _, id := range []int{-1, 0, k - 1} {
			want := lw
			b, c := ihProbe(ht, 7, func(v Value) bool { return v.(ihKey).id == id })
			want += b + c
			got := ihMeterWork(t, func(m *meter) error {
				_, found, err := ht.lookupM(m, ihKey{id})
				if found != (id >= 0) {
					t.Errorf("k=%d id=%d: found = %v", k, id, found)
				}
				return err
			})
			ihCheck(t, "lookupM k="+ihItoa(k)+" id="+ihItoa(id+1), got, want)
		}

		// count: the bitsets of the table, then each key of the iterator
		// probes the whole chain (the keys that are there are not counted twice)
		other := []Value{ihKey{-1}, ihKey{-2}}
		b, c := ihProbe(ht, 7, nil)
		want := uint64(len(ht.table)) + 2*(lw+b+c)
		got := ihMeterWork(t, func(m *meter) error {
			it := NewList(other).Iterate()
			defer it.Done()
			n, err := ht.countM(m, it)
			if n != 0 {
				t.Errorf("k=%d: count = %d", k, n)
			}
			return err
		})
		ihCheck(t, "countM k="+ihItoa(k), got, want)

		// delete: of a key that is not there, then of the first, then of the
		// last (the model is walked before the entry is gone)
		for _, id := range []int{-1, 0, k - 1} {
			b, c := ihProbe(ht, 7, func(v Value) bool { return v.(ihKey).id == id })
			want := ihLookupWork(int(ht.len)) + b + c // (lookupWork of the table as it is before the entry goes)
			got := ihMeterWork(t, func(m *meter) error {
				_, found, err := ht.deleteM(m, ihKey{id})
				if found != (id >= 0) {
					t.Errorf("k=%d id=%d: deleted = %v", k, id, found)
				}
				return err
			})
			ihCheck(t, "deleteM k="+ihItoa(k)+" id="+ihItoa(id+1), got, want)
			if k == 1 {
				break // the first is the last
			}
		}
	}
}

// TestIhHashtable_SetAndDictPaths: the same probes, through the set and dict
// of the host.
func TestIhHashtable_SetAndDictPaths(t *testing.T) {
	const k = 2048
	s := NewSet(0)
	for i := 0; i < k; i++ {
		if err := s.Insert(ihKey{i}); err != nil {
			t.Fatal(err)
		}
	}
	b, c := ihProbe(&s.ht, 7, nil)
	want := ihLookupWork(k) + b + c
	got := ihMeterWork(t, func(m *meter) error { _, err := s.hasM(m, ihKey{-1}); return err })
	ihCheck(t, "set.hasM", got, want)
	// s.deleteM of a key that is not there
	got = ihMeterWork(t, func(m *meter) error { _, err := s.deleteM(m, ihKey{-1}); return err })
	ihCheck(t, "set.deleteM", got, want)
	if want != 4+256+2048 {
		t.Fatalf("expected walk: %d, want %d", want, 4+256+2048)
	}
}

// ---- K3: the hash of keys ----

// TestIhHashM_BytesOfKeys: the hash of a string of 64 bytes or more is workFast
// of its length (a unit for each 64 bytes); under 64 bytes it is nothing; the
// hash of bytes is workFast of the length whatever it is.
func TestIhHashM_BytesOfKeys(t *testing.T) {
	str := func(n int) String { return String(make([]byte, n)) }
	for _, c := range []struct {
		n         int
		str, byts uint64
	}{
		{0, 0, 0}, {63, 0, 0}, {64, 1, 1}, {65, 1, 1}, {127, 1, 1}, {128, 2, 2}, {10000, 156, 156},
	} {
		got := ihMeterWork(t, func(m *meter) error { _, err := hashM(m, str(c.n)); return err })
		ihCheck(t, "hashM(string) len="+ihItoa(c.n), got, c.str)
		got = ihMeterWork(t, func(m *meter) error { _, err := hashM(m, Bytes(str(c.n))); return err })
		ihCheck(t, "hashM(bytes) len="+ihItoa(c.n), got, c.byts)
		if ihFast(c.n) != c.byts {
			t.Fatalf("table: %d", c.n)
		}
	}
}

// TestIhHashM_BigInt: the hash of a big integer is its words of 64 bits; a small
// one is free.
func TestIhHashM_BigInt(t *testing.T) {
	two := func(e uint) Int { return MakeBigInt(new(big.Int).Lsh(big.NewInt(1), e)) }
	for _, c := range []struct {
		x    Int
		want uint64
	}{
		{MakeInt(5), 0},
		{two(30), 0},    // a small integer (int32 range)
		{two(62), 1},    // a big one of a word
		{two(5000), 79}, // 5001 bits
		{two(64*200 - 1), 200},
		{two(64 * 200), 201},
	} {
		got := ihMeterWork(t, func(m *meter) error { _, err := hashM(m, c.x); return err })
		ihCheck(t, "hashM(int) of "+ihItoa(int(c.want))+" words", got, c.want)
	}
}

// TestIhLookup_StringKey: lookup of a string key of n bytes in a table of that
// key: the hash (workFast(n) from 64 bytes), the bucket (1), and the comparison
// of the key found (workFast(n) from 64 bytes: both strings are that long).
func TestIhLookup_StringKey(t *testing.T) {
	for _, c := range []struct {
		n    int
		want uint64
	}{
		{1, 1}, {63, 1}, {64, 1 + 1 + 1}, {128, 2 + 1 + 2}, {10000, 156 + 1 + 156},
	} {
		key := String(make([]byte, c.n))
		ht := new(hashtable)
		if err := ht.insert(key, None); err != nil {
			t.Fatal(err)
		}
		got := ihMeterWork(t, func(m *meter) error {
			_, found, err := ht.lookupM(m, String(append([]byte(nil), key...)))
			if !found {
				t.Errorf("n=%d: not found", c.n)
			}
			return err
		})
		ihCheck(t, "lookupM string n="+ihItoa(c.n), got, c.want)
	}
}

// ---- K3: comparison ----

// TestIhCompare_Strings: == and != of two strings of the same length n are
// workFast(n) (and of strings of different lengths nothing); < is workFast of the
// shorter; the same through equalFast (the pairs of a table) for 64 bytes and
// more.
func TestIhCompare_Strings(t *testing.T) {
	str := func(n int) String { return String(make([]byte, n)) }
	cmp := func(op syntax.Token, x, y Value) uint64 {
		return ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, x, y, CompareLimit); return err })
	}
	for _, n := range []int{63, 64, 128, 10000} {
		for _, op := range []syntax.Token{syntax.EQL, syntax.NEQ} {
			ihCheck(t, "string "+op.String()+" n="+ihItoa(n), cmp(op, str(n), str(n)), ihFast(n))
			ihCheck(t, "string "+op.String()+" lengths differ", cmp(op, str(n), str(n+1)), 0)
		}
		// equalFast
		got := ihMeterWork(t, func(m *meter) error { _, err := equalFast(m, str(n), str(n), CompareLimit); return err })
		want := uint64(0)
		if n >= 64 {
			want = ihFast(n)
		}
		ihCheck(t, "equalFast string n="+ihItoa(n), got, want)
		got = ihMeterWork(t, func(m *meter) error { _, err := equalFast(m, str(n), str(n+1), CompareLimit); return err })
		ihCheck(t, "equalFast strings of different lengths", got, 0)
	}
	// ordered: the shorter
	ihCheck(t, "string < 10000 vs 200", cmp(syntax.LT, str(10000), str(200)), ihFast(200))
	ihCheck(t, "string < 200 vs 10000", cmp(syntax.LT, str(200), str(10000)), ihFast(200))
	ihCheck(t, "string <= 128 vs 128", cmp(syntax.LE, str(128), str(128)), 2)
	ihCheck(t, "string > 10000 vs 10000", cmp(syntax.GT, str(10000), str(10000)), 156)
}

// TestIhCompare_Bytes: every comparison of two bytes values is workFast of the
// shorter, equal lengths or not.
func TestIhCompare_Bytes(t *testing.T) {
	by := func(n int) Bytes { return Bytes(make([]byte, n)) }
	cmp := func(op syntax.Token, x, y Value) uint64 {
		return ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, x, y, CompareLimit); return err })
	}
	for _, op := range []syntax.Token{syntax.EQL, syntax.NEQ, syntax.LT, syntax.GE} {
		ihCheck(t, "bytes "+op.String()+" 128/128", cmp(op, by(128), by(128)), 2)
		ihCheck(t, "bytes "+op.String()+" 10000/10000", cmp(op, by(10000), by(10000)), 156)
		ihCheck(t, "bytes "+op.String()+" 10000/200", cmp(op, by(10000), by(200)), 3)
		ihCheck(t, "bytes "+op.String()+" 200/10000", cmp(op, by(200), by(10000)), 3)
		ihCheck(t, "bytes "+op.String()+" 63/63", cmp(op, by(63), by(63)), 0)
	}
}

// TestIhCompare_BigInts: a comparison of two integers, when the left one is
// big, is its words of 64 bits; a small left one is the words of the right one
// if that is big (G5-1: it was free, and a small key probing a chain of big
// ints was not charged), and free if both are small.
func TestIhCompare_BigInts(t *testing.T) {
	two := func(e uint) Int { return MakeBigInt(new(big.Int).Lsh(big.NewInt(1), e)) }
	for _, op := range []syntax.Token{syntax.EQL, syntax.LT, syntax.GE} {
		cmp := func(x, y Int) uint64 {
			return ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, x, y, CompareLimit); return err })
		}
		ihCheck(t, "2**5000 "+op.String()+" 2**5000", cmp(two(5000), two(5000)), 79)
		ihCheck(t, "2**5000 "+op.String()+" 2**100", cmp(two(5000), two(100)), 79)
		ihCheck(t, "2**100 "+op.String()+" 2**5000", cmp(two(100), two(5000)), 2)
		ihCheck(t, "2**5000 "+op.String()+" 7", cmp(two(5000), MakeInt(7)), 79)
		ihCheck(t, "7 "+op.String()+" 2**5000", cmp(MakeInt(7), two(5000)), 79) // (G5-1: the big one decides, on either side)
		ihCheck(t, "2**64000 "+op.String()+" 2**64000", cmp(two(64*1000), two(64*1000)), 1001)
	}
}

// TestIhCompare_OtherType: two values of a type that is not built in (the
// generic case) are a unit.
func TestIhCompare_OtherType(t *testing.T) {
	for _, op := range []syntax.Token{syntax.EQL, syntax.NEQ} {
		got := ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, ihKey{1}, ihKey{2}, CompareLimit); return err })
		ihCheck(t, "generic "+op.String(), got, 1)
	}
}

// TestIhCompare_Tuples: two equal tuples of n small integers are n units (the
// pairs are charged a chunk of 256 at a time, before they are compared).
func TestIhCompare_Tuples(t *testing.T) {
	mk := func(n int) Tuple {
		t := make(Tuple, n)
		for i := range t {
			t[i] = MakeInt(i)
		}
		return t
	}
	for _, n := range []int{1, 10, 256, 257, 1000} {
		for _, op := range []syntax.Token{syntax.EQL, syntax.LE} {
			got := ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, mk(n), mk(n), CompareLimit); return err })
			ihCheck(t, "tuple "+op.String()+" n="+ihItoa(n), got, uint64(n))
		}
	}
}

// TestIhCompare_DictsAndSets: the equality of two dicts of n small integer keys
// is, for each entry of the first, 11 units, the lookup of its key in the
// second (lookupWork, the buckets walked to the key; the keys that are small
// integers cost nothing to hash or compare), and its value (a small integer:
// nothing). The equality of two sets is, for each element, a unit and the
// lookup of the element in the second.
func TestIhCompare_DictsAndSets(t *testing.T) {
	for _, n := range []int{1, 3, 7, 100, 3000} {
		mk := func(set bool) (Value, Value) {
			if set {
				x, y := NewSet(0), NewSet(0)
				for i := 0; i < n; i++ {
					if err := x.Insert(MakeInt(i)); err != nil {
						t.Fatal(err)
					}
					if err := y.Insert(MakeInt(i)); err != nil {
						t.Fatal(err)
					}
				}
				return x, y
			}
			x, y := NewDict(0), NewDict(0)
			for i := 0; i < n; i++ {
				if err := x.SetKey(MakeInt(i), MakeInt(i)); err != nil {
					t.Fatal(err)
				}
				if err := y.SetKey(MakeInt(i), MakeInt(i)); err != nil {
					t.Fatal(err)
				}
			}
			return x, y
		}
		// the lookups of the keys of x in y: model of the walk
		walk := func(ht *hashtable) uint64 {
			var total uint64
			for e := ht.head; e != nil; e = e.next {
				key := e.key
				b, _ := ihProbe(ht, e.hash, func(v Value) bool { return v == key })
				total += ihLookupWork(int(ht.len)) + b
			}
			return total
		}
		x, y := mk(false)
		for _, op := range []syntax.Token{syntax.EQL, syntax.NEQ} {
			got := ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, x, y, CompareLimit); return err })
			ihCheck(t, "dict "+op.String()+" n="+ihItoa(n), got, 11*uint64(n)+walk(&y.(*Dict).ht))
		}
		xs, ys := mk(true)
		for _, op := range []syntax.Token{syntax.EQL, syntax.NEQ} {
			got := ihMeterWork(t, func(m *meter) error { _, err := compareM(m, op, xs, ys, CompareLimit); return err })
			ihCheck(t, "set "+op.String()+" n="+ihItoa(n), got, uint64(n)+walk(&ys.(*Set).ht))
		}
	}
	// dicts of different sizes are free
	a, b := NewDict(0), NewDict(0)
	_ = a.SetKey(MakeInt(1), None)
	ihCheck(t, "dicts of different lengths", ihMeterWork(t, func(m *meter) error { _, err := compareM(m, syntax.EQL, a, b, CompareLimit); return err }), 0)
}

// ---- K3: x in list is charged a chunk at a time ----

// ihCounted is a Value that counts the comparisons that are made with it.
type ihCounted struct{ n *int }

func (c ihCounted) String() string        { return "ihCounted" }
func (c ihCounted) Type() string          { return "ihCounted" }
func (c ihCounted) Freeze()               {}
func (c ihCounted) Truth() Bool           { return True }
func (c ihCounted) Hash() (uint32, error) { return 0, errors.New("unhashable") }
func (c ihCounted) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	*c.n++
	return false, nil
}

// TestIhMembership_ChargedAChunkAtATime: `x in l` of a list of 100000 elements
// under a limit of 3000 units of work is refused, with a *WorkBudgetError, after
// the comparisons that the limit pays for, and not at the first chunk (a charge
// of the whole list at once, which the refusal of a list that is longer than the
// limit would do before any comparison) and not at the end of the list.
//
// The work: a chunk of 256 is charged before it is scanned (256 units), and each
// comparison of a value of another type is a unit; the meter charges what it has
// every 1024 units, and the third charge of 1024 does not fit in 3000
// (2048 + 1024 > 3000). The units after c comparisons are
// c + 256*ceil(c/256) (the chunk of the comparison c included), so the third
// flush is at the comparison 1536 (units 1536 + 6*256 = 3072, 1024 over the
// 2048 charged), refused before that comparison is made: 1535 comparisons.
func TestIhMembership_ChargedAChunkAtATime(t *testing.T) {
	const limit, length = 3000, 100000
	var n int
	elems := make([]Value, length)
	for i := range elems {
		elems[i] = ihCounted{&n}
	}
	th := &Thread{Name: "ih"}
	th.SetMaxWork(limit)
	m := th.meter()
	_, err := hasElems(&m, elems, ihCounted{&n})
	var be *WorkBudgetError
	if !errors.As(err, &be) {
		t.Fatalf("hasElems: err = %v, want a *WorkBudgetError", err)
	}
	if n != 1535 {
		t.Errorf("comparisons made before the refusal: %d, want 1535", n)
	}
	if got := th.Work(); got != limit {
		t.Errorf("Work() after the refusal = %d, want the limit %d", got, limit)
	}

	// the same list, without a limit: every chunk and every comparison, 2 units
	// for each element
	n = 0
	th = &Thread{Name: "ih"}
	m = th.meter()
	if _, err := hasElems(&m, elems[:1000], ihCounted{&n}); err != nil {
		t.Fatal(err)
	}
	if n != 1000 || th.Work() != 2000 {
		t.Errorf("1000 elements: %d comparisons and %d units, want 1000 and 2000", n, th.Work())
	}
}

// ---- the guard: a meter that adds is flushed ----

// ihFlushExceptions are the functions that make a meter, add to it and do not
// flush it themselves, with the reason each (the key is "dir/file.go:Func").
var ihFlushExceptions = map[string]string{}

// ihUnflushed reports whether the function makes a meter (a call of a method
// meter(), or a composite literal of the type meter of this package or workMeter
// of lib/json) and calls add, and does not call flush (both packages call the
// method of the meter flush).
func ihUnflushed(fd *ast.FuncDecl) bool {
	var makes, adds, flushes bool
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "meter":
					makes = true
				case "add":
					adds = true
				case "flush":
					flushes = true
				}
			}
		case *ast.CompositeLit:
			if id, ok := n.Type.(*ast.Ident); ok && (id.Name == "meter" || id.Name == "workMeter") {
				makes = true
			}
		}
		return true
	})
	return makes && adds && !flushes
}

// TestIhGuard_DetectorSelfTest: the detector flags, on source that is parsed in
// the test, the functions of the shapes of the two packages that add without
// flushing, and not those that flush or that make no meter.
func TestIhGuard_DetectorSelfTest(t *testing.T) {
	const src = `package p
func starlarkShapeBad(th *Thread) { m := th.meter(); m.add(1) }
func starlarkShapeOK(th *Thread) { m := th.meter(); m.add(1); m.flush() }
func literalShapeBad(th *Thread) { m := meter{thread: th}; m.add(1) }
func jsonShapeBad(th *Thread) { work := workMeter{th: th}; work.add(1) }
func jsonShapeOK(th *Thread) { work := workMeter{th: th}; work.add(1); work.flush() }
func receivesMeter(m *meter) { m.add(1) }
func makesOnly(th *Thread) { m := th.meter(); _ = m }
`
	f, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"starlarkShapeBad": true, "literalShapeBad": true, "jsonShapeBad": true}
	for _, d := range f.Decls {
		fd := d.(*ast.FuncDecl)
		if got := ihUnflushed(fd); got != want[fd.Name.Name] {
			t.Errorf("ihUnflushed(%s) = %v, want %v", fd.Name.Name, got, want[fd.Name.Name])
		}
	}
}

// TestIhGuard_EveryMeterThatAddsIsFlushed: a function (non-test, in starlark and
// lib/json) that makes a meter (thread.meter() or meter{...}) and calls add on
// something must call flush too, else the units under a chunk of flushWork that
// are left when it ends are never charged. A new function that does not is
// listed in ihFlushExceptions with the reason, or fixed.
func TestIhGuard_EveryMeterThatAddsIsFlushed(t *testing.T) {
	seen := map[string]bool{}
	for _, dir := range []string{".", "../lib/json"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			for fname, file := range pkg.Files {
				for _, d := range file.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					if !ihUnflushed(fd) {
						continue
					}
					key := filepath.Base(dir) + "/" + filepath.Base(fname) + ":" + fd.Name.Name
					if dir == "." {
						key = "starlark/" + filepath.Base(fname) + ":" + fd.Name.Name
					}
					seen[key] = true
					if _, ok := ihFlushExceptions[key]; !ok {
						t.Errorf("%s makes a meter and adds to it, and never flushes it", key)
					}
				}
			}
		}
	}
	for key := range ihFlushExceptions {
		if !seen[key] {
			t.Errorf("exception %s is not needed any more: remove it", key)
		}
	}
}
