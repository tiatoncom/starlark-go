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
| `OnCompare(th, fn, b, op, x, y, result)` | after a comparison (`== != < <= > >=`, `in`, `not in`) whose result a branch tests, just before that branch's `OnBranch` |
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

## The branch table

The compiler records, for each conditional jump that tests a condition of the
source, a `Branch` in `Funcode.Branches` (sorted by pc; saved with the compiled
program, format version 15). Its fields:

* `Kind`: `BranchIf`, `BranchElif` (an `IfStmt` whose `If` is the parent's
  `ElsePos`; `else:` followed by `if` is `BranchIf`), `BranchWhile`,
  `BranchCondExpr`, `BranchCompIf`, or `BranchAnd` / `BranchOr` for an operand of
  an and/or chain inside a condition.
* `Cond`: `syntax.Start` of the condition handed to the compiler, parentheses
  and a leading `not` included (for an operand: of the operand).
* `Keyword`: the `if` / `elif` / `while` token, the `if` of a conditional
  expression or comprehension clause, or the `and` / `or` that joins an operand
  to the previous one (the first operator for operand 0).
* `Operand`, `Chain`: `-1` and `Cond` outside a chain. In a chain: the operand's
  number in the flat list of the chain, from 0 (`a and b and c` has three
  operands), and `syntax.Start` of the chain as it stands (with its
  parentheses; after a `not` it is the expression after the `not`).
* `Negated`: an odd number of `not` / `not in` swaps lies between the construct
  and the jump.

A chain of the other operator, or in parentheses, standing as an operand is a
chain of its own, numbered from 0, and has no `Branch` for the operand it
forms: the `Branch` of its last operand tests it.

```
a and (b or c)   and: a#0, "(b or c)"#1 (no Branch); or (Chain "("): b#0, c#1
not (a and b)    and (Chain "(", Negated): a#0, b#1
a or b and c     or: a#0, "b and c"#1 (no Branch); and (Chain "b"): b#0, c#1
```

`taken` is the truth of `tested`. When an operand short-circuits, the tests of
the same value that the compiled code makes again on its way out of the chain
are not reported, so the last event of a chain is the one that decided it.

And/or outside a condition (`x = a and b`, an argument of a call in a
condition) have no `Branch` and no event.

## Calls

`OnEnter` receives a `CallSite`: the position of the calling frame, or
`Synthetic` when the call comes from a built-in (`sorted(xs, key=f)`) or from Go.
`fn.IsToplevel()` tells the toplevel of a file from a `def`. The arguments are
the parameters by name with the values bound to them, defaults and `*args` /
`**kwargs` included.
