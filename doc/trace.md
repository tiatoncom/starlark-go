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
program, format version 16; a program of version 15 or 14 is refused). It is side data:
the interpreter reads the table, never the code, to tell what a jump or a
comparison is. The `Branch` a host gets has these fields and no others:
`PC`, `Kind`, `Cond`, `Chain`, `Keyword`, `Operand`, `Negated`, `ChainID`,
`ParentChain`. What the interpreter itself reads (the flags below: Flip, Echo,
Short, CmpPC) is kept apart, in `Funcode.BranchMech`, a slice of the internal
package with the index of `Funcode.Branches`; a host cannot see it and
`starlark` exports nothing of it. The saved format carries both. Fields:

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
the interpreter undoes it (the Flip flag of the table): `taken` is the truth of
the expression whatever the code does, and `tested`, whose value the `NOT` has
replaced, is `Bool(taken)`.

**`tested` is not uniform under a computed `not`.** For the last operand of a
chain under a `not` that is evaluated as a value, `tested` is `Bool(taken)`: the
real value is gone. For the earlier operands of the same chain (`a` of
`not (a and b) and c`, where `b` is the last), `tested` is their real value, as everywhere else. The
difference is the mechanics of Flip: the jump after the `NOT` sees the opposite
truth, and the interpreter cancels it by flipping `taken`; the `NOT` has already
replaced the value on the stack by a `Bool`, so `Bool(taken)` is all that is left
of it. The operands' own jumps come before the `NOT` and see the values
themselves. A consumer that needs the value of the last operand cannot have it
here; `taken` and `Negated` are always right.

### Echo: what is not reported

A chain evaluated as a value (a chain in parentheses, a chain that is not the
last operand: `(a and b)`, `a or b or c`) is closed by a jump that tests its
result. When an operand short-circuits, that jump tests again the value that the
operand's own jump has just reported; the interpreter does not report it.
The Echo flag marks such a jump in the table (compile time), the Short flag
marks the operand jumps that can start an echo (the value that ends the chain:
truthy for `or`, falsy for `and`), and the interpreter keeps one bit per call
(whether the previous traced jump short-circuited). When the last operand of the
chain was evaluated, the closing jump is that operand's only test and is
reported. The result is what the consumer reads: the last event of a chain is the
one that decided it, and an operand with no event was not evaluated.

### Comparisons

The CmpPC flag of the table is the address of the comparison whose result the jump tests, or
`-1`; the interpreter, after a comparison, asks the table
(`Funcode.BranchForCompare`: a binary search of a sorted index, built when the
function is compiled or decoded). `OnCompare` therefore fires for a comparison at
any place: the first, a middle or the last operand of a chain, in a chain of its
own, under `not`, and for `not in` (`op` is `in`, `result` is the result of `in`,
`Negated` the `not`).

## Static access to the table

A host that marks up the source (an editor, a catalogue of conditions) needs the
chain numbers, `Operand`, `Negated` and `Keyword` without running the function,
and the same as the hooks will report. `Program.Funcodes` gives the table as the
compiler made it:

```go
func (prog *Program) Funcodes() []FuncodeInfo

type FuncodeInfo struct {
	Name       string          // "lambda" for a lambda, "<toplevel>" for the toplevel
	Pos        syntax.Position // the def / lambda token: what Function.Position() reports
	IsToplevel bool            // what Function.IsToplevel() reports
	Branches   []Branch        // sorted by PC; nil when the function has no condition
	Chains     []ChainInfo     // every and/or chain, by ChainID; nil when there is none
}

type ChainInfo struct {
	ChainID       int             // the index in Chains
	ParentChain   int             // ChainID of the chain around it, or -1
	ParentOperand int             // its operand number in the parent's flat list, or -1
	Kind          BranchKind      // BranchAnd or BranchOr
	Start         syntax.Position // the Chain of its Branches
}
```

* **Every function of the program**: the toplevel first, then the defs and
  lambdas, nested ones included, in the order of the program's function pool
  (the order in which the compiler numbers them: a nested function is listed
  before the function around it, since it is finished first, and several
  closures of one def are one entry). The order depends on the source alone and
  is saved with the program, so a program read back by `CompiledProgram` lists
  the same functions in the same order, with the same `Branches` (the table is
  part of the format since version 15, the chain table since 16; the bytecode
  is unchanged by both).
* **The same values as the hooks.** The `Branch` of an event is, field by field,
  an element of `Branches` of the entry whose `Pos` is `fn.Position()` (find it
  by `ChainID`, `Operand` and `Cond`; `PC` is the address). The tests check this
  against the oracle (`trace_oracle_test.go`, every event of 4000 random trees)
  and on a program with a toplevel, nested defs and a lambda, also after
  `Write` / `CompiledProgram`. A `Branch` in the table that the interpreter
  reports no event for (the closing jump of a chain that short-circuited, see
  Echo) is still there.
* **Chains: all of them.** `Chains` lists every and/or chain of the conditions of
  the function, in the order of `ChainID`: `Chains[i].ChainID == i`, the numbers
  are exactly `0..n-1`, and the `ChainID` and `ParentChain` of every `Branch`,
  and the `ParentChain` of every `ChainInfo`, index this slice. The parent of a
  chain is numbered before it. Where the table of `Branches` has gaps (a number
  that no `Branch` carries) `Chains` still names the chain, so the host needs no
  count of its own:
  * a chain whose operands are all chains of their own has no `Branch`
    (`((a and b) or (c and d)) and ((e and f) or (g and h))`: seven chains,
    numbers 0..6, the outer one is 0 and none of its operands is a leaf);
  * a chain in dead code (after `return`, `break` or `continue`) is numbered
    like any other,
    and so shifts the numbers of the chains after it, but emits no jump, so it
    has no `Branch`;
  * `Kind` is `BranchAnd` or `BranchOr`; `Start` is the `Chain` of its
    `Branch`es (parentheses included, after a `not`).
  * `ParentOperand` is the slot of the chain in the parent: the number of the
    operand in the parent's flat list, the number a `Branch` of that parent
    carries in `Operand` (`a and (b or c) and d`: the chain `b or c` is operand
    1 of its parent). `-1` for a chain that is not inside another; then
    `ParentChain` is `-1` too.

  The chain table is part of the saved format (version 16), so a program read
  back by `CompiledProgram` gives the same `Chains` as the one compiled from
  source.
* **Positions across programs.** A `syntax.Position` holds a pointer to the file
  name, so two `Position`s from different compilations of one source are not
  `==`: compare `Filename()`, `Line` and `Col`.
* The slices are copies. The interpreter's flags (Echo, Short, Flip, CmpPC) are
  not part of `FuncodeInfo` and not reachable from it.

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

* `TestUnpackErrorBadType` (`starlark/eval_test.go`): upstream's; a hang was
  seen in one environment and is not related to this change. It is not touched
  here; if it stops `go test ./...`, run
  `go test -skip TestUnpackErrorBadType ./...`.
* The `Span` of some nodes of `syntax` (upstream's) ends at the position of the
  closing bracket, not after it, unlike `ParenExpr`, `CallExpr` and the rest,
  whose `Span` end is the position after the last character. Probed on
  `a[1]` (`IndexExpr`: end column 4, the `]` is at 4, after it is 5), `a[1:2]`
  (`SliceExpr`: end column 6, after it is 7) and `()` (the `TupleExpr` with
  `Lparen` set, which the parser makes only for the empty tuple: end column 2,
  after it is 3). A non-empty tuple in parentheses is a `ParenExpr` around a
  `TupleExpr` without `Lparen`: `(a, b)` is a `ParenExpr` ending after the `)`
  (correct), its inner `TupleExpr` runs from `a` to after `b`; `(a,)` is a
  `ParenExpr` ending after the `)` (correct), its inner `TupleExpr` ends after
  `a`, the trailing comma not included. `ParenExpr` itself is right: its end is
  `Rparen.add(")")`. A consumer that needs the end of an index, a slice or an
  empty tuple adds one column; the fork does not change `syntax`, because
  `Span` is public API of upstream and the branch table uses only `Start`.
  (Probe: `ParseExpr` of each source, `Span()` of the root.)
* `go vet` warns about `starlark/int_posix64.go:69` (upstream's `unsafe.Pointer`).
