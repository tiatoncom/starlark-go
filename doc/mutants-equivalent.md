# Mutants that no test can kill

The mutation run (`internal/cmd/mutate`) changes one place of the resource
accounting at a time and runs the tests of `./starlark` and `./lib/json`. A
mutant that survives is either a missing test or a change that no program can
see. This document is the second kind, with the proof for each: what the
mutation is, and for each observable of a run (the result of the program, the
error, `Thread.Work()`, `Thread.AllocatedBytes()`, `Thread.Steps`) whether it
changes and why. What changes is time or Go memory that is not charged.

Ids are those of the numbering of the tool, and they hold for the tree hash it
prints (`mutate -list`), not for the tree in general: the numbering is made from
the text of the declared files. They were made and run for the tree
`a7bca9f5e72c5fc50435aa11af5cdcb3f5ec3c9877da3658d9efd3b47da8a89c` (62cca72);
the comments of 242d9c5 changed the hash to
`42203a1b66b358f2867f665269f81721b19b687391b8bfede7fb4e0256e2cdfd`, and every
id there names the same mutation (the lists of the three groups compared, id by
id). The price of the deep call (120, not 56) and the comments of the
calibration by platform changed it to
`4e84123c9c04da645b2f39f956d609c72cac07d1edbd6f8c02c830024f75c678`: the two
rows of `deepCallWork` follow the new price (struct-0168, struct-0169, killed by
`TestFixed_TheCallThatIsDeep`), and every other id names the same mutation as
before. After another change of the declared files, compare the lists before
reusing an id. File:line are of the unmutated source of that last tree.

| id | place | changes |
|---|---|---|
| struct-0128 | `work.go:180` | nothing |
| `sortSlice.Less`: continues after the stop | `library.go:1444` | nothing |
| struct-0109 | `work.go:346` | nothing on a 64-bit platform |
| struct-0138 | `eval.go:2230` | Go memory of a deep stack |

struct-0011 (`value.go:1718`) and struct-0101 (`encodePath.contains`, `lib/json/json.go:717`) were in
this table. They are equivalent in the result, the error and the counters, but
not in time, and time is what the budget is for: they are killed by tests of
the set and of the time (their sections below), and are not unobservable.

Two mutants that looked like these are not equivalent: see the end.

## struct-0128: `regate` without `max(..., 1)`

`left = max(thread.maxWork-thread.extraWork, 1)` becomes
`left = thread.maxWork-thread.extraWork`.

The statement is inside `if thread.maxWork > thread.extraWork` (`work.go:179`),
and both are `uint64`, so the difference is at least 1 and `max(d, 1) == d`. The
case that needs the floor, `maxWork <= extraWork`, is the `left := uint64(1)` of
`work.go:178`, which the mutation does not touch.

| observable | changes? | why |
|---|---|---|
| result, error | no | the gate `thread.maxSteps` is the same number for every state of the thread |
| `Work()`, `AllocatedBytes()`, `Steps` | no | they are not read by `regate`; the gate decides only when `stepGate` (`work.go:190`) looks at the limits, and it is the same step |

## struct-0109: `words64` as `len(b.Bits())`

`uint64(b.BitLen()+63) / 64` becomes `uint64(len(b.Bits()))` (`work.go:346`).

`big.Int.Bits` is the normalised slice of `big.Word`: no word of zero at the
top, empty for zero. Its length is `ceil(BitLen/64)` when a `big.Word` is 64
bits (`bits.UintSize == 64`: amd64, arm64, the platforms that the budgets are
calibrated on), which is the formula. For zero both are 0; the sign is not in
`Bits`, and not in `BitLen`.

| observable | changes? | why |
|---|---|---|
| result, error | no | `words64` is only a price: `alloc.go:459,499`, `work.go:352,417`, `workcmp.go:41,203` |
| `Work()`, `AllocatedBytes()` | no on 64-bit | the same number of words at every call site |
| `Steps` | no | not charged by it |

On a platform of 32-bit words `len(b.Bits())` is up to twice the formula and
the prices double: this is the reason the function is written as it is (the
comment at `work.go:343`). The tests run on 64-bit, so the mutant is equal
there.

## struct-0138: `leaveFunction` leaves the zero counts in the map

`if thread.active[f]--; thread.active[f] == 0 { delete(thread.active, f) }`
becomes `thread.active[f]--` (`eval.go:2230`).

`thread.active` is read in two places only, both in `enterFunction`:
`thread.active[f] > 0` (`eval.go:2220`) and `thread.active[f]++`
(`eval.go:2223`). A key with count 0 and an absent key give the same `0` to
both. Nothing ranges over the map or takes its length. The map is dropped
(`thread.active = nil`, `eval.go:2234`) as soon as a call leaves with the stack
at most `recursionScanDepth` deep, so it does not outlive a deep stack.

| observable | changes? | why |
|---|---|---|
| result, error | no | "called recursively" is decided by `> 0` at `eval.go:2220` |
| `Work()` | no | the work of a deep call is `deepCallWork` by the depth of `thread.stack` (`interp.go:34-38`), not by the map |
| `AllocatedBytes()`, `Steps` | no | the map is not charged (no charge at `eval.go:2210`) |

What changes: while the stack stays deeper than 32, the map keeps one entry for
each function that was active once, not only for the active ones: at most the
number of functions of the program, in Go memory that is not charged.

## struct-0011: `valueWriter` without the set (equivalent in results and counters, NOT in time: killed)

`if len(w.path) == pathSetDepth` becomes `if len(w.path) == 1<<30`
(`value.go:1718`): the set `pathSet` is never made, `onPath` (`value.go:1705`)
always takes `pathContains`, the linear search of the slice.

The two answer the same: the path holds `*List` and `*Dict` only
(`value.go:1859,1915`), pointers that are comparable, and a set of interface
values and `slices.Contains` both compare them with `==`. The set is made from
the whole path and kept in step by `push` and `pop`, so at every moment it holds
what the slice holds; `onPath` prevents a value from being on the path twice.
The charge `w.work(8)` of `push` (`value.go:1715`) depends on `len(w.path)` and
not on the set, and the mutation does not touch it.

| observable | changes? | why |
|---|---|---|
| result | no | the same cycles are found ("..." at the same place) |
| error | no | the form has no error of its own for a cycle |
| `Work()`, `AllocatedBytes()`, `Steps` | no | same charges: the work is by the length of the path, the bytes by the output |

What changes: the check of a container at depth d costs d comparisons instead of
one lookup, so the form of a value nested n levels deep takes time quadratic in
n (n is at most `MaxValueDepth`, 10000) instead of linear, while the work
counter stays linear: about 300 ns a unit at depth 9000.

Killed by `TestPathSet_ValueWriterHoldsThePathBeyondTheFirstLevels`
(`starlark/pathset_test.go`, white box: after N pushes beyond `pathSetDepth` the
set is not nil and holds exactly N entries, and the pops take them out; the
mutant never makes the set). No guard asserts a wall time, so the time itself
is not tested, only the set that makes it linear.

## struct-0101: `json.encode`, the cycle check without the set (equivalent in results and counters, NOT in time: killed)

In `encodePath.contains` (`lib/json/json.go:717`) `if p.set != nil { _, ok := p.set[item]; return ok }`
becomes `if false {...}`, so `slices.Contains(p.stack, item)` always answers: the set
is made and maintained by `push` and `pop` (`json.go:725`, `:737`) and never read.
(In the tree the ids were made for, this was an inline `pathSet` in `encodeTo`,
`json.go:277`; the encoder's path is now the type `encodePath`.)

Same argument as struct-0011: `path` and `pathSet` hold the same pointers at all
times, `slices.Contains` on `unsafe.Pointer` and the map lookup compare the same
way, and the answer decides only whether `cycle in JSON structure` is returned.
The charge of the node (`work.add(4)`, `json.go:274`) is before the check and
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

## `sortSlice.Less`: continues after the stop

The row `if s.stopped { return false // (not reached ...` (`library.go:1444`) is
mutated (the guard removed or its result changed). Every place that sets
`s.stopped` (`Less` and `Swap`) panics with `sortStop` in the same statement
sequence, and `stableSort` is left by that panic, recovered in the caller; so no
`Less` is called with `s.stopped` true, and the guard is unreachable by design.
With it removed or changed no result, error, `Work()`, `AllocatedBytes()` or
`Steps` can differ, because the code it guards never runs: the mutant survives
by construction. (The property that matters, no comparison after the first
error, is held by a test that counts the calls of `Less` after the error: 0.)

struct-0147 (`pathSetDepth` 32 to 16, `value.go:1703`) is not equivalent: it
changes `Work()` (8 units per container at nesting 17 to 32). It is killed by
`TestMU_FormOfANestedListChargesFromDepth32`.
