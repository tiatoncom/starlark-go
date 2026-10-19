# Mutants that no test can kill

The mutation run (`internal/cmd/mutate`) changes one place of the resource
accounting at a time and runs the tests of `./starlark` and `./lib/json`. A
mutant that survives is either a missing test or a change that no program can
see. This document is the second kind, with the proof for each: what the
mutation is, and for each observable of a run (the result of the program, the
error, `Thread.Work()`, `Thread.AllocatedBytes()`, `Thread.Steps`) whether it
changes and why. What changes is time or Go memory that is not charged.

Ids are those of the numbering of the tool for this tree. File:line are of the
unmutated source.

| id | place | changes |
|---|---|---|
| struct-0128 | `work.go:172` | nothing |
| struct-0109 | `work.go:337` | nothing on a 64-bit platform |
| struct-0138 | `eval.go:2223` | Go memory of a deep stack |
| struct-0011 | `value.go:1718` | time of the form of a deep value |
| struct-0101 | `lib/json/json.go:277` | time of `json.encode` of a deep value |

One mutant that looked like these is not equivalent: see the end.

## struct-0128: `regate` without `max(..., 1)`

`left = max(thread.maxWork-thread.extraWork, 1)` becomes
`left = thread.maxWork-thread.extraWork`.

The statement is inside `if thread.maxWork > thread.extraWork` (`work.go:171`),
and both are `uint64`, so the difference is at least 1 and `max(d, 1) == d`. The
case that needs the floor, `maxWork <= extraWork`, is the `left := uint64(1)` of
`work.go:170`, which the mutation does not touch.

| observable | changes? | why |
|---|---|---|
| result, error | no | the gate `thread.maxSteps` is the same number for every state of the thread |
| `Work()`, `AllocatedBytes()`, `Steps` | no | they are not read by `regate`; the gate decides only when `stepGate` (`work.go:182`) looks at the limits, and it is the same step |

## struct-0109: `words64` as `len(b.Bits())`

`uint64(b.BitLen()+63) / 64` becomes `uint64(len(b.Bits()))` (`work.go:337`).

`big.Int.Bits` is the normalised slice of `big.Word`: no word of zero at the
top, empty for zero. Its length is `ceil(BitLen/64)` when a `big.Word` is 64
bits (`bits.UintSize == 64`: amd64, arm64, the platforms that the budgets are
calibrated on), which is the formula. For zero both are 0; the sign is not in
`Bits`, and not in `BitLen`.

| observable | changes? | why |
|---|---|---|
| result, error | no | `words64` is only a price: `alloc.go:458,498`, `work.go:343,408`, `workcmp.go:41,203` |
| `Work()`, `AllocatedBytes()` | no on 64-bit | the same number of words at every call site |
| `Steps` | no | not charged by it |

On a platform of 32-bit words `len(b.Bits())` is up to twice the formula and
the prices double: this is the reason the function is written as it is (the
comment at `work.go:334`). The tests run on 64-bit, so the mutant is equal
there.

## struct-0138: `leaveFunction` leaves the zero counts in the map

`if thread.active[f]--; thread.active[f] == 0 { delete(thread.active, f) }`
becomes `thread.active[f]--` (`eval.go:2223`).

`thread.active` is read in two places only, both in `enterFunction`:
`thread.active[f] > 0` (`eval.go:2213`) and `thread.active[f]++`
(`eval.go:2216`). A key with count 0 and an absent key give the same `0` to
both. Nothing ranges over the map or takes its length. The map is dropped
(`thread.active = nil`, `eval.go:2227`) as soon as a call leaves with the stack
at most `recursionScanDepth` deep, so it does not outlive a deep stack.

| observable | changes? | why |
|---|---|---|
| result, error | no | "called recursively" is decided by `> 0` at `eval.go:2213` |
| `Work()` | no | the work of a deep call is `deepCallWork` by the depth of `thread.stack` (`interp.go:34-38`), not by the map |
| `AllocatedBytes()`, `Steps` | no | the map is not charged (no charge at `eval.go:2203`) |

What changes: while the stack stays deeper than 32, the map keeps one entry for
each function that was active once, not only for the active ones: at most the
number of functions of the program, in Go memory that is not charged.

## struct-0011: `valueWriter` without the set

`if len(w.path) == pathSetDepth` becomes `if len(w.path) == 1<<30`
(`value.go:1718`): the set `pathSet` is never made, `onPath` (`value.go:1705`)
always takes `pathContains`, the linear search of the slice.

The two answer the same: the path holds `*List` and `*Dict` only
(`value.go:1859,1915`), pointers that are comparable, and a set of interface
values and `slices.Contains` both compare them with `==`. The set is made from
the whole path and kept in step by `push` and `pop`, so at every moment it holds
what the slice holds; `onPath` prevents a value from being on the path twice.
The charge `w.work(8)` of `push` (`value.go:1714`) depends on `len(w.path)` and
not on the set, and the mutation does not touch it.

| observable | changes? | why |
|---|---|---|
| result | no | the same cycles are found ("..." at the same place) |
| error | no | the form has no error of its own for a cycle |
| `Work()`, `AllocatedBytes()`, `Steps` | no | same charges: the work is by the length of the path, the bytes by the output |

What changes: the check of a container at depth d costs d comparisons instead of
one lookup, so the form of a value nested n levels deep takes time quadratic in
n (n is at most `MaxValueDepth`, 10000) instead of linear.

## struct-0101: `json.encode`, the cycle check without the set

`if pathSet != nil { _, cycle = pathSet[ptr] } else { cycle = pathContains(path, ptr) }`
becomes `if false {...} else {...}` (`lib/json/json.go:277`): the set is made
and maintained (`json.go:286-296`) and never read.

Same argument as struct-0011: `path` and `pathSet` hold the same pointers at all
times, `slices.Contains` on `unsafe.Pointer` and the map lookup compare the same
way, and the answer decides only whether `cycle in JSON structure` is returned.
The charge of the node (`work.add(4)`, `json.go:270`) is before the check and
does not depend on it.

| observable | changes? | why |
|---|---|---|
| result, error (type and text) | no | `cycle in JSON structure` is returned for the same nodes |
| `Work()`, `AllocatedBytes()`, `Steps` | no | the charges are by node and by byte |

What changes: time, quadratic in the depth of the value (at most
`MaxValueDepth`), and the Go memory of a set that is built and not used.

struct-0147 (`pathSetDepth` 32 to 16, `value.go:1703`) is not equivalent: it
changes `Work()` (8 units per container at nesting 17 to 32). It is killed by
`TestMU_FormOfANestedListChargesFromDepth32`.
