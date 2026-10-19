package starlark

// Properties of the work and the bytes of operations on big integers and on
// long strings: the exact numbers, derived from the prices (a unit for each
// 64-bit word of the longer operand, five for making a big integer, and so on),
// not read off the code under test. Every number is asserted by equality, so a
// price that is dropped, halved or changed is a red test.

import (
	"fmt"
	"math/big"
	"testing"

	"go.starlark.net/syntax"
)

// isMeasure is the work (beyond the steps) and the bytes charged between
// reset() and stop() of a program.
type isMeasure struct{ work, alloc uint64 }

// isBig is the integer 2**(bits-1): of bits bits, so of (bits+63)/64 words.
func isBig(bits int) Int { return MakeBigInt(new(big.Int).Lsh(big.NewInt(1), uint(bits-1))) }

// isRun runs setup, then op, with the predeclared env, and returns what op was
// charged. The statement "r = x" is charged the same fixed overhead as any
// other statement of one value, so isRun subtracts that of the empty statement.
func isRun(t *testing.T, env StringDict, setup, op string) isMeasure {
	t.Helper()
	one := func(op string) isMeasure {
		th := &Thread{}
		var w0, s0, a0 uint64
		var got isMeasure
		pre := StringDict{
			"reset": NewBuiltin("reset", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
				w0, s0, a0 = th.Work(), th.Steps, th.AllocatedBytes()
				return None, nil
			}),
			"stop": NewBuiltin("stop", func(th *Thread, _ *Builtin, _ Tuple, _ []Tuple) (Value, error) {
				got = isMeasure{(th.Work() - w0) - (th.Steps - s0), th.AllocatedBytes() - a0}
				return None, nil
			}),
		}
		for k, v := range env {
			pre[k] = v
		}
		src := setup + "\nr = None\nreset()\n" + op + "\nstop()\n"
		if _, err := ExecFileOptions(&syntax.FileOptions{GlobalReassign: true, Set: true, While: true, TopLevelControl: true}, th, "t.star", src, pre); err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		return got
	}
	base, m := one("r = None"), one(op)
	return isMeasure{m.work - base.work, m.alloc - base.alloc}
}

// isCall returns what the call of fn with args and kwargs from Go is charged.
func isCall(t *testing.T, fn Value, args Tuple, kwargs []Tuple) isMeasure {
	t.Helper()
	th := &Thread{}
	w0, s0, a0 := th.Work(), th.Steps, th.AllocatedBytes()
	if _, err := Call(th, fn, args, kwargs); err != nil {
		t.Fatal(err)
	}
	return isMeasure{(th.Work() - w0) - (th.Steps - s0), th.AllocatedBytes() - a0}
}

const (
	isWordBytes  = 9  // the bytes of a big integer for each 64-bit word
	isIntBase    = 64 // the bytes of a big integer besides its words
	isMakeBigInt = 5  // the work of making a big integer
	isBaseCall   = 4  // the work of a call of a built-in method
)

// The binary and unary operations on big integers of 1, 64 and 1000 words are
// charged a unit for each word of the longer, and five for the integer that is
// made, and the integer is charged 64 + 9w bytes of w words.
func TestIsBigIntOps(t *testing.T) {
	for _, w := range []int{1, 64, 1000} {
		env := StringDict{
			"a": isBig(64 * w),      // w words
			"b": isBig(64*w - 1),    // w words (of one bit less)
			"s": MakeInt(5),         // small
			"m": MakeInt64(1 << 31), // the smallest that is not small (one word)
		}
		uw := uint64(w)
		for _, c := range []struct {
			op      string
			work    uint64 // beyond the steps
			words   uint64 // of the result, 0: no big integer is made
			comment string
		}{
			{"a - b", uw + isMakeBigInt, uw, "MINUS"},
			{"a - s", uw + isMakeBigInt, uw, "MINUS with a small operand"},
			{"s - a", uw + isMakeBigInt, uw, "MINUS, small on the left"},
			{"a | b", uw + isMakeBigInt, uw, "PIPE"},
			{"a | s", uw + isMakeBigInt, uw, "PIPE with a small operand"},
			{"a ^ b", uw + isMakeBigInt, uw, "CIRCUMFLEX"},
			{"a ^ s", uw + isMakeBigInt, uw, "CIRCUMFLEX with a small operand"},
			{"a & a", uw + isMakeBigInt, uw, "AMP, a big result"},
			{"a & b", uw, 0, "AMP, a small result (a & b == 0)"},
			{"a & s", uw, 0, "AMP, a small result (a & 5 == 0)"},
			{"a + b", uw + isMakeBigInt, uw, "PLUS"},
			{"-a", uw + isMakeBigInt, uw, "unary MINUS"},
			{"~a", uw + isMakeBigInt, uw, "TILDE"},
			{"a << 3", uw + isMakeBigInt, uw + 1, "shift left"},
			{"a >> 3", uw + isMakeBigInt, uw, "shift right"},
			{"a << 0", uw + isMakeBigInt, uw, "shift left by 0"},
			{"a >> 0", uw + isMakeBigInt, uw, "shift right by 0"},
		} {
			got := isRun(t, env, "", "r = "+c.op)
			wantBytes := uint64(0)
			if c.words != 0 {
				wantBytes = isIntBase + isWordBytes*c.words
			}
			if got.work != c.work || got.alloc != wantBytes {
				t.Errorf("w=%d %s (%s): work %d bytes %d, want work %d bytes %d", w, c.op, c.comment, got.work, got.alloc, c.work, wantBytes)
			}
		}
		// the comparisons are linear too (and make nothing)
		for _, op := range []string{"a == b", "a < b", "a >= b"} {
			if got := isRun(t, env, "", "r = "+op); got.work != uw || got.alloc != 0 {
				t.Errorf("w=%d %s: work %d bytes %d, want work %d bytes 0", w, op, got.work, got.alloc, uw)
			}
		}
	}
}

// The operation on two small integers whose result is not small makes a big
// integer of one word: five units, and 64 + 9 bytes.
func TestIsSmallOpsWithABigResult(t *testing.T) {
	env := StringDict{"x": MakeInt64(1<<31 - 1), "y": MakeInt64(-(1 << 31)), "two": MakeInt(2), "one": MakeInt(1)}
	for _, op := range []string{"x + one", "x - y", "y - one", "x * two", "y * two", "x << one", "-y"} {
		got := isRun(t, env, "", "r = "+op)
		if got.work != isMakeBigInt || got.alloc != isIntBase+isWordBytes {
			t.Errorf("%s: work %d bytes %d, want work %d bytes %d", op, got.work, got.alloc, isMakeBigInt, isIntBase+isWordBytes)
		}
	}
	// a result that is small is free
	if got := isRun(t, env, "", "r = x - one"); got.work != 0 || got.alloc != 0 {
		t.Errorf("a small result: work %d bytes %d, want none", got.work, got.alloc)
	}
}

// "%d" of a big integer is charged d/2 + d*d/4096 for its d = bits/3+1 estimated
// digits (intToStringWork: linear and quadratic), on top of the bytes of the string it makes; "%x" and
// "%o" are linear in the words.
func TestIsIntToString(t *testing.T) {
	for _, bits := range []int{3000, 8000, 14000} { // under MaxIntBits
		x := isBig(bits)
		env := StringDict{"x": x}
		d := uint64(bits/3 + 1)
		// the one price of the conversion (see intToStringWork): d/2 + d*d/4096
		quad := d/2 + d*d/4096
		if (d/2)+(uint64(bits/4+1)*uint64(bits/4+1))/4096 == quad {
			t.Fatalf("bits=%d: a number that the estimate of /4 and a free conversion do not tell apart", bits)
		}
		bi := x.BigInt()
		words := uint64(bits+63) / 64
		for _, c := range []struct {
			format string
			base   int
			work   uint64
		}{
			{"%d", 10, quad},
			{"%i", 10, quad},
			{"%x", 16, words},
			{"%X", 16, words},
			{"%o", 8, words},
		} {
			text := bi.Text(c.base)
			got := isRun(t, env, "", fmt.Sprintf("r = '%s' %% x", c.format))
			// (the string is memory without pointers: a unit for each 96 bytes of it)
			want := c.work + uint64(len(text))/96
			if got.work != want || got.alloc != uint64(len(text)) {
				t.Errorf("bits=%d %s: work %d bytes %d, want work %d (%d + %d for the string) bytes %d", bits, c.format, got.work, got.alloc, want, c.work, len(text)/96, len(text))
			}
		}
	}
	// a small integer is free
	if got := isRun(t, StringDict{"x": MakeInt(7)}, "", "r = '%d' % x"); got.work > 1 {
		t.Errorf("'%%d' %% 7: work %d, want at most 1", got.work)
	}
}

// workSearch of the operator in: str and bytes, needles of 1 byte (a 128th of
// the haystack), 2 to 16 (a 16th), 17 to 63 (an 8th), 64 and more (a 4th), and
// an integer in bytes is IndexByte (a 64th).
func TestIsInOnLongOperands(t *testing.T) {
	const n = 10000
	setup := fmt.Sprintf("s = 'x' * %d\nbs = b'x' * %d\n", n, n)
	for _, k := range []int{1, 2, 16, 17, 63, 64, 100} {
		setup += fmt.Sprintf("n%d = 'y' * %d\nb%d = b'y' * %d\n", k, k, k, k)
	}
	setup += "i = 121\n"
	for _, c := range []struct {
		k    int
		want uint64
	}{
		{1, n / 128}, {2, n / 16}, {16, n / 16}, {17, n / 8}, {63, n / 8}, {64, n / 4}, {100, n / 4},
	} {
		if got := isRun(t, nil, setup, fmt.Sprintf("r = n%d in s", c.k)); got.work != c.want {
			t.Errorf("a str of %d in a str of %d: work %d, want %d", c.k, n, got.work, c.want)
		}
		if got := isRun(t, nil, setup, fmt.Sprintf("r = b%d in bs", c.k)); got.work != c.want {
			t.Errorf("bytes of %d in bytes of %d: work %d, want %d", c.k, n, got.work, c.want)
		}
	}
	if got := isRun(t, nil, setup, "r = i in bs"); got.work != n/64 {
		t.Errorf("an int in bytes of %d: work %d, want %d", n, got.work, n/64)
	}
}

// isWindowBound is the bytes that searchCharged has charged when the match of
// a needle of m bytes that starts at pos in a haystack of total bytes is found
// (searching from the end if last): the end of the window that finds it. The
// windows are the ones of searchCharged: the first of max(64, 2*m) bytes, each
// next twice as long up to max(1024 * the divisor of m, 16*m). A window that ends
// at next searches s[done : next+m-1] (from the front) or s[total-next-m+1 :
// total-done] (from the end), so the match at pos is found by the first window
// with pos < next (front), or pos >= total-next-m+1 (end).
func isWindowBound(total, pos, m int, last bool) int {
	div := 128
	switch {
	case m <= 1:
	case m <= 16:
		div = 16
	case m <= 63:
		div = 8
	default:
		div = 4
	}
	chunk := max(1024*div, 16*m)
	step := max(64, 2*m)
	for done := 0; done < total; {
		next := min(total, done+step)
		if (!last && pos < next) || (last && pos >= total-next-m+1) {
			return next
		}
		done = next
		step = min(step*2, chunk)
	}
	return total
}

// partition and rpartition scan up to the separator, found or not.
func TestIsPartitionOnLongOperands(t *testing.T) {
	const n = 10000
	// 'a' * n + 'b' * 64: the separators of b are found at n; 'b' * 64 + 'a' * n: the
	// last of them is found at 64-k
	setup := fmt.Sprintf("p = 'a' * %d + 'b' * 64\nq = 'b' * 64 + 'a' * %d\nz = 'z'\nzz = 'zz'\n", n, n)
	for _, k := range []int{1, 2, 16, 17, 63, 64} {
		setup += fmt.Sprintf("b%d = 'b' * %d\n", k, k)
	}
	rate := func(k int) int { // the divisor of workSearch
		switch {
		case k <= 1:
			return 128
		case k <= 16:
			return 16
		case k <= 63:
			return 8
		}
		return 4
	}
	for _, k := range []int{1, 2, 16, 17, 63, 64} {
		// partition: the scan is the text before the separator and the separator
		// The search charges BEFORE it reads (searchCharged): by windows that
		// grow (64 bytes, doubling, up to a chunk of 1024 units' worth), so a
		// match is charged up to the end of the window that found it, not up to
		// the match.
		if got, want := isRun(t, nil, setup, fmt.Sprintf("r = p.partition(b%d)", k)).work, uint64(isBaseCall+isWindowBound(n+64, n, k, false)/rate(k)); got != want {
			t.Errorf("partition by %d bytes: work %d, want %d", k, got, want)
		}
		// rpartition of q: the last 'b' * k is at 64-k, found by the window that
		// reaches it from the end
		if got, want := isRun(t, nil, setup, fmt.Sprintf("r = q.rpartition(b%d)", k)).work, uint64(isBaseCall+isWindowBound(n+64, 64-k, k, true)/rate(k)); got != want {
			t.Errorf("rpartition by %d bytes: work %d, want %d", k, got, want)
		}
	}
	// not found: all of the text
	for _, c := range []struct{ sep, rate int }{{1, 128}, {2, 16}} {
		sep := []string{"z", "zz"}[c.sep-1]
		if got, want := isRun(t, nil, setup, "r = p.partition('"+sep+"')").work, uint64(isBaseCall+(n+64)/c.rate); got != want {
			t.Errorf("partition by an absent %q: work %d, want %d", sep, got, want)
		}
	}
}

// replace is charged the scan of the count, two units for each match, and the
// bytes of the result (16 to a unit: the result is charged as a whole).
func TestIsReplaceOnLongOperands(t *testing.T) {
	for _, c := range []struct {
		name, old, new string
		matches        int
	}{
		{"ab by c", "ab", "c", 10000},     // shorter
		{"ab by xyz", "ab", "xyz", 10000}, // longer
		{"ab by cd", "ab", "cd", 10000},   // as long
		{"a by c", "a", "c", 10000},       // one byte
		{"ab by c, few", "ab", "c", 3},
	} {
		setup := "s = 'ab,' * 10000\nold = '" + c.old + "'\nnew = '" + c.new + "'\ncount = " + fmt.Sprint(c.matches) + "\n"
		got := isRun(t, nil, setup, "r = s.replace(old, new, count)")
		size := uint64(30000 + c.matches*(len(c.new)-len(c.old)))
		want := uint64(isBaseCall) + workSearchOf(30000, len(c.old)) + 2*uint64(c.matches) + size/16
		if got.alloc != size || got.work != want {
			t.Errorf("%s: work %d bytes %d, want work %d (4 + the scan %d + 2 for each of %d matches + %d for the result) bytes %d",
				c.name, got.work, got.alloc, want, workSearchOf(30000, len(c.old)), c.matches, size/16, size)
		}
	}
}

// workSearchOf is the table of workSearch, written out (not called).
func workSearchOf(n, m int) uint64 {
	switch {
	case m <= 1:
		return uint64(n / 128)
	case m <= 16:
		return uint64(n / 16)
	case m <= 63:
		return uint64(n / 8)
	}
	return uint64(n / 4)
}

// split is charged the scan (twice a search for a separator, a 4th for a
// whitespace), 4 units for each field, and the fields (16 bytes to a unit).
func TestIsSplitOnLongOperands(t *testing.T) {
	for _, c := range []struct {
		name  string
		text  string // the text to split: len and fields below
		call  string
		scan  uint64
		field int
	}{
		{"by a byte", "'ab,' * 10000", "s.split(',')", 2 * workSearchOf(30000, 1), 10001},
		{"by two bytes", "'ab, ' * 10000", "s.split(', ')", 2 * workSearchOf(40000, 2), 10001},
		{"by 20 bytes", "('ab' + 'z' * 20) * 10000", "s.split(z20)", 2 * workSearchOf(220000, 20), 10001},
		{"by white space", "'ab ' * 10000", "s.split()", 30000 / 4, 10000},
		{"rsplit by a byte", "'ab,' * 10000", "s.rsplit(',')", 2 * workSearchOf(30000, 1), 10001},
		{"with a limit", "'ab,' * 10000", "s.split(',', 99)", 2 * workSearchOf(30000, 1), 100},
	} {
		got := isRun(t, nil, "z20 = 'z' * 20\ns = "+c.text+"\n", "r = "+c.call)
		want := uint64(isBaseCall) + c.scan + 4*uint64(c.field) + got.alloc/16
		if got.work != want {
			t.Errorf("%s: work %d, want %d (4 + scan %d + 4 for each of %d fields + %d for the bytes %d)", c.name, got.work, want, c.scan, c.field, got.alloc/16, got.alloc)
		}
	}
}

// splitlines is charged a 64th of the text, 4 units for each line, and the
// lines (16 bytes to a unit).
func TestIsSplitlinesOnLongOperands(t *testing.T) {
	for _, c := range []struct {
		name  string
		text  string
		len   int
		lines int
	}{
		{"short lines", "'ab\\n' * 10000", 30000, 10001},
		{"long lines", "('x' * 1000 + '\\n') * 50", 50050, 51},
		{"a line", "'x' * 6400", 6400, 1},
	} {
		got := isRun(t, nil, "s = "+c.text+"\n", "r = s.splitlines()")
		want := uint64(isBaseCall) + uint64(c.len/64) + 4*uint64(c.lines) + got.alloc/16
		if got.work != want {
			t.Errorf("%s: work %d, want %d (4 + %d for the text + 4 for each of %d lines + %d for the bytes %d)", c.name, got.work, want, c.len/64, c.lines, got.alloc/16, got.alloc)
		}
	}
}

// format with more than 8 keyword arguments makes a map of them, a map insert
// (8 units) each.
func TestIsFormatWithManyKeywords(t *testing.T) {
	fn, err := String("{k0}{k1}").Attr("format")
	if err != nil {
		t.Fatal(err)
	}
	kws := func(n int) []Tuple {
		kw := make([]Tuple, n)
		for i := range kw {
			kw[i] = Tuple{String(fmt.Sprintf("k%d", i)), MakeInt(i)}
		}
		return kw
	}
	// up to 8 there is no map: that is the reference of what a call is charged
	ref := isCall(t, fn, nil, kws(8))
	for _, n := range []int{9, 100, 1000, 5000} {
		got := isCall(t, fn, nil, kws(n))
		// the work of the call beyond the one with 8: the map (8 units for each
		// keyword), and the bytes of the call in the units of 16 bytes (the map)
		wantMap := 8 * uint64(n)
		if got.work-got.alloc/16 != ref.work-ref.alloc/16+wantMap {
			t.Errorf("%d keywords: work %d bytes %d (reference of 8: work %d bytes %d), want %d for the map besides the bytes", n, got.work, got.alloc, ref.work, ref.alloc, wantMap)
		}
	}
}

// trim (strip, lstrip, rstrip) is charged a unit for each character of the
// cutset, 512 units for each 1024 characters that are cut, and a unit for each
// two of the rest, and one.
func TestIsTrimOnLongOperands(t *testing.T) {
	for _, c := range []struct {
		name        string
		text        string
		call        string
		cut, cutset int // characters cut, and of the cutset
	}{
		{"strip, 2000 cut", "' ' * 1000 + 'x' + ' ' * 1000", "s.strip()", 2000, 0},
		{"strip, 2048 cut", "' ' * 1024 + 'x' + ' ' * 1024", "s.strip()", 2048, 0},
		{"strip, 20000 cut", "' ' * 10000 + 'x' + ' ' * 10000", "s.strip()", 20000, 0},
		{"lstrip, 5000 cut", "' ' * 5000 + 'x' + ' ' * 5000", "s.lstrip()", 5000, 0},
		{"rstrip, 3000 cut", "' ' * 5000 + 'x' + ' ' * 3000", "s.rstrip()", 3000, 0},
		{"strip with a cutset of 7", "'_' * 3000 + 'x' + ' ' * 2500", "s.strip(' \\t\\n\\r\\v\\f_')", 5500, 7},
		{"lstrip with a cutset of 7", "'_' * 3000 + 'x' + ' ' * 2500", "s.lstrip(' \\t\\n\\r\\v\\f_')", 3000, 7},
		{"strip, 1023 cut", "' ' * 1000 + 'x' + ' ' * 23", "s.strip()", 1023, 0},
		{"strip, 1024 cut", "' ' * 1000 + 'x' + ' ' * 24", "s.strip()", 1024, 0},
	} {
		got := isRun(t, nil, "s = "+c.text+"\n", "r = "+c.call)
		cut := uint64(c.cut)
		want := uint64(isBaseCall) + uint64(c.cutset) + cut/1024*512 + cut%1024/2 + 1
		if got.work != want {
			t.Errorf("%s: work %d, want %d (4 + %d for the cutset + %d for %d blocks of 1024 + %d for the rest + 1)", c.name, got.work, want, c.cutset, cut/1024*512, cut/1024, cut%1024/2)
		}
	}
}
