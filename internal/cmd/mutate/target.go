package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Target is the tree the mutants are made in. It is the ONLY place that writes
// a file of the tree: a path must be one of DeclaredFiles, relative and clean,
// and the file it names must lie inside the root after the links are followed.
type Target struct {
	Root     string // absolute, links resolved
	declared map[string]bool
	mu       sync.Mutex
	frozen   bool
}

// NewTarget checks the root and makes a Target.
func NewTarget(root string) (*Target, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", real)
	}
	t := &Target{Root: real, declared: map[string]bool{}}
	for _, f := range DeclaredFiles {
		t.declared[f] = true
	}
	return t, nil
}

// path resolves a declared relative path to a file inside the root.
func (t *Target) path(rel string) (string, error) {
	if !t.declared[rel] {
		return "", fmt.Errorf("%q is not a declared file", rel)
	}
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%q is not a clean relative path", rel)
	}
	p := filepath.Join(t.Root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if real != p {
		// A link on the way: allowed only if it stays inside the root.
		if r, err := filepath.Rel(t.Root, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%q leaves the root through a link (%s)", rel, real)
		}
	}
	return real, nil
}

// Read returns the content of a declared file.
func (t *Target) Read(rel string) (string, error) {
	p, err := t.path(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// Freeze puts every file back to the given pristine text and refuses every
// later Write. It is what a signal handler calls.
func (t *Target) Freeze(pristine map[string]string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.frozen = true
	var first error
	for rel, s := range pristine {
		if err := t.write(rel, s); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Write replaces the content of an existing declared file.
func (t *Target) Write(rel, content string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.frozen {
		return fmt.Errorf("the target is frozen")
	}
	return t.write(rel, content)
}

func (t *Target) write(rel, content string) error {
	p, err := t.path(rel)
	if err != nil {
		return err
	}
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), st.Mode().Perm())
}

// ReadTree reads every declared file.
func (t *Target) ReadTree() (*Tree, error) {
	files := map[string]string{}
	for _, f := range DeclaredFiles {
		s, err := t.Read(f)
		if err != nil {
			return nil, err
		}
		files[f] = s
	}
	return NewTree(files), nil
}
