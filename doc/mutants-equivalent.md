# Mutants that no test can kill

The mutation run (`internal/cmd/mutate`) changes one place of the resource
accounting at a time and runs the tests of `./starlark` and `./lib/json`. A
mutant that survives is either a missing test or a change that no program can
see. This document is the second kind, with the proof for each: what the
mutation is, and for each observable of a run (the result of the program, the
error, `Thread.Work()`, `Thread.AllocatedBytes()`, `Thread.Steps`) whether it
changes and why. What changes is time or Go memory that is not charged.

Ids are those of the numbering of the tool, and they hold for the tree hash it
prints (`mutate -list`), not for the tree in general: the numbering is by
position, made from the text of the declared files. The ids and the file:line
below are those of the final tree a905557, hash
`9c06ad3da6d3addb43c0ece631eb5a25953eacbd489b1c752eec209ac9525d94`: struct 272,
calls 269, prices 104 (645 mutants). After another change of the declared
files, compare the lists before reusing an id.

How the final tree was run. A full run of 639 mutants on 0b76469 (hash
`0d85634e...`): killed by a test 619, did not build 5 (fixed in the tool since:
they build), timeout 4, survived 11. Then a rerun with `-only` on a905557 of 98
mutants (every mutant not killed by a test on 0b76469, every new or changed one,
every row of `workcmp.go` and `int.go`): killed by a test 93, timeout 2,
survived 5. Those 5 are the table below.

| id | place | changes |
|---|---|---|
| struct-0124 | `work.go:184` | nothing |
| struct-0205 | `library.go:1499` | nothing |
| struct-0206 | `library.go:1441` | nothing |
| struct-0105 | `work.go:350` | nothing on a 64-bit platform |
| struct-0134 | `eval.go:2239` | Go memory of a deep stack |

struct-0011 (`value.go:1749`), struct-0262 (`onPath`, `value.go:1713`) and
struct-0097 (`encodePath.contains`, `lib/json/json.go:861`) are equivalent in the
result, the error and the counters, but not in time, and time is what the budget
is for: they are killed by white-box tests of the set (their sections below),
and are not unobservable.

Two mutants are caught only by the time limit of the run, with no assertion:
struct-0008 (`value.go:1188`, `Tuple.Freeze` recursive instead of `freezeTree`)
and struct-0010 (`value.go:1511`, `freezeTree` walks a shared tuple again): the
freeze of a DAG of shared tuples (`t = (t, t)` repeated) becomes exponential.
No test asserts it by a count; the run catches them by its timeout, which is a
weaker kill than an assertion. The former timeouts struct-0057 (the
int-to-string limit in the form) and struct-0130 (the recursion scan switches to
the map) are asserted now: `TestG6FormOfABigIntBeyondTheLimitIsRefused` and
`TestG6RecursionScanSwitchesToTheMapBeyondTheDepth`.

A mutant that looked like these and is not equivalent: see the end.

## struct-0124: `regate` without `max(..., 1)`

`left = max(thread.maxWork-thread.extraWork, 1)` becomes
`left = thread.maxWork-thread.extraWork` (`work.go:184`).

The statement is inside `if thread.maxWork > thread.extraWork` (`work.go:183`),
and both are `uint64`, so the difference is at least 1 and `max(d, 1) == d`. The
case that needs the floor, `maxWork <= extraWork`, is the `left := uint64(1)` of
`work.go:182`, which the mutation does not touch.

| observable | changes? | why |
|---|---|---|
| result, error | no | the gate `thread.maxSteps` is the same number for every state of the thread |
| `Work()`, `AllocatedBytes()`, `Steps` | no | they are not read by `regate`; the gate decides only when `stepGate` (`work.go:194`) looks at the limits, and it is the same step |

## struct-0105: `words64` as `len(b.Bits())`

`uint64(b.BitLen()+63) / 64` becomes `uint64(len(b.Bits()))` (`work.go:350`).

`big.Int.Bits` is the normalised slice of `big.Word`: no word of zero at the
top, empty for zero. Its length is `ceil(BitLen/64)` when a `big.Word` is 64
bits (`bits.UintSize == 64`: amd64, arm64, the platforms that the budgets are
calibrated on), which is the formula. For zero both are 0; the sign is not in
`Bits`, and not in `BitLen`.

| observable | changes? | why |
|---|---|---|
| result, error | no | `words64` is only a price |
| `Work()`, `AllocatedBytes()` | no on 64-bit | the same number of words at every call site |
| `Steps` | no | not charged by it |

On a platform of 32-bit words `len(b.Bits())` is up to twice the formula and
the prices double: this is the reason the function is written as it is (the
comment above `words64`, `work.go:347`). The tests run on 64-bit, so the mutant
is equal there.

## struct-0134: `leaveFunction` leaves the zero counts in the map

`if thread.active[f]--; thread.active[f] == 0 { delete(thread.active, f) }`
becomes `thread.active[f]--` (`eval.go:2239`).

`thread.active` is read in two places only, both in `enterFunction`:
`thread.active[f] > 0` (`eval.go:2229`) and `thread.active[f]++`
(`eval.go:2232`). A key with count 0 and an absent key give the same `0` to
both. Nothing ranges over the map or takes its length. The map is dropped
(`thread.active = nil`, `eval.go:2243`) as soon as a call leaves with the stack
at most `recursionScanDepth` deep, so it does not outlive a deep stack.

| observable | changes? | why |
|---|---|---|
| result, error | no | "called recursively" is decided by `> 0` at `eval.go:2229` |
| `Work()` | no | the work of a deep call is `deepCallWork` by the depth of `thread.stack` (`interp.go:38`), not by the map |
| `AllocatedBytes()`, `Steps` | no | the map is not charged |

What changes: while the stack stays deeper than 32, the map keeps one entry for
each function that was active once, not only for the active ones: at most the
number of functions of the program, in Go memory that is not charged.

## struct-0011 and struct-0262: the cycle check of the form without the set (equivalent in results and counters, NOT in time: killed)

struct-0011: `if len(w.path) == pathSetDepth` becomes `if len(w.path) == 1<<30`
(`value.go:1749`): the set `pathSet` is never made, so `onPath` always takes
`pathContains`, the linear search of the slice.

struct-0262: `onPath` (`value.go:1713`) does not consult the set
(`if w.pathSet != nil` is false): the set is made and kept by `push` and `pop`
and never read, and `onPath` takes `pathContains`.

The two answer the same: the path holds `*List` and `*Dict` only, pointers that
are comparable, and a set of interface values and `slices.Contains` both
compare them with `==`. The set is made from the whole path and kept in step by
`push` and `pop`, so at every moment it holds what the slice holds; `onPath`
prevents a value from being on the path twice. The charge of `push`
(`DeepFormWork(len(w.path))`, `value.go:1739`) depends on `len(w.path)` and not
on the set, and the mutations do not touch it.

| observable | changes? | why |
|---|---|---|
| result | no | the same cycles are found ("..." at the same place) |
| error | no | the form has no error of its own for a cycle |
| `Work()`, `AllocatedBytes()`, `Steps` | no | same charges: the work is by the length of the path, the bytes by the output |

What changes: the check of a container at depth d costs d comparisons instead of
one lookup, so the form of a value nested n levels deep takes time quadratic in
n (n is at most `MaxValueDepth`, 10000) instead of linear, while the work
counter stays linear: about 300 ns a unit at depth 9000.

Killed by white-box tests (`starlark/pathset_test.go`):
`TestPathSet_ValueWriterHoldsThePathBeyondTheFirstLevels` (struct-0011: after N
pushes beyond `pathSetDepth` the set is not nil and holds exactly N entries, and
the pops take them out; the mutant never makes the set) and
`TestPathSet_OnPathConsultsTheSetFromTheThreshold` (struct-0262: a value that is
only in the set is found, one that is only in the slice is not; the mutant reads
the slice). No guard asserts a wall time, so the time itself is not tested, only
the set that makes it linear.

## struct-0097: `json.encode`, the cycle check without the set (equivalent in results and counters, NOT in time: killed)

In `encodePath.contains` (`lib/json/json.go:861`) `if p.set != nil { _, ok := p.set[item]; return ok }`
becomes `if false {...}`, so `slices.Contains(p.stack, item)` always answers: the set
is made and maintained by `push` and `pop` (`json.go:868`, `:880`) and never read.

Same argument as struct-0011: the path and its set hold the same pointers at all
times, `slices.Contains` on `unsafe.Pointer` and the map lookup compare the same
way, and the answer decides only whether `cycle in JSON structure` is returned.
The charge of the node (`work.add(4)`, `json.go:275`) is before the check and
does not depend on it.

| observable | changes? | why |
|---|---|---|
| result, error (type and text) | no | `cycle in JSON structure` is returned for the same nodes |
| `Work()`, `AllocatedBytes()`, `Steps` | no | the charges are by node and by byte |

What changes: time, quadratic in the depth of the value (at most
`MaxValueDepth`), and the Go memory of a set that is built and not used.

Killed by the white-box tests of the encoder's path set
(`lib/json/pathset_internal_test.go`):
`TestEncodePath_ContainsConsultsTheSetFromTheThreshold` (from the threshold
depth `encodePathSetDepth` = 32 a key that is only in the set is found, and one
that is only in the slice is not: the mutant reads the slice and fails it),
`TestEncodePath_SetFromTheThresholdDepth` (the set exists from that depth) and
`TestEncode_CycleDeeperThanTheSetThreshold` (a cycle below the threshold is
still found). No guard asserts a wall time.

## struct-0205: `sortSlice.Less` continues after the stop

The row `if s.stopped { return false // (not reached ...` (`library.go:1499`) is
mutated (the guard removed or its result changed). Every place that sets
`s.stopped` (`Less` and `Swap`) is followed by `panic(sortStop{})`, which leaves
`stableSort` and is recovered in the caller; so no `Less` is called with
`s.stopped` true, and the guard is unreachable by design. With it removed or
changed no result, error, `Work()`, `AllocatedBytes()` or `Steps` can differ,
because the code it guards never runs: the mutant survives by construction.
(The property that matters, no comparison after the first error, is held by a
test that counts the calls of `Less` and `Swap` after the error:
`TestG6SortIsLeftAtTheFirstComparisonError`.)

## struct-0206: the work of the comparisons is charged after a refusal of a limit too

In the sort (`library.go:1441`) the row
`if !isResourceError(slice.err) { if err := slice.m.flush(); ...` is mutated, so
the work of the comparisons is flushed after a refusal of a limit too, where the
original does not.

`meter.flush` sets `charged = units` before it calls `ChargeWork`
(`work.go:264-269`), and after the stop neither `Less` nor `Swap` adds units, so
the extra flush of the mutant charges 0 units. `ChargeWork(0)` on the cancelled
thread returns the recorded refusal (the `*WorkBudgetError`), the error that the
sort is returning already.

| observable | changes? | why |
|---|---|---|
| result, error (type, fields, text) | no | the flush returns the refusal that is recorded |
| `Work()` | no | 0 units are charged; it stays at the limit |
| `AllocatedBytes()`, `Steps` | no | not touched |
| a run after `Uncancel` | no | `Work()` is the same number |

`TestG6SortedRefusalLeavesTheWorkAtTheLimit` (`starlark/g4_test.go`) holds
`Work()` at the limit after the refusal and after `Uncancel`; it passes on the
mutant, as it must.

## Not equivalent

`pathSetDepth` 32 to 16 (`value.go:1703`) changes `Work()` (8 units per
container at nesting 17 to 32, `DeepFormWork`). It is killed by
`TestMU_FormOfANestedListChargesFromDepth32`.
