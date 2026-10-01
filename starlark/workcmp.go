package starlark

import (
	"fmt"
	"math"
	"strings"

	"go.starlark.net/syntax"
)

// Hashing and comparison that charge their work to a meter (see work.go).
//
// The exported Hash, Equal, Compare and CompareDepth do the same without a
// meter (a nil meter does nothing). The built-in types are handled here, with
// the work of each leaf and each visited element charged; a value of another
// type is one unit and its own CompareSameType or Hash.

// hashM is the Hash of k, charging to m the work it does: the bytes of a
// string, the elements of a tuple (each visited: a DAG t = (t, t) repeated is
// exponential in its nodes, and is stopped by the steps charged), the words of
// a big integer.
func hashM(m *meter, k Value) (uint32, error) {
	switch k := k.(type) {
	case String:
		if len(k) >= 64 { // under 64 bytes the hash is under a unit
			if err := m.add(workFast(len(k))); err != nil {
				return 0, err
			}
		}
		return hashString(string(k)), nil
	case Bytes:
		if err := m.add(workFast(len(k))); err != nil {
			return 0, err
		}
		return hashString(string(k)), nil
	case Tuple:
		return k.hashM(m, 0)
	case Int:
		if _, big := k.get(); big != nil {
			if err := m.add(uint64(len(big.Bits()))); err != nil {
				return 0, err
			}
		}
	}
	return k.Hash()
}

// hashM is Tuple.Hash at the given nesting depth, charging m one unit for each
// element visited.
func (t Tuple) hashM(m *meter, depth int) (uint32, error) {
	if depth > MaxValueDepth {
		return 0, fmt.Errorf("tuple is nested more than %d levels deep", MaxValueDepth)
	}
	// Use same algorithm as Python.
	var x, mult uint32 = 0x345678, 1000003
	for _, elem := range t {
		if err := m.add(1); err != nil {
			return 0, err
		}
		var y uint32
		var err error
		switch et := elem.(type) {
		case Tuple:
			y, err = et.hashM(m, depth+1)
		case String, Bytes, Int:
			y, err = hashM(m, elem)
		default:
			y, err = elem.Hash()
		}
		if err != nil {
			return 0, err
		}
		x = x ^ y*mult
		mult += 82520 + uint32(len(t)+len(t))
	}
	return x, nil
}

// equalFast is equalM for the pairs that a hash table and a membership test
// compare most: two strings, two small ints. The work of the rest is in equalM.
func equalFast(m *meter, x, y Value, depth int) (bool, error) {
	switch x := x.(type) {
	case String:
		if y, ok := y.(String); ok {
			if len(x) >= 64 && len(x) == len(y) {
				if err := m.add(workFast(len(x))); err != nil {
					return false, err
				}
			}
			return x == y, nil
		}
	case Int:
		if y, ok := y.(Int); ok {
			if xs, xb := x.get(); xb == nil {
				if ys, yb := y.get(); yb == nil {
					return xs == ys, nil
				}
			}
		}
	}
	return equalM(m, x, y, depth)
}

// equalM is EqualDepth, charging its work to m.
func equalM(m *meter, x, y Value, depth int) (bool, error) {
	return compareM(m, syntax.EQL, x, y, depth)
}

// compareM is CompareDepth, charging its work to m: each pair of leaves
// compared, each element of a list, tuple, dict or set visited, the bytes of
// two strings. The comparison of nested data stops as soon as the steps are
// used up, so that a wide and deep structure (width^depth leaves) is bounded.
func compareM(m *meter, op syntax.Token, x, y Value, depth int) (bool, error) {
	if depth < 1 {
		return false, fmt.Errorf("comparison exceeded maximum recursion depth")
	}
	// The common leaves first: small ints, and strings (the keys of a table).
	switch x := x.(type) {
	case String:
		if y, ok := y.(String); ok && (op == syntax.EQL || op == syntax.NEQ) {
			if len(x) == len(y) {
				if err := m.add(workFast(len(x))); err != nil {
					return false, err
				}
			}
			return (x == y) == (op == syntax.EQL), nil
		}
	case Int:
		if y, ok := y.(Int); ok {
			if xs, xb := x.get(); xb == nil {
				if ys, yb := y.get(); yb == nil {
					return threeway(op, signum64(xs-ys)), nil // safe: int32 operands
				}
			}
		}
	}
	if sameType(x, y) {
		switch x := x.(type) {
		case String:
			if y, ok := y.(String); ok {
				if len(x) == len(y) || (op != syntax.EQL && op != syntax.NEQ) {
					if err := m.add(workFast(min(len(x), len(y)))); err != nil {
						return false, err
					}
				}
				return threeway(op, strings.Compare(string(x), string(y))), nil
			}
		case Bytes:
			if y, ok := y.(Bytes); ok {
				if err := m.add(workFast(min(len(x), len(y)))); err != nil {
					return false, err
				}
				return threeway(op, strings.Compare(string(x), string(y))), nil
			}
		case *List:
			if y, ok := y.(*List); ok {
				// It's tempting to check x == y as an optimization here,
				// but wrong because a list containing NaN is not equal to itself.
				return sliceCompareM(m, op, x.elems, y.elems, depth)
			}
		case Tuple:
			if y, ok := y.(Tuple); ok {
				return sliceCompareM(m, op, x, y, depth)
			}
		case *Dict:
			if y, ok := y.(*Dict); ok {
				switch op {
				case syntax.EQL:
					return dictsEqualM(m, x, y, depth)
				case syntax.NEQ:
					ok, err := dictsEqualM(m, x, y, depth)
					return !ok, err
				}
				return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
			}
		case *Set:
			if y, ok := y.(*Set); ok {
				return setCompareM(m, op, x, y, depth)
			}
		case Int:
			if y, ok := y.(Int); ok {
				if _, big := x.get(); big != nil {
					if err := m.add(uint64(len(big.Bits()))); err != nil {
						return false, err
					}
				}
				t, err := x.Cmp(y, depth)
				if err != nil {
					return false, err
				}
				return threeway(op, t), nil
			}
		}

		if err := m.add(1); err != nil {
			return false, err
		}
		if xcomp, ok := x.(Comparable); ok {
			return xcomp.CompareSameType(op, y, depth)
		}

		if xcomp, ok := x.(TotallyOrdered); ok {
			t, err := xcomp.Cmp(y, depth)
			if err != nil {
				return false, err
			}
			return threeway(op, t), nil
		}

		// use identity comparison
		switch op {
		case syntax.EQL:
			return x == y, nil
		case syntax.NEQ:
			return x != y, nil
		}
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}

	// different types

	// int/float ordered comparisons
	switch x := x.(type) {
	case Int:
		if y, ok := y.(Float); ok {
			var cmp int
			if y != y {
				cmp = -1 // y is NaN
			} else if !math.IsInf(float64(y), 0) {
				cmp = x.rational().Cmp(y.rational()) // y is finite
			} else if y > 0 {
				cmp = -1 // y is +Inf
			} else {
				cmp = +1 // y is -Inf
			}
			return threeway(op, cmp), nil
		}
	case Float:
		if y, ok := y.(Int); ok {
			var cmp int
			if x != x {
				cmp = +1 // x is NaN
			} else if !math.IsInf(float64(x), 0) {
				cmp = x.rational().Cmp(y.rational()) // x is finite
			} else if x > 0 {
				cmp = +1 // x is +Inf
			} else {
				cmp = -1 // x is -Inf
			}
			return threeway(op, cmp), nil
		}
	}

	// All other values of different types compare unequal.
	switch op {
	case syntax.EQL:
		return false, nil
	case syntax.NEQ:
		return true, nil
	}
	return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
}

func sliceCompareM(m *meter, op syntax.Token, x, y []Value, depth int) (bool, error) {
	// Fast path: check length.
	if len(x) != len(y) && (op == syntax.EQL || op == syntax.NEQ) {
		return op == syntax.NEQ, nil
	}

	// Find first element that is not equal in both lists.
	for i := 0; i < len(x) && i < len(y); i++ {
		if i&255 == 0 { // the pairs of a chunk are charged before they are compared
			if err := m.add(uint64(min(256, len(x)-i, len(y)-i))); err != nil {
				return false, err
			}
		}
		if eq, err := equalFast(m, x[i], y[i], depth-1); err != nil {
			return false, err
		} else if !eq {
			switch op {
			case syntax.EQL:
				return false, nil
			case syntax.NEQ:
				return true, nil
			default:
				return compareM(m, op, x[i], y[i], depth-1)
			}
		}
	}

	return threeway(op, len(x)-len(y)), nil
}

func dictsEqualM(m *meter, x, y *Dict, depth int) (bool, error) {
	if x.Len() != y.Len() {
		return false, nil
	}
	for e := x.ht.head; e != nil; e = e.next {
		key, xval := e.key, e.value
		if err := m.add(1); err != nil { // one entry
			return false, err
		}
		yval, found, err := y.ht.lookupM(m, key)
		if err != nil {
			// (the key is hashable: it is a key of x; an error is the work limit)
			if m != nil && m.thread != nil {
				return false, err
			}
		}
		if !found {
			return false, nil
		} else if eq, err := compareM(m, syntax.EQL, xval, yval, depth-1); err != nil {
			return false, err
		} else if !eq {
			return false, nil
		}
	}
	return true, nil
}

func setsEqualM(m *meter, x, y *Set) (bool, error) {
	if x.Len() != y.Len() {
		return false, nil
	}
	for e := x.ht.head; e != nil; e = e.next {
		if err := m.add(1); err != nil {
			return false, err
		}
		found, err := y.hasM(m, e.key)
		if err != nil {
			if m != nil && m.thread != nil {
				return false, err
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func setCompareM(m *meter, op syntax.Token, x, y *Set, depth int) (bool, error) {
	switch op {
	case syntax.EQL:
		return setsEqualM(m, x, y)
	case syntax.NEQ:
		ok, err := setsEqualM(m, x, y)
		return !ok, err
	case syntax.GE: // superset
		if x.Len() < y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.isSupersetM(m, iter)
	case syntax.LE: // subset
		if x.Len() > y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.isSubsetM(m, iter)
	case syntax.GT: // proper superset
		if x.Len() <= y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.isSupersetM(m, iter)
	case syntax.LT: // proper subset
		if x.Len() >= y.Len() {
			return false, nil
		}
		iter := y.Iterate()
		defer iter.Done()
		return x.isSubsetM(m, iter)
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}
