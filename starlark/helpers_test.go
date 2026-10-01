package starlark

// longestChain is the number of buckets of the longest chain of a table.
func longestChain(ht *hashtable) int {
	longest := 0
	for i := range ht.table {
		n := 0
		for p := &ht.table[i]; p != nil; p = p.next {
			n++
		}
		longest = max(longest, n)
	}
	return longest
}
