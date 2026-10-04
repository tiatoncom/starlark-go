package starlark_test

// An independent oracle of the trace of conditions. A random condition is
// built as a tree (depth up to 5: and/or chains, not, redundant parentheses,
// leaves a, a == 1, a < 1, a != 0, a in xs, a not in xs), rendered into four
// constructs (if, a conditional expression, a comprehension if, while) and run
// on 16 inputs. The oracle evaluates the tree by the textbook rules of
// short-circuiting and writes down, from the tree alone, the events that must
// come: which leaves are tested, in which order, with which Negated, taken,
// operand, positions, chain ids and which comparison precedes them. The test
// compares the whole list of events (their contents and their order) and the
// result of the function, not just the outcome.
//
// The trees are generated from a fixed seed, so a run is repeatable. The
// default count is small enough to run in a few seconds; STARLARK_TRACE_ORACLE_N
// raises it (60000 is a long run).

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type oracleNode struct {
	t    string // leaf, not, paren, and, or
	lk   int    // leaf kind
	v    int    // var
	kids []*oracleNode
}

var oracleLeafKinds = []string{"%s", "%s == 1", "%s not in xs", "%s < 1", "%s in xs", "%s != 0"}

func oracleGen(r *rand.Rand, depth int) *oracleNode {
	if depth <= 0 || r.Intn(4) == 0 {
		return &oracleNode{t: "leaf", lk: r.Intn(len(oracleLeafKinds)), v: r.Intn(4)}
	}
	switch r.Intn(6) {
	case 0:
		k := oracleGen(r, depth-1)
		return oracleWrap("not", oracleParenChain(k))
	case 1:
		return oracleWrap("paren", oracleGen(r, depth-1))
	default:
		op := "and"
		if r.Intn(2) == 0 {
			op = "or"
		}
		n := 2 + r.Intn(2)
		c := &oracleNode{t: op}
		for i := 0; i < n; i++ {
			k := oracleGen(r, depth-1)
			c.kids = append(c.kids, oracleFixKid(r, op, k))
		}
		return c
	}
}

func oracleWrap(t string, k *oracleNode) *oracleNode {
	return &oracleNode{t: t, kids: []*oracleNode{k}}
}

func oracleParenChain(k *oracleNode) *oracleNode {
	if k.t == "and" || k.t == "or" {
		return oracleWrap("paren", k)
	}
	return k
}

// kid of a chain op: a chain of the same op, or `or` under `and`, gets parens
func oracleFixKid(r *rand.Rand, op string, k *oracleNode) *oracleNode {
	if k.t == op || (op == "and" && k.t == "or") {
		return oracleWrap("paren", k)
	}
	if k.t == "and" && r.Intn(2) == 0 {
		return oracleWrap("paren", k)
	}
	return k
}

type oracleEvent struct {
	cmp      bool
	line     int
	col      int
	kind     string
	operand  int
	chainCol int
	kwCol    int
	negated  bool
	taken    bool
	chainID  int
	parent   int
	op       syntax.Token
	res      bool
}

func (e oracleEvent) String() string {
	if e.cmp {
		return fmt.Sprintf("cmp@%d:%d %s#%d %v res=%v", e.line, e.col, e.kind, e.operand, e.op, e.res)
	}
	return fmt.Sprintf("br@%d:%d %s#%d chainCol=%d kw=%d neg=%v taken=%v chain=%d parent=%d", e.line, e.col, e.kind, e.operand, e.chainCol, e.kwCol, e.negated, e.taken, e.chainID, e.parent)
}

type oracleChain struct {
	stands int
	depth  int
	parent *oracleChain
	rank   int
}

type oracleLeaf struct {
	n       *oracleNode
	opStart int
	chain   *oracleChain
	operand int
	kw      int
	neg     bool
	kind    string
}

type oracleCtx struct {
	opStart, stands int
	chain           *oracleChain
	depth           int
	neg             bool
	operand         int
	kw              int
	kind            string
}

type oracleRender struct {
	sb     strings.Builder
	base   int
	chains []*oracleChain
	leaves map[*oracleNode]*oracleLeaf
}

func (r *oracleRender) pos() int { return r.base + r.sb.Len() + 1 }

func (r *oracleRender) node(n *oracleNode, c oracleCtx, rootKind string) {
	p := r.pos()
	if c.opStart < 0 {
		c.opStart = p
	}
	if c.stands < 0 {
		c.stands = p
	}
	switch n.t {
	case "not":
		r.sb.WriteString("not ")
		c.stands = r.pos()
		c.neg = !c.neg
		r.node(n.kids[0], c, rootKind)
	case "paren":
		r.sb.WriteString("(")
		r.node(n.kids[0], c, rootKind)
		r.sb.WriteString(")")
	case "leaf":
		l := &oracleLeaf{n: n, opStart: c.opStart, chain: c.chain, operand: c.operand, kw: c.kw, neg: c.neg, kind: c.kind}
		r.leaves[n] = l
		fmt.Fprintf(&r.sb, oracleLeafKinds[n.lk], fmt.Sprintf("v%d", n.v))
	default:
		ch := &oracleChain{stands: c.stands, depth: c.depth, parent: c.chain}
		r.chains = append(r.chains, ch)
		for i, k := range n.kids {
			kw := 0
			if i > 0 {
				r.sb.WriteString(" ")
				kw = r.pos()
				r.sb.WriteString(n.t + " ")
			}
			r.node(k, oracleCtx{opStart: -1, stands: -1, chain: ch, depth: c.depth + 1, neg: c.neg, operand: i, kw: kw, kind: n.t}, rootKind)
		}
	}
}

type oracleState struct {
	v  [4]int
	ev []oracleEvent
	cl int // cond line
	k  string
	kw int
}

func (s *oracleState) eval(n *oracleNode, r *oracleRender) bool {
	switch n.t {
	case "not":
		return !s.eval(n.kids[0], r)
	case "paren":
		return s.eval(n.kids[0], r)
	case "leaf":
		l := r.leaves[n]
		x := s.v[n.v]
		var truth, taken bool
		var op syntax.Token
		var res bool
		cmp := true
		switch n.lk {
		case 0:
			truth = x != 0
			cmp = false
		case 1:
			truth = x == 1
			op, res = syntax.EQL, truth
		case 2:
			in := x == 1 || x == 2
			truth = !in
			op, res = syntax.IN, in
		case 3:
			truth = x < 1
			op, res = syntax.LT, truth
		case 4:
			truth = x == 1 || x == 2
			op, res = syntax.IN, truth
		case 5:
			truth = x != 0
			op, res = syntax.NEQ, truth
		}
		taken = truth
		if n.lk == 2 {
			taken = res
		}
		neg := l.neg != (n.lk == 2)
		e := oracleEvent{line: s.cl, col: l.opStart, kind: l.kind, operand: l.operand, negated: neg, taken: taken, chainID: -1, parent: -1}
		if l.chain == nil {
			e.kind = s.k
			e.operand = -1
			e.chainCol = l.opStart
			e.kwCol = s.kw
		} else {
			e.chainCol = l.chain.stands
			e.kwCol = l.kw
			e.chainID = l.chain.rank
			if l.chain.parent != nil {
				e.parent = l.chain.parent.rank
			}
		}
		if cmp {
			ce := e
			ce.cmp, ce.op, ce.res = true, op, res
			s.ev = append(s.ev, ce)
		}
		s.ev = append(s.ev, e)
		return truth
	case "and":
		for _, k := range n.kids {
			if !s.eval(k, r) {
				return false
			}
		}
		return true
	default:
		for _, k := range n.kids {
			if s.eval(k, r) {
				return true
			}
		}
		return false
	}
}

type oracleTemplate struct {
	name, src, prefix, kw, kind string
	line                        int
}

var oracleTemplates = []oracleTemplate{
	{"if", "def f(v0, v1, v2, v3, xs):\n    if %s:\n        return 1\n    return 0\n", "    if ", "if", "if", 2},
	{"cond", "def f(v0, v1, v2, v3, xs):\n    return 1 if %s else 0\n", "    return 1 if ", "if", "cond_expr", 2},
	{"comp", "def f(v0, v1, v2, v3, xs):\n    return len([1 for _ in [0, 1, 2] if %s])\n", "    return len([1 for _ in [0, 1, 2] if ", "if", "comp_if", 2},
	{"while", "def f(v0, v1, v2, v3, xs):\n    n = 0\n    while %s:\n        v0, v1, v2, v3 = v1, v2, v3, v0\n        n += 1\n        if n >= 5:\n            break\n    return n\n", "    while ", "while", "while", 3},
}

// oracleTrees is the number of trees of a default run.
const oracleTrees = 4000

func TestTraceOracle(t *testing.T) {
	seeds := oracleTrees
	if v := os.Getenv("STARLARK_TRACE_ORACLE_N"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("STARLARK_TRACE_ORACLE_N=%q: want a positive number", v)
		}
		seeds = n
	}
	r := rand.New(rand.NewSource(7))
	bad := 0
	for it := 0; it < seeds && bad < 8; it++ {
		tree := oracleGen(r, 1+it%5)
		for _, tm := range oracleTemplates {
			var rd oracleRender
			rd.leaves = map[*oracleNode]*oracleLeaf{}
			rd.base = len(tm.prefix)
			rd.node(tree, oracleCtx{opStart: -1, stands: -1}, tm.kind)
			cond := rd.sb.String()
			// chain ranks: by (stands, depth)
			ord := append([]*oracleChain(nil), rd.chains...)
			sort.SliceStable(ord, func(i, j int) bool {
				if ord[i].stands != ord[j].stands {
					return ord[i].stands < ord[j].stands
				}
				return ord[i].depth < ord[j].depth
			})
			for i, c := range ord {
				c.rank = i
			}
			src := fmt.Sprintf(tm.src, cond)
			kwcol := strings.Index(tm.prefix, tm.kw) + 1
			var got []oracleEvent
			tr := &starlark.Tracer{
				OnBranch: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, tested starlark.Value, taken bool) {
					if b.Cond.Line != int32(tm.line) {
						return
					}
					e := oracleEvent{line: int(b.Cond.Line), col: int(b.Cond.Col), kind: b.Kind.String(), operand: b.Operand, chainCol: int(b.Chain.Col), kwCol: int(b.Keyword.Col), negated: b.Negated, taken: taken, chainID: b.ChainID, parent: b.ParentChain}
					if b.Keyword.Line == 0 {
						e.kwCol = 0
					}
					if b.Operand < 0 && b.Keyword.Col != 0 && int(b.Keyword.Col) != kwcol {
						e.kwCol = -int(b.Keyword.Col)
					}
					if tested.Truth() != starlark.Bool(taken) {
						e.kwCol = -999
					}
					got = append(got, e)
				},
				OnCompare: func(_ *starlark.Thread, _ *starlark.Function, b starlark.Branch, op syntax.Token, x, y starlark.Value, result bool) {
					if b.Cond.Line != int32(tm.line) {
						return
					}
					got = append(got, oracleEvent{cmp: true, line: int(b.Cond.Line), col: int(b.Cond.Col), kind: b.Kind.String(), operand: b.Operand, op: op, res: result,
						chainCol: 0, kwCol: 0, negated: false, taken: false, chainID: 0, parent: 0})
				},
			}
			thread, globals, err := execTraced(t, src, nil, nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, src)
			}
			thread.Trace = tr
			for m := 0; m < 16; m++ {
				got = got[:0]
				st := &oracleState{cl: tm.line, k: tm.kind, kw: kwcol}
				for i := 0; i < 4; i++ {
					st.v[i] = (m >> i) & 1
				}
				args := starlark.Tuple{}
				for i := 0; i < 4; i++ {
					args = append(args, starlark.MakeInt(st.v[i]))
				}
				args = append(args, starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.MakeInt(2)}))
				var wantRes int
				switch tm.name {
				case "if", "cond":
					if st.eval(tree, &rd) {
						wantRes = 1
					}
				case "comp":
					for i := 0; i < 3; i++ {
						if st.eval(tree, &rd) {
							wantRes++
						}
					}
				case "while":
					n := 0
					for st.eval(tree, &rd) {
						st.v = [4]int{st.v[1], st.v[2], st.v[3], st.v[0]}
						n++
						if n >= 5 {
							break
						}
					}
					wantRes = n
				}
				res, err := starlark.Call(thread, globals["f"], args, nil)
				if err != nil {
					t.Fatalf("%v\n%s", err, src)
				}
				// normalise expected: kwCol check for compare events zeroed; for single leaf kw checked
				var want []oracleEvent
				for _, e := range st.ev {
					if e.cmp {
						e.chainCol, e.kwCol, e.negated, e.taken, e.chainID, e.parent = 0, 0, false, false, 0, 0
					}
					want = append(want, e)
				}
				gs, ws := fmt.Sprint(got), fmt.Sprint(want)
				if gs != ws || int(res.(starlark.Int).BigInt().Int64()) != wantRes {
					bad++
					t.Errorf("MISMATCH tmpl=%s inputs=%d cond=%q\n got  %v\n want %v\n res=%v want %d", tm.name, m, cond, strings.ReplaceAll(gs, "] [", "\n       "), strings.ReplaceAll(ws, "] [", "\n       "), res, wantRes)
					break
				}
			}
		}
	}
	t.Logf("%d trees x 4 constructs x 16 inputs, bad=%d", seeds, bad)
}
