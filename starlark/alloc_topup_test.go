package starlark_test

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// A built-in of the host that charges part of what it allocates (ChargeAlloc)
// and returns a value that is larger is charged the difference by Call, not the
// whole value over again: the budget holds what was allocated once.
func TestCall_TopsUpWhatTheBuiltinDidNotCharge(t *testing.T) {
	partial := starlark.NewBuiltin("partial", func(th *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		if err := th.ChargeAlloc(100); err != nil {
			return nil, err
		}
		return starlark.String(strings.Repeat("x", 1000)), nil // 1000 bytes in all
	})
	th := &starlark.Thread{}
	th.SetMaxAllocBytes(1 << 20)
	if _, err := starlark.ExecFileOptions(&syntax.FileOptions{}, th, "t.star", "r = partial()\n", starlark.StringDict{"partial": partial}); err != nil {
		t.Fatal(err)
	}
	// 1000 bytes of the string, of which the built-in charged 100: the call tops
	// up 900 (a few bytes for the statement's own values are within a unit's slack).
	if got := th.AllocatedBytes(); got < 1000 || got > 1000+64 {
		t.Errorf("%d bytes were charged for a string of 1000 bytes (of which the built-in charged 100): want 1000", got)
	}
}
