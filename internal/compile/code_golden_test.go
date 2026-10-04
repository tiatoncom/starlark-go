package compile

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/resolve"
	"go.starlark.net/syntax"
)

var updateCodeGolden = flag.Bool("update-code-golden", false, "rewrite testdata/code_v020.golden (only ever from the v0.2.0 compiler)")

// codeSamples lists the Starlark sources whose bytecode is pinned.
func codeSamples(t *testing.T) []string {
	files, err := filepath.Glob("../../starlark/testdata/*.star")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "testdata/branches.star")
	sort.Strings(files)
	return files
}

// compileChunks compiles every chunk ("---" separated) of a sample file the
// way starlarktest does; chunks that do not parse or resolve (they test
// errors) are skipped. The result is keyed by "file#chunk".
func compileChunks(t *testing.T, file string) map[string]*Program {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*Program)
	for i, chunk := range strings.Split(string(data), "\n---\n") {
		opts := &syntax.FileOptions{
			Set:               strings.Contains(chunk, "option:set"),
			While:             strings.Contains(chunk, "option:while"),
			TopLevelControl:   strings.Contains(chunk, "option:toplevelcontrol"),
			GlobalReassign:    strings.Contains(chunk, "option:globalreassign"),
			LoadBindsGlobally: strings.Contains(chunk, "option:loadbindsglobally"),
			Recursion:         strings.Contains(chunk, "option:recursion"),
		}
		f, err := opts.Parse(file, chunk, 0)
		if err != nil {
			continue
		}
		always := func(string) bool { return true }
		if err := resolve.File(f, always, func(string) bool { return false }); err != nil {
			continue
		}
		module := f.Module.(*resolve.Module)
		pos := syntax.MakePosition(&file, 1, 1)
		out[fmt.Sprintf("%s#%d", filepath.Base(file), i)] = File(f.Options, f.Stmts, pos, "<toplevel>", module.Locals, module.Globals)
	}
	return out
}

func renderCode(t *testing.T) string {
	var buf bytes.Buffer
	for _, file := range codeSamples(t) {
		progs := compileChunks(t, file)
		keys := make([]string, 0, len(progs))
		for k := range progs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			prog := progs[k]
			fmt.Fprintf(&buf, "%s toplevel %s\n", k, hex.EncodeToString(prog.Toplevel.Code))
			for i, fn := range prog.Functions {
				fmt.Fprintf(&buf, "%s fn%d %s %s\n", k, i, fn.Name, hex.EncodeToString(fn.Code))
			}
		}
	}
	return buf.String()
}

// TestCodeBytesGolden pins the bytes of Funcode.Code of every function of the
// sample programs to what the v0.2.0 compiler produced: the trace tables
// are side data and must never add, drop or reorder an instruction.
func TestCodeBytesGolden(t *testing.T) {
	got := renderCode(t)
	const golden = "testdata/code_v020.golden"
	if *updateCodeGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("Funcode.Code differs from the v0.2.0 golden at line %d:\n got %.200s\nwant %.200s", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("Funcode.Code golden has %d lines, the compiler produced %d", len(wl), len(gl))
	}
}

// TestCodeBytesGoldenNot pins the bytes of the functions of
// testdata/branches_not.star (a "not" or a chain that is evaluated as a value,
// comparisons at every place of a chain) to what the v0.2.0 compiler produced.
// The golden was made by running the same sample through a checkout of v0.2.0;
// it is never rewritten by this tree.
func TestCodeBytesGoldenNot(t *testing.T) {
	var buf bytes.Buffer
	progs := compileChunks(t, "testdata/branches_not.star")
	keys := make([]string, 0, len(progs))
	for k := range progs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prog := progs[k]
		fmt.Fprintf(&buf, "%s toplevel %s\n", k, hex.EncodeToString(prog.Toplevel.Code))
		for i, fn := range prog.Functions {
			fmt.Fprintf(&buf, "%s fn%d %s %s\n", k, i, fn.Name, hex.EncodeToString(fn.Code))
		}
	}
	want, err := os.ReadFile("testdata/code_v020_not.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != string(want) {
		t.Errorf("Funcode.Code of branches_not.star differs from the v0.2.0 golden:\n got %.300s\nwant %.300s", got, want)
	}
	if len(progs) == 0 {
		t.Error("no sample compiled")
	}
}
