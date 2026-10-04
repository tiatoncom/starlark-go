package starlark

import (
	"go.starlark.net/internal/compile"
	"go.starlark.net/syntax"
)

// A FuncodeInfo is the static description of one function of a compiled
// Program: what a host can learn about its conditions without running it.
// It carries the same Branch values that OnBranch and OnCompare receive at run
// time, so a markup built from it agrees with a trace by construction.
type FuncodeInfo struct {
	Name       string          // name of the function; "lambda" for a lambda, "<toplevel>" for the toplevel
	Pos        syntax.Position // position of the def or lambda token, as Function.Position reports it
	IsToplevel bool            // the toplevel function of the file, as Function.IsToplevel reports it
	Branches   []Branch        // the branch table of the function, sorted by PC; nil if it has no conditions
}

// Funcodes returns the static description of every function of the program:
// first the toplevel function, then the defs and lambdas in the order in which
// the compiler numbers them (the order of the program's function pool, which
// is saved with the program), nested ones included. The order depends on the
// source alone, so it is the same for a program compiled from source and for
// the same program read back by CompiledProgram.
//
// A function that the Funcodes lists has at run time a *Function with the same
// Pos (and the same Branches in the events). Several closures of one def
// share one entry. The returned slices are copies: the caller may change them.
//
// The interpreter's own flags of the table (what makes an echo invisible, what
// a computed "not" flips) are not part of the description; see doc/trace.md.
func (prog *Program) Funcodes() []FuncodeInfo {
	c := prog.compiled
	infos := make([]FuncodeInfo, 0, 1+len(c.Functions))
	add := func(f *compile.Funcode) {
		info := FuncodeInfo{Name: f.Name, Pos: f.Pos, IsToplevel: f == c.Toplevel}
		if len(f.Branches) > 0 {
			info.Branches = append([]Branch(nil), f.Branches...)
		}
		infos = append(infos, info)
	}
	add(c.Toplevel)
	for _, f := range c.Functions {
		add(f)
	}
	return infos
}
