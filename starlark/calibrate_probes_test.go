package starlark_test

// The probes of TestCalibrate: the worst form of each operation that the
// reviews found, and of the rest of the table of prices.

var calibrationProbes = []probe{
	// ---- the interpreter: a unit is about a step
	{"loop: for i in range(N): pass", "", "for i in range(2000000):\n    pass"},
	{"loop: x = len(l)", "l = [1]", "for i in range(1000000):\n    x = len(l)"},
	{"loop: def call", "def f(a): return a\n", "for i in range(1000000):\n    f(i)"},
	{"loop: l.append(i)", "l = []", "for i in range(1000000):\n    l.append(i)"},
	{"loop: d[i] = i", "d = {}", "for i in range(500000):\n    d[i] = i"},

	// ---- strings: search, in its worst form
	{"find: 100-byte needle, all a", "a = 'a' * 1000000", "r = a.find('a' * 100 + 'b')"},
	{"find: 10000-byte needle, all a", "a = 'a' * 1000000", "r = a.find('a' * 10000 + 'b')"},
	{"find: 1-byte needle, miss", "a = 'a' * 1000000", "r = a.find('b')"},
	{"find: 2-byte needle, ab...", "a = 'ab' * 500000", "r = a.find('bb')"},
	{"rfind: 100-byte needle", "a = 'a' * 1000000", "r = a.rfind('a' * 100 + 'b')"},
	{"count: 100-byte needle", "a = 'a' * 1000000", "r = a.count('a' * 100 + 'b')"},
	{"count: 1-byte needle", "a = 'a' * 1000000", "r = a.count('a')"},
	{"count: aa", "a = 'a' * 1000000", "r = a.count('aa')"},
	{"in: 100-byte needle", "a = 'a' * 1000000", "r = ('a' * 100 + 'b') in a"},
	{"replace: 100-byte miss", "a = 'a' * 1000000", "r = a.replace('a' * 100 + 'b', 'x')"},
	{"replace: a -> bb (n matches)", "a = 'a' * 1000000", "r = a.replace('a', 'bb')"},
	{"replace: a -> b", "a = 'a' * 1000000", "r = a.replace('a', 'b')"},
	{"partition: 100-byte miss", "a = 'a' * 1000000", "r = a.partition('a' * 100 + 'b')"},
	{"rpartition: 100-byte miss", "a = 'a' * 1000000", "r = a.rpartition('a' * 100 + 'b')"},
	{"startswith/endswith", "a = 'a' * 1000000", "for i in range(1000):\n    r = a.startswith('a' * 1000)\n    r = a.endswith('a' * 1000)"},
	{"split: , (500000 fields)", "s = 'a,' * 500000", "r = s.split(',')"},
	{"split: whitespace", "s = 'a ' * 500000", "r = s.split()"},
	{"split: , 100-byte sep miss", "a = 'a' * 1000000", "r = a.split('a' * 100 + 'b')"},
	{"rsplit: ,", "s = 'a,' * 500000", "r = s.rsplit(',')"},
	{"splitlines", "s = 'a\\n' * 500000", "r = s.splitlines()"},
	{"join: 500000 strings", "l = ['ab'] * 500000", "r = ','.join(l)"},
	{"strip: 5000 non-ascii cutset", "a = 'a' * 200000\nc = 'é' * 5000", "for i in range(10):\n    r = a.lstrip(c)"},
	{"lstrip: A4 form", "a = 'a' * 300000\nc = 'é' * 29999 + 'a'", "r = a.lstrip(c)"},
	{"strip: ascii cutset", "a = ' ' * 1000000 + 'x'", "r = a.strip()"},
	{"upper (ascii)", "a = 'a' * 1000000", "r = a.upper()"},
	{"upper (non-ascii)", "a = 'é' * 500000", "r = a.upper()"},
	{"lower (non-ascii)", "a = 'É' * 500000", "r = a.lower()"},
	{"title", "a = 'ab ' * 300000", "r = a.title()"},
	{"capitalize", "a = 'ab ' * 300000", "r = a.capitalize()"},
	{"isalpha/isdigit/isspace", "a = 'a' * 1000000", "r = a.isalpha()\nr = a.isalnum()\nr = a.isdigit()\nr = a.isupper()"},
	{"s + s", "a = 'a' * 1000000", "r = a + a"},
	{"s * 8", "a = 'a' * 100000", "r = a * 8"},
	{"s[::-1]", "a = 'a' * 1000000", "r = a[::-1]"},
	{"s[::2]", "a = 'a' * 1000000", "r = a[::2]"},
	{"s == s", "a = 'a' * 1000000\nb = 'a' * 1000000", "for i in range(100):\n    r = a == b"},
	{"s < s", "a = 'a' * 1000000\nb = 'a' * 1000000", "for i in range(100):\n    r = a < b"},
	{"hash(s)", "a = 'a' * 1000000", "r = hash(a)"},
	{"dict key: 1 MB string", "a = 'a' * 1000000", "d = {}\nfor i in range(20):\n    d[a] = i"},
	{"str(s)", "a = 'a' * 1000000", "r = str(a)"},
	{"repr(s): plain", "a = 'a' * 1000000", "r = repr(a)"},
	{"repr(s): escapes", "a = 'a\\n' * 500000", "r = repr(a)"},
	{"repr(list of str)", "l = ['abcdefghij'] * 100000", "r = repr(l)"},
	{"%s % s", "a = 'a' * 1000000", "r = '%s!' % a"},
	{"% d", "", "for i in range(100000):\n    r = '%d' % i"},
	{"'{}'.format(s)", "a = 'a' * 1000000", "r = '{}!'.format(a)"},
	{"format of 100000 fields", "t = ['abcdefghij'] * 100000", "r = ('{},' * 100000).format(*t)"},
	{"list(s.elems())", "a = 'a' * 300000", "r = list(a.elems())"},
	{"list(s.codepoints())", "a = 'é' * 300000", "r = list(a.codepoints())"},
	{"bytes(s)", "a = 'a' * 1000000", "r = bytes(a)"},
	{"list(b.elems())", "b = bytes('a' * 300000)", "r = list(b.elems())"},
	{"str(b)", "b = bytes('a' * 1000000)", "r = str(b)"},

	// ---- lists and tuples
	{"x in l (ints, miss)", "l = list(range(500000))", "r = -1 in l"},
	{"x in l (strings, miss)", "l = ['abcdefghij' + str(i) for i in range(200000)]", "r = 'zzz' in l"},
	{"x in l (mixed int/float)", "l = list(range(300000))", "r = -1.5 in l"},
	{"x in l (big ints)", "x = 1 << 500\nl = [x + i for i in range(100000)]", "r = (x - 1) in l"},
	{"l.index(miss) / remove", "l = list(range(300000))", "r = l.index(299999)"},
	{"max(l)", "l = list(range(500000))", "r = max(l)"},
	{"min(l) mixed", "l = list(range(250000)) + [0.5] * 250000", "r = min(l)"},
	{"sorted: random ints", "l = [(i * 7919) % 1000003 for i in range(200000)]", "r = sorted(l)"},
	{"sorted: ascending", "l = list(range(300000))", "r = sorted(l)"},
	{"sorted: descending", "l = list(range(300000, 0, -1))", "r = sorted(l)"},
	{"sorted: equal", "l = [1] * 300000", "r = sorted(l)"},
	{"sorted: strings", "l = [str((i * 7919) % 1000003) for i in range(100000)]", "r = sorted(l)"},
	{"sorted: mixed int/float", "l = [(i * 7919) % 1000003 * (1 + (i % 2) * 0.5) for i in range(100000)]", "r = sorted(l)"},
	{"sorted: tuples", "l = [((i * 7919) % 1000, str(i)) for i in range(100000)]", "r = sorted(l)"},
	{"sorted: key=lambda", "l = [(i * 7919) % 1000003 for i in range(100000)]", "r = sorted(l, key=lambda x: -x)"},
	{"list(l)", "l = list(range(1000000))", "r = list(l)"},
	{"tuple(l)", "l = list(range(1000000))", "r = tuple(l)"},
	{"reversed(l)", "l = list(range(1000000))", "r = reversed(l)"},
	{"enumerate(l)", "l = list(range(500000))", "r = enumerate(l)"},
	{"zip(l, l)", "l = list(range(500000))", "r = zip(l, l)"},
	{"l * 8", "l = list(range(100000))", "r = l * 8"},
	{"l + l", "l = list(range(500000))", "r = l + l"},
	{"l[::2]", "l = list(range(1000000))", "r = l[::2]"},
	{"l[::-1]", "l = list(range(1000000))", "r = l[::-1]"},
	{"l.extend(l)", "l = list(range(500000))", "l.extend(l)"},
	{"l += l", "l = list(range(500000))", "l += l"},
	{"l.insert(0) x 1000, 1M slots", "l = list(range(1000000))", "for i in range(1000):\n    l.insert(0, i)"},
	{"l.pop(0) x 1000, 1M slots", "l = list(range(1000000))", "for i in range(1000):\n    l.pop(0)"},
	{"l.clear()", "l = list(range(1000000))", "l.clear()"},
	{"l == m (ints)", "l = list(range(1000000))\nm = list(range(1000000))", "r = l == m"},
	{"l < m (ints)", "l = list(range(1000000))\nm = list(range(1000000))", "r = l < m"},
	{"l == m (mixed int/float)", "l = list(range(300000))\nm = [float(i) for i in range(300000)]", "r = l == m"},
	{"nested == (wide, 5 deep)", "def mk(d):\n    if d == 0:\n        return 1\n    return [mk(d - 1) for i in range(12)]\nx = mk(5)\ny = mk(5)", "r = x == y"},
	{"str(l) ints", "l = list(range(300000))", "r = str(l)"},
	{"str(l) empty lists depth 40", "x = [[]] * 500000\nfor i in range(40):\n    x = [x]", "r = str(x)"},
	{"x < 1.5, big int 32 KB", bigSetup, "for i in range(1000):\n    r = x < 1.5"},

	// ---- dicts and sets
	{"d[k] = v, 500000", "", "d = {}\nfor i in range(500000):\n    d[i] = i"},
	{"set(l)", "l = list(range(500000))", "r = set(l)"},
	{"dict(d)", "d = {i: i for i in range(300000)}", "r = dict(d)"},
	{"d == e", "d = {i: i for i in range(300000)}\ne = {i: i for i in range(300000)}", "r = d == e"},
	{"d | e", "d = {i: i for i in range(300000)}\ne = {i + 300000: i for i in range(300000)}", "r = d | e"},
	{"d.update(e) same keys", "d = {i: i for i in range(300000)}\ne = dict(d)", "d.update(e)"},
	{"d.keys()/items()/values()", "d = {i: i for i in range(300000)}", "r = d.keys()\nr = d.items()\nr = d.values()"},
	{"d.clear() after growth, x1000000", "d = {i: i for i in range(300000)}\nd.clear()", "for k in range(300000):\n    d['a'] = 1\n    d.clear()"},
	{"s | t", "s = set(range(300000))\nt = set(range(300000, 600000))", "r = s | t"},
	{"s & t", "s = set(range(300000))\nt = set(range(150000, 450000))", "r = s & t"},
	{"s - t", "s = set(range(300000))\nt = set(range(150000, 450000))", "r = s - t"},
	{"s ^ t", "s = set(range(300000))\nt = set(range(150000, 450000))", "r = s ^ t"},
	{"s == t", "s = set(range(300000))\nt = set(range(300000))", "r = s == t"},
	{"s <= t", "s = set(range(300000))\nt = set(range(300000))", "r = s <= t"},
	{"collisions: i << 16", "", "d = {}\nfor i in range(20000):\n    d[i << 16] = i"},
	{"collisions: i << 40", "", "d = {}\nfor i in range(20000):\n    d[i << 40] = i"},
	{"d[k] with 30 char string keys", "ks = ['key-long-prefix-%d-abcdefghij' % i for i in range(200000)]", "d = {}\nfor k in ks:\n    d[k] = 1"},
	{"x in d, strings", "d = {'key-long-prefix-%d-abcdefghij' % i: i for i in range(100000)}\nks = ['key-long-prefix-%d-abcdefghij' % i for i in range(100000)]", "n = 0\nfor k in ks:\n    if k in d:\n        n += 1"},

	// ---- calls
	{"f() with 2000 params x 2000", "def f(" + params(2000) + "): pass\n", "for i in range(2000):\n    f()"},
	{"f(**d), 60000 keys", "def f(**kw): pass\nd = {'k%d' % i: i for i in range(60000)}", "f(**d)"},
	{"f(**d) onto 2000 params", "def f(" + params(2000) + "): pass\nd = {'p%d' % i: i for i in range(2000)}", "for i in range(5):\n    f(**d)"},
	{"'{k}'.format(**d)", "d = {'k%d' % i: 'v' for i in range(100000)}", "r = ('{k99999}' * 10000).format(**d)"},
	{"f(*l), 500000", "def f(*a): return 1\nl = list(range(500000))", "f(*l)"},
	{"chain of 20000 functions", chain(20000), "r = f0()"},

	// ---- integers
	{"big + 1 (6000 words)", bigSetup, "for i in range(5000):\n    r = x + 1"},
	{"big * big (1500 words)", bigSetup2, "r = y * y"},
	{"big // y (6000 / 1500 words)", bigSetup2 + "x = y * y * y * y", "r = x // y"},
	{"big << 511", bigSetup, "for i in range(5000):\n    r = x << 511"},
	{"-big", bigSetup, "for i in range(5000):\n    r = -x"},
	{"str(int 4300 digits) x20", "x = int('7' * 4299)", "for i in range(20):\n    r = str(x)"},
	{"int(str 4300 digits) x20", "s = '7' * 4299", "for i in range(20):\n    r = int(s)"},
	{"hash of 6000-word int", bigSetup, "for i in range(500):\n    r = {x: 1}"},

	// ---- json
	{"json.encode(list of int)", "l = list(range(300000))", "r = json.encode(l)"},
	{"json.encode(list of str)", "l = ['abcdefghij'] * 200000", "r = json.encode(l)"},
	{"json.encode(dict)", "d = {'key%d' % i: i for i in range(100000)}", "r = json.encode(d)"},
	{"json.encode(nested lists)", "l = [[i, [i]] for i in range(100000)]", "r = json.encode(l)"},
	{"json.encode(non-ascii str)", "l = ['é' * 100] * 5000", "r = json.encode(l)"},
	{"json.decode(list of int)", "s = json.encode(list(range(300000)))", "r = json.decode(s)"},
	{"json.decode(list of str)", "s = json.encode(['abcdefghij'] * 200000)", "r = json.decode(s)"},
	{"json.decode(empty lists)", "s = '[' + '[],' * 300000 + '[]]'", "r = json.decode(s)"},
	{"json.decode(empty objects)", "s = '[' + '{},' * 300000 + '{}]'", "r = json.decode(s)"},
	{"json.decode(dicts)", "s = json.encode([{'a': i} for i in range(100000)])", "r = json.decode(s)"},
	{"json.indent(flat)", "s = json.encode(list(range(300000)))", "r = json.indent(s)"},
	{"json.indent(nested)", "s = '[' * 1000 + '1,' * 100000 + '1' + ']' * 1000", "r = json.indent(s)"},

	// ---- deep and shared structure
	{"hash of a DAG tuple (2^22 paths)", "t = (1,)\nfor i in range(22):\n    t = (t, t)", "r = {t: 1}"},
}

func params(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ", "
		}
		s += "p" + itoa(i) + "=0"
	}
	return s
}

func chain(n int) string {
	s := ""
	for i := 0; i < n-1; i++ {
		s += "def f" + itoa(i) + "(): return f" + itoa(i+1) + "()\n"
	}
	s += "def f" + itoa(n-1) + "(): return 1\n"
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

const bigSetup = "x = 1 << 500\nfor i in range(9):\n    x = x * x\n"
const bigSetup2 = "y = 1 << 500\nfor i in range(7):\n    y = y * y\n"
