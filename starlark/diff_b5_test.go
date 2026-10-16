package starlark_test

// A differential test against v0.2.0 of the results that the review of
// compatibility found changed without a declaration (B5): json.decode with a
// default and a long integer, the set methods that take an iterable with an
// unhashable element on an empty receiver, and the depth at which a comparison,
// a hash table lookup and the equality of dicts and sets refuse a nested key.
//
// The expectations of v0.2.0 are in testdata/diff_b5_v020.golden, produced by
// running TestWriteB5Golden in a tree of v0.2.0 (d34a821).

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const goldenB5 = "testdata/diff_b5_v020.golden"

// nest wraps leaf in k levels of the container kind.
func nest(kind string, k int, leaf string) string {
	s := leaf
	for i := 0; i < k; i++ {
		switch kind {
		case "tuple":
			s = "(" + s + ",)"
		case "list":
			s = "[" + s + "]"
		case "dict":
			s = "{'k': " + s + "}"
		}
	}
	return s
}

func b5Corpus() []string {
	var out []string
	// ---- json.decode(s, default=d) with a long integer ----
	digits := func(n int) string { return strings.Repeat("9", n) }
	// (an integer of more than 4300 digits is refused in this version, on
	// purpose: those are cases of the Compatibility list, and a float or an
	// exponent that fails to parse is an "invalid number" whose operand is cut
	// above 256 bytes, like every operand of a message)
	for _, n := range []int{10, 100, 4299, 4300} {
		d := digits(n)
		for _, def := range []string{"", ", default=7", ", default=None", ", default={}"} {
			out = append(out,
				"result = json.decode('"+d+"'"+def+")\n",
				"result = json.decode('-"+d+"'"+def+")\n",
				"result = json.decode('["+d+", 1]'"+def+")\n",
				"result = json.decode('{\"a\": "+d+"}'"+def+")\n",
				"result = str(json.decode('"+d+"'"+def+"))[:5]\n",
			)
			if n <= 100 {
				out = append(out,
					"result = json.decode('"+d+".5'"+def+")\n",
					"result = json.decode('"+d+"e1'"+def+")\n",
				)
			}
		}
	}
	out = append(out,
		"result = json.decode('{\"a\": [1, 2', default=5)\n",
		"result = json.decode('', default=5)\n",
		"result = json.decode('[1, 2]', default=5)\n",
		"result = json.decode('nul', default=5)\n",
	)
	// ---- the set methods and operators with unhashable elements ----
	recv := []string{"set()", "set([1])", "set([1, 2, 3])"}
	args := []string{"[[1]]", "[1, [1]]", "[[1], 1]", "[[], []]", "[{}]", "[{'a': 1}, 2]", "[(1, [2])]", "[]", "[1, 2]", "{}", "{'a': 1}", "'ab'", "[set([1])]", "[range(3)]", "[(1, 2), (1, [])]"}
	for _, r := range recv {
		for _, m := range []string{"union", "intersection", "difference", "symmetric_difference", "issubset", "issuperset", "update", "intersection_update", "difference_update", "symmetric_difference_update"} {
			for _, a := range args {
				out = append(out, fmt.Sprintf("s = %s\nresult = s.%s(%s)\nresult = (result, s)\n", r, m, a))
			}
		}
		for _, a := range args {
			out = append(out,
				fmt.Sprintf("s = %s\nresult = s.add(%s)\n", r, a),
				fmt.Sprintf("s = %s\nresult = s.remove(%s)\n", r, a),
				fmt.Sprintf("s = %s\nresult = s.discard(%s)\n", r, a),
				fmt.Sprintf("s = %s\nresult = %s in s\n", r, a),
				fmt.Sprintf("s = %s\nresult = set(%s)\n", r, a),
			)
		}
	}
	for _, r := range recv {
		for _, o := range recv {
			out = append(out,
				fmt.Sprintf("result = %s ^ %s\n", r, o),
				fmt.Sprintf("result = %s | %s\n", r, o),
				fmt.Sprintf("result = %s & %s\n", r, o),
				fmt.Sprintf("result = %s - %s\n", r, o),
				fmt.Sprintf("s = %s\ns ^= %s\nresult = s\n", r, o),
			)
		}
	}
	// ---- depth of comparison, tables, equality of dicts and sets ----
	leaves := []string{"1", "'a'", "1.5", "None", "True", "(1, 2)", "[]", "{}", "'" + strings.Repeat("x", 100) + "'", "(1 << 70)"}
	ops := []string{"==", "!=", "<", "<=", ">", ">="}
	for _, kind := range []string{"tuple", "list"} {
		for k := 0; k <= 14; k++ {
			for _, leaf := range leaves {
				a, b := nest(kind, k, leaf), nest(kind, k, leaf)
				c := nest(kind, k, "2")
				for _, op := range ops {
					out = append(out, fmt.Sprintf("result = %s %s %s\n", a, op, b))
				}
				out = append(out,
					fmt.Sprintf("result = %s == %s\n", a, c),
					fmt.Sprintf("result = %s < %s\n", a, c),
					fmt.Sprintf("result = %s in [%s, %s]\n", a, c, b),
					fmt.Sprintf("result = [%s, %s].index(%s)\n", c, b, a),
					fmt.Sprintf("result = sorted([%s, %s])\n", a, c),
					fmt.Sprintf("result = max([%s, %s])\n", a, c),
					fmt.Sprintf("result = min([%s, %s])\n", a, c),
				)
			}
		}
	}
	for k := 0; k <= 14; k++ {
		for _, leaf := range []string{"1", "'a'", "None", "(1, 2)"} {
			t := nest("tuple", k, leaf)
			u := nest("tuple", k, leaf)
			v := nest("tuple", k, "2")
			out = append(out,
				// tuple keys: lookup, insertion, deletion, equality of tables
				fmt.Sprintf("d = {%s: 1}\nresult = d[%s]\n", t, u),
				fmt.Sprintf("d = {%s: 1}\nresult = d.get(%s, 0)\n", t, u),
				fmt.Sprintf("d = {%s: 1}\nresult = %s in d\n", t, u),
				fmt.Sprintf("d = {%s: 1}\nresult = %s in d\n", t, v),
				fmt.Sprintf("d = {%s: 1}\nd[%s] = 2\nresult = d\n", t, u),
				fmt.Sprintf("d = {%s: 1}\nresult = d.pop(%s)\n", t, u),
				fmt.Sprintf("d = {%s: 1}\ne = {%s: 1}\nresult = d == e\n", t, u),
				fmt.Sprintf("d = {%s: 1}\ne = {%s: 1}\nresult = d != e\n", t, u),
				fmt.Sprintf("d = {%s: 1}\ne = {%s: 1}\nresult = d == e\n", t, v),
				fmt.Sprintf("d = {%s: 1}\ne = {%s: 2}\nresult = d == e\n", t, u),
				fmt.Sprintf("d = {%s: 1}\ne = {%s: 1}\nresult = d | e\n", t, u),
				fmt.Sprintf("d = {%s: 1}\ne = dict(d)\nresult = (d == e, len(e))\n", t),
				fmt.Sprintf("s = set([%s])\nt = set([%s])\nresult = s == t\n", t, u),
				fmt.Sprintf("s = set([%s])\nt = set([%s])\nresult = s == t\n", t, v),
				fmt.Sprintf("s = set([%s])\nt = set([%s])\nresult = (s <= t, s < t, s >= t, s | t, s & t, s - t, s ^ t)\n", t, u),
				fmt.Sprintf("s = set([%s])\nresult = %s in s\n", t, u),
				fmt.Sprintf("s = set([%s])\nresult = len(set([%s, %s]))\n", t, u, v),
				fmt.Sprintf("s = set([%s])\ns.add(%s)\nresult = len(s)\n", t, u),
				fmt.Sprintf("s = set([%s])\ns.remove(%s)\nresult = len(s)\n", t, u),
				fmt.Sprintf("result = {%s: 1, %s: 2}\n", t, u),
				fmt.Sprintf("result = hash(%s) == hash(%s) if False else 0\n", t, u),
				// the value of a dict, nested: d == e compares values
				fmt.Sprintf("d = {'a': %s}\ne = {'a': %s}\nresult = d == e\n", nest("dict", k, leaf), nest("dict", k, leaf)),
				fmt.Sprintf("d = {'a': %s}\ne = {'a': %s}\nresult = d == e\n", nest("dict", k, leaf), nest("dict", k, "2")),
				fmt.Sprintf("result = %s == %s\n", nest("dict", k, leaf), nest("dict", k, leaf)),
				fmt.Sprintf("result = %s == %s\n", nest("dict", k, leaf), nest("dict", k, "2")),
			)
		}
	}
	// two nestings of different depth, and mixed kinds
	for a := 0; a <= 12; a++ {
		for b := 0; b <= 12; b++ {
			out = append(out,
				fmt.Sprintf("result = %s == %s\n", nest("tuple", a, "1"), nest("tuple", b, "1")),
				fmt.Sprintf("result = %s < %s\n", nest("tuple", a, "1"), nest("tuple", b, "1")),
				fmt.Sprintf("result = %s == %s\n", nest("list", a, "1"), nest("tuple", b, "1")),
				fmt.Sprintf("d = {%s: 1}\nresult = %s in d\n", nest("tuple", a, "'s'"), nest("tuple", b, "'s'")),
			)
		}
	}
	return uniq(out)
}

// TestWriteB5Golden writes the golden file. It is run in a tree of v0.2.0, not
// here: STARLARK_WRITE_GOLDEN=<directory of the tree's starlark package>.
func TestWriteB5Golden(t *testing.T) {
	dir := os.Getenv("STARLARK_WRITE_GOLDEN")
	if dir == "" {
		t.Skip("set STARLARK_WRITE_GOLDEN to write the golden file (in a tree of v0.2.0)")
	}
	writeGolden(t, dir+"/"+goldenB5, b5Corpus())
}

// TestDifferentialB5 lists the programs of B5 that differ from v0.2.0 (result,
// error text, prints, steps).
func TestDifferentialB5(t *testing.T) {
	golden := readGolden(t, goldenB5)
	if len(golden) < 2000 {
		t.Fatalf("the golden file has %d programs", len(golden))
	}
	var bad []string
	nbad := 0
	classes := map[string]int{}
	for _, g := range golden {
		got := runDiffProgram(g.Src)
		if sameOutcome(g, got) {
			continue
		}
		nbad++
		// (one example of each class: the program and the two answers, with
		// the long runs of one byte squeezed)
		line := fmt.Sprintf("%.200q\n   v0.2.0: %s %.100q steps=%d\n   now:    %s %.100q steps=%d", squeeze(g.Src), g.Status, squeeze(g.Err+g.Result), g.Steps, got.Status, squeeze(got.Err+got.Result), got.Steps)
		if classes[line[:strings.Index(line, "\n")]]++; classes[line[:strings.Index(line, "\n")]] == 1 && len(bad) < 60 {
			bad = append(bad, line)
		}
	}
	if nbad > 0 {
		t.Fatalf("%d of %d programs differ from v0.2.0, in %d classes:\n%s", nbad, len(golden), len(classes), strings.Join(bad, "\n"))
	}
}

// squeeze replaces the runs of more than 8 equal bytes by <byte x count>.
func squeeze(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] == s[i] {
			j++
		}
		if j-i > 8 {
			fmt.Fprintf(&b, "<%c x%d>", s[i], j-i)
		} else {
			b.WriteString(s[i:j])
		}
		i = j
	}
	return b.String()
}
