package main

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Group names. The numbering of the mutants of a group is the order in which
// the generator below finds them in the pristine sources: the order of the
// table (struct), of the file list and of the patterns (calls, prices), and
// then the offset in the file. It depends on the text of the declared files
// and on nothing else; Tree names that text.
const (
	GroupStruct = "struct"
	GroupCalls  = "calls"
	GroupPrices = "prices"
)

// Groups is the declared order of the groups.
var Groups = []string{GroupStruct, GroupCalls, GroupPrices}

// Files of the targets. Every file a mutant may touch is here, and only these
// files can be written by the tool (see Target.write).
const (
	fEval   = "starlark/eval.go"
	fLib    = "starlark/library.go"
	fValue  = "starlark/value.go"
	fAlloc  = "starlark/alloc.go"
	fInterp = "starlark/interp.go"
	fHash   = "starlark/hashtable.go"
	fCmp    = "starlark/workcmp.go"
	fWork   = "starlark/work.go"
	fPrices = "starlark/prices.go"
	fSort   = "starlark/sortstable.go"
	fInt    = "starlark/int.go"
	fForm   = "starlark/form.go"
	fJSON   = "lib/json/json.go"
)

// DeclaredFiles are the files the mutants of any group are made in.
var DeclaredFiles = []string{
	fEval, fLib, fValue, fAlloc, fInterp, fHash, fCmp, fWork, fPrices, fSort, fInt, fForm, fJSON,
}

// Mutant is one change of one span of one file. Start and End are byte
// offsets in the pristine file.
type Mutant struct {
	ID     string // group-NNNN, NNNN is Index
	Group  string
	Index  int // in the group, from 0
	File   string
	Start  int
	End    int
	Before string // the pristine text of [Start, End)
	After  string // what replaces it
	Line   int    // 1-based line of Start
	Note   string
}

// Label is the one-line form of a mutant.
func (m Mutant) Label() string {
	return fmt.Sprintf("%s:%d  %s", m.File, m.Line, oneLine(m.Note, 100))
}

// Apply returns the source with the mutation made.
func (m Mutant) Apply(src string) string { return src[:m.Start] + m.After + src[m.End:] }

func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// Tree is the text of the declared files and its hash.
type Tree struct {
	Files map[string]string // relative path -> content
	Hash  string            // sha256 over sorted (path, length, bytes)
}

// NewTree hashes the files given.
func NewTree(files map[string]string) *Tree {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(files[p]))
		h.Write([]byte(files[p]))
	}
	return &Tree{Files: files, Hash: fmt.Sprintf("%x", h.Sum(nil))}
}

// Set is the mutants of one group and the patterns that the sources no longer
// contain.
type Set struct {
	Group     string
	Mutants   []Mutant
	Missing   []string
	Identical int // mutants not made because they would be the same program
}

type generator struct {
	tree      *Tree
	group     string
	muts      []Mutant
	missing   []string
	identical int
}

// Generate makes the mutants of a group from the pristine tree.
func Generate(tree *Tree, group string) (*Set, error) {
	g := &generator{tree: tree, group: group}
	switch group {
	case GroupStruct:
		g.structGroup()
	case GroupCalls:
		g.callsGroup()
	case GroupPrices:
		g.pricesGroup()
	default:
		return nil, fmt.Errorf("unknown group %q (have %v)", group, Groups)
	}
	for i := range g.muts {
		g.muts[i].Index = i
		g.muts[i].ID = fmt.Sprintf("%s-%04d", group, i)
	}
	return &Set{Group: group, Mutants: g.muts, Missing: g.missing, Identical: g.identical}, nil
}

func (g *generator) src(file string) string {
	s, ok := g.tree.Files[file]
	if !ok {
		panic("file not in the tree: " + file)
	}
	return s
}

func (g *generator) add(file string, a, e int, repl, note string) {
	s := g.src(file)
	g.muts = append(g.muts, Mutant{
		Group: g.group, File: file, Start: a, End: e,
		Before: s[a:e], After: repl,
		Line: 1 + strings.Count(s[:a], "\n"), Note: note,
	})
}

var unescape = strings.NewReplacer(`\n`, "\n", `\t`, "\t")

// literal makes one mutant for each occurrence of old in file. The two-character
// sequences \n and \t of the table stand for a newline and a tab.
func (g *generator) literal(file, old, repl, note string) {
	old, repl = unescape.Replace(old), unescape.Replace(repl)
	s := g.src(file)
	n := strings.Count(s, old)
	if n < 1 {
		g.missing = append(g.missing, fmt.Sprintf("%s: %q", file, oneLine(old, 70)))
		return
	}
	idx := -1
	for k := 0; k < n; k++ {
		i := strings.Index(s[idx+1:], old)
		if i < 0 {
			break
		}
		idx += 1 + i
		g.add(file, idx, idx+len(old), repl, note)
	}
}

// findCallEnd returns the offset after the parenthesis that closes the first
// one found at or after i. Strings and comments are not understood.
func findCallEnd(s string, i int) (int, bool) {
	j := strings.IndexByte(s[i:], '(')
	if j < 0 {
		return 0, false
	}
	d := 0
	for k := i + j; k < len(s); k++ {
		switch s[k] {
		case '(':
			d++
		case ')':
			d--
			if d == 0 {
				return k + 1, true
			}
		}
	}
	return 0, false
}

func isFuncDecl(s string, start int) bool {
	return strings.HasSuffix(s[:start], "func ")
}

// collect replaces each whole call that re matches (at its start) with repl.
func (g *generator) collect(file string, re *regexp.Regexp, repl string) {
	s := g.src(file)
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if isFuncDecl(s, loc[0]) {
			continue
		}
		e, ok := findCallEnd(s, loc[0])
		if !ok {
			g.missing = append(g.missing, fmt.Sprintf("%s:%d: no end of call", file, 1+strings.Count(s[:loc[0]], "\n")))
			continue
		}
		g.add(file, loc[0], e, repl, "DROP "+s[loc[0]:e])
	}
}

// half rewrites the argument x of each call that re matches to (x)/2. For
// chargeUnits the first argument (the thread) is kept.
func (g *generator) half(file string, re *regexp.Regexp) {
	s := g.src(file)
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		if isFuncDecl(s, loc[0]) {
			continue
		}
		e, ok := findCallEnd(s, loc[0])
		if !ok {
			continue
		}
		name := s[loc[2]:loc[3]]
		inner := s[loc[1] : e-1]
		var repl string
		if name == "chargeUnits" {
			if i := strings.Index(inner, ","); i >= 0 {
				inner = inner[i+1:]
			}
			repl = "chargeUnits(thread, (" + strings.TrimSpace(inner) + ")/2)"
		} else {
			repl = name + "((" + inner + ")/2)"
		}
		g.add(file, loc[0], e, repl, "HALF "+oneLine(s[loc[0]:e], 80))
	}
}

var (
	reCallsThread = regexp.MustCompile(`thread\.(chargeWork|chargeIntLinear|chargeIntQuadratic|chargeIntToString|chargeFrame|chargeGrowth|chargeBytes|chargeValues|chargeTuple|chargeEntries|chargeNewEntry|chargeNewSetEntry|chargeItems|chargeNewValues|chargeListOf|chargeOne)\(`)
	reCallsMeter  = regexp.MustCompile(`\b(m|s\.m|slice\.m|w\.m)\.(add)\(`)
	reHalfStar    = regexp.MustCompile(`\b(thread\.chargeWork|m\.add|s\.m\.add|w\.m\.add|slice\.m\.add)\(`)
	reJSONMeter   = regexp.MustCompile(`\b(work|meter)\.(add|flush)\(`)
	reJSONUnits   = regexp.MustCompile(`\bchargeUnits\(`)
	reJSONSpend   = regexp.MustCompile(`\bspend\(`)
	reJSONHalf    = regexp.MustCompile(`\b(spend|work\.add|chargeUnits)\(`)

	rePriceReturn = regexp.MustCompile(`return ([^\n{};]+?)\s*\}\)?,?\n`)
	reFirstReturn = regexp.MustCompile(`return ([^\n]+)\n`)
)

func (g *generator) callsGroup() {
	for _, f := range []string{fEval, fInterp, fLib, fHash, fCmp, fValue, fAlloc, fPrices} {
		g.collect(f, reCallsThread, "error(nil)")
		g.collect(f, reCallsMeter, "error(nil)")
	}
	for _, f := range []string{fEval, fInterp, fLib, fHash, fCmp, fValue, fAlloc} {
		g.half(f, reHalfStar)
	}
	g.collect(fJSON, reJSONMeter, "error(nil)")
	g.collect(fJSON, reJSONUnits, "error(nil)")
	g.collect(fJSON, reJSONSpend, "error(nil)")
	g.half(fJSON, reJSONHalf)
}

func (g *generator) pricesGroup() {
	s := g.src(fPrices)
	for _, loc := range rePriceReturn.FindAllStringSubmatchIndex(s, -1) {
		a, e := loc[2], loc[3]
		g.zeroAndHalf(fPrices, a, e, "return "+oneLine(s[a:e], 70))
	}
	w := g.src(fWork)
	for _, fn := range []string{"workFast", "workSlow", "workSlots", "workSearch", "iterWork", "insertWork", "words64", "chargeIntToString"} {
		head := regexp.MustCompile(`func (\([^)]*\) )?` + fn + `\([^)]*\)[^{]*\{`).FindStringIndex(w)
		if head == nil {
			g.missing = append(g.missing, fmt.Sprintf("%s: func %s", fWork, fn))
			continue
		}
		r := reFirstReturn.FindStringSubmatchIndex(w[head[1]:])
		if r == nil {
			g.missing = append(g.missing, fmt.Sprintf("%s: return in func %s", fWork, fn))
			continue
		}
		a, e := head[1]+r[2], head[1]+r[3]
		g.zeroAndHalf(fWork, a, e, fn)
	}
}

// zeroAndHalf makes the two mutants of a price: its value 0 and its half. A
// price that is 0 already has none (both would be the same program).
func (g *generator) zeroAndHalf(file string, a, e int, note string) {
	s := g.src(file)
	if strings.TrimSpace(s[a:e]) == "0" {
		g.identical++
		return
	}
	g.add(file, a, e, "0", "ZERO "+note)
	g.add(file, a, e, "("+s[a:e]+")/2", "HALF "+note)
}
