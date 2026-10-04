# Trace hooks

A host program can watch which branch of a condition a Starlark function took,
which comparison decided it, and when a function written in Starlark starts and
ends. Set `Thread.Trace` to a `*starlark.Tracer`; every hook may be nil.

```go
thread := &starlark.Thread{Trace: &starlark.Tracer{
	OnBranch: func(th *starlark.Thread, fn *starlark.Function, b starlark.Branch, tested starlark.Value, taken bool) { ... },
}}
```

| hook | when |
|---|---|
| `OnBranch(th, fn, b, tested, taken)` | at each conditional jump that tests a condition of the source, before the jump |
| `OnCompare(th, fn, b, op, x, y, result)` | after a comparison (`== != < <= > >=`, `in`, `not in`) whose result a branch tests, just before that branch's `OnBranch`; wherever the comparison stands among the operands of a chain |
| `OnEnter(th, fn, call, args)` | after the arguments are bound to the parameters, before the first instruction |
| `OnExit(th, fn, result, err)` | when the function returns or fails; not when it is left by a panic |

## What a hook costs and what it does not

A hook adds no instruction. `Funcode.Code` is byte for byte what it was without
hooks, so `Thread.Steps`, `SetMaxExecutionSteps` and every limit built on them
are the same with and without a `Tracer`, and the same with any hook. Hooks do
not count as steps. With `Thread.Trace == nil` the interpreter pays one
comparison per conditional jump, comparison, call and return.

A hook must not run Starlark on the thread it observes: `Call` from inside a
hook panics.

**With `Trace == nil`**: +1.0 to +2.3 % on the interpreter-bound benchmarks
(`bench_gauss`, `bench_int`, `bench_mix`, `bench_builtin_method`,
`bench_range_iteration`, `Branchy`; `bench_calling` within noise), geomean
+1.5 %. Per place that has the check (a conditional jump, a comparison, a call,
a return: 1180 per run of `Branchy`) that is about 0.3 ns. Allocations and bytes
per operation are equal to v0.2.0 everywhere. Method: an A/B comparison of two
binaries (v0.2.0 and this tree) that alternate within every round, the one that
starts a round rotating, 20 rounds, `benchstat` over n=20; two separate runs of
one binary on this machine differ by about as much, so single runs prove
nothing. The same method finds no difference beyond noise (one benchmark, +1 %,
in one of two runs) between a nil `Trace` with the table-driven lookups of
comparisons and echoes and the earlier version that scanned the code at run time.

**With all four hooks set** (and doing nothing): about 8 ns per event
(`BranchyAll`: 489 branches, 489 comparisons, 101 enters and 101 exits per run,
+9.5 us over `Branchy`), and no allocation: the `args` slice of `OnEnter` is a
buffer of the thread, reused by every call (before, it was one allocation per
call: 472 allocations and 21.9 KiB per run of `Branchy` against 371 and 15.6 KiB
without a Tracer; now 371 and 15.6 KiB, the same). The slice is valid until the
hook returns; a hook that keeps the arguments copies them. A `Tracer` none of
whose hooks is set costs one more comparison per event (`BranchyEmpty`, +5 %).

## The branch table

The compiler records, for each conditional jump that tests a condition of the
source, a `Branch` in `Funcode.Branches` (sorted by pc; saved with the compiled
program, format version 15; a program of version 14 is refused). It is side data:
the interpreter reads the table, never the code, to tell what a jump or a
comparison is. Its fields:

* `Kind`: `BranchIf`, `BranchElif` (an `IfStmt` whose `If` is the parent's
  `ElsePos`; `else:` followed by `if` is `BranchIf`), `BranchWhile`,
  `BranchCondExpr`, `BranchCompIf`, or `BranchAnd` / `BranchOr` for an operand of
  an and/or chain inside a condition.
* `Cond`: `syntax.Start` of the condition handed to the compiler, parentheses
  and a leading `not` included (for an operand: of the operand, its own `not`
  included).
* `Keyword`: of the construct, the `if` / `elif` / `while` token, the `if` of a
  conditional expression or comprehension clause, on the `Branch` of a condition
  that is not a chain. Of an operand of a chain: the `and` / `or` that joins it to
  the previous one; operand 0 has none, its `Keyword` is the zero `Position`
  (`IsValid()` is false). The keyword of the construct is not repeated on the
  operands.
* `Operand`, `Chain`: `-1` and `Cond` outside a chain. In a chain: the operand's
  number in the flat list of the chain, from 0 (`a and b and c` has three
  operands), and `syntax.Start` of the chain as it stands (with its
  parentheses; after a `not` it is the expression after the `not`).
* `ChainID`, `ParentChain`: `-1` outside a chain. Otherwise the number of the
  chain in the function, from 0, in the order in which the chains start in the
  source (an outer chain before the chain that starts at the same place inside
  it), and the number of the chain around it, or `-1`. `(Chain, Operand)` alone
  does not tell the operands of `(a and b) and c`: `b` and `c` are both `and#1`
  of a chain that starts at `(`.
* `Negated`: see below.

A chain of the other operator, or in parentheses, standing as an operand is a
chain of its own, numbered from 0, and has no `Branch` for the operand it
forms: the `Branch` of its last operand tests it.

```
a and (b or c)   and: a#0, "(b or c)"#1 (no Branch); or (Chain "("): b#0, c#1
not (a and b)    and (Chain "(", Negated): a#0, b#1
a or b and c     or: a#0, "b and c"#1 (no Branch); and (Chain "b"): b#0, c#1
```

`not` and parentheses are looked through wherever they stand: in front of a chain
that is not the last operand (`not (a and b) and c`), in parentheses
(`(a and not (b and c))`), twice (`not (not (a and b))`). Redundant parentheses
and `not not` change the positions of the events and nothing else: kinds, operand
numbers, `ChainID` / `ParentChain`, `taken`, `Negated`, comparisons and their
order are the same.

And/or outside a condition (`x = a and b`, an argument of a call in a
condition) have no `Branch` and no event.

### taken, Negated and the side that ran

`taken` is "the condition is true": `tested.Truth()`, the truth of the
expression itself. It is **not** "the jump was made". `Branch.Negated` says that
an odd number of `not` / `not in` (wherever they stand, however the compiler
evaluates them) lies between the construct and the expression. The side of the
condition that the source follows is

    thenSide = taken XOR Negated

(true: the "then" side; for an operand of a chain, the operand counts as true for
the chain). The last event of a run of a condition always says which branch ran:

| condition | `taken` | `Negated` | then-side |
|---|---|---|---|
| `x` | `x` | no | `taken` |
| `not x` | `x` | yes | `!taken` |
| `not not x` | `x` | no | `taken` |
| `x in y` | `x in y` | no | `taken` |
| `x not in y` | `x in y` | yes | `!taken` |
| `not (x not in y)` | `x in y` | no | `taken` |
| `not (a and b)` | each operand: its own truth | yes | the last event: `!taken` |
| `not (a and b) and c` | a, b: own truth; c: `c` | a, b yes; c no | the last event: c's |

The compiler may evaluate a `not` as a value (`not (a and b) and c`: the chain,
then a `NOT` instruction, then the jump). The jump then sees the opposite truth;
the interpreter undoes it (`Branch.Flip`): `taken` is the truth of the
expression whatever the code does, and `tested`, whose value the `NOT` has
replaced, is `Bool(taken)`.

### Echo: what is not reported

A chain evaluated as a value (a chain in parentheses, a chain that is not the
last operand: `(a and b)`, `a or b or c`) is closed by a jump that tests its
result. When an operand short-circuits, that jump tests again the value that the
operand's own jump has just reported; the interpreter does not report it.
`Branch.Echo` marks such a jump in the table (compile time), `Branch.Short`
marks the operand jumps that can start an echo (the value that ends the chain:
truthy for `or`, falsy for `and`), and the interpreter keeps one bit per call
(whether the previous traced jump short-circuited). When the last operand of the
chain was evaluated, the closing jump is that operand's only test and is
reported. The result is what the consumer reads: the last event of a chain is the
one that decided it, and an operand with no event was not evaluated.

### Comparisons

`Branch.CmpPC` is the address of the comparison whose result the jump tests, or
`-1`; the interpreter, after a comparison, asks the table
(`Funcode.BranchForCompare`: a binary search of a sorted index, built when the
function is compiled or decoded). `OnCompare` therefore fires for a comparison at
any place: the first, a middle or the last operand of a chain, in a chain of its
own, under `not`, and for `not in` (`op` is `in`, `result` is the result of `in`,
`Negated` the `not`).

## Calls

`OnEnter` receives a `CallSite`: the position of the calling frame, or
`Synthetic` when the call comes from a built-in (`sorted(xs, key=f)`) or from Go.
`fn.IsToplevel()` tells the toplevel of a file from a `def`. The arguments are
the parameters by name with the values bound to them, defaults and `*args` /
`**kwargs` included. The `args` slice is valid until the hook returns.

`OnExit` is called for every `OnEnter`, on every way the function ends: it
returns, its body fails, the step limit stops it, the thread is cancelled. A call
refused before the body runs (wrong arguments, recursion) has neither `OnEnter`
nor `OnExit`. A function left by a Go panic has no `OnExit`.

## Known

* `TestUnpackErrorBadType` (`starlark/eval_test.go`) is upstream's and is
  reported to hang on a clean v0.2.0 (it did not in the runs for this change). It
  is not touched here; if it stops `go test ./...`, run
  `go test -skip TestUnpackErrorBadType ./...`.
* `go vet` warns about `starlark/int_posix64.go:69` (upstream's `unsafe.Pointer`).
