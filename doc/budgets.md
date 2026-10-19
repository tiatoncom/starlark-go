# Budgets: steps, work, bytes, stack depth

A host program can bound what a script costs with four limits on a `*starlark.Thread`. Each is set per thread, and `0` means no limit (the stack depth: the default).

| limit | set with | counted by | refused with |
|---|---|---|---|
| steps | `SetMaxExecutionSteps(n)` | `Thread.Steps` | `Cancel("too many steps")`, or `OnMaxSteps`; exactly as in v0.2.0 |
| work | `SetMaxWork(n)` | `Thread.Work()` | `*WorkBudgetError{Limit, Charged, Requested}` |
| bytes | `SetMaxAllocBytes(n)` | `Thread.AllocatedBytes()` | `*AllocBudgetError{Limit, Allocated, Requested}` |
| stack depth | `SetMaxCallStackDepth(n)` | the frames of the stack | the error `Starlark stack overflow` |

A host recognizes the work and byte refusals with `errors.As`; the text of both carries the limit only, because the other numbers derive from the data of the script. A host function that takes time or memory calls `ChargeWork(n)` and `ChargeAlloc(n)` BEFORE it does the work, and returns the error as it is: a built-in of the host has no price in the tables, so nothing else bounds it. The counters are not atomic: only the goroutine that runs the thread reads or changes them; only `Cancel` may come from another goroutine.

## The model

**Steps** are what they were in v0.2.0, byte for byte: the interpreter counts an opcode and nothing else is converted to steps. `Thread.Steps`, `ExecutionSteps()` and the point where a limit stops a program are the same with every operand (the differential tests against v0.2.0 hold it: `TestDifferentialAgainstV020`, `TestDifferentialLargeOperands`, `TestDifferentialReviewPrograms`, `TestDifferentialStepLimit`). The only programs that take other steps than in v0.2.0 are the ones that this version refuses and v0.2.0 ran on (see Compatibility).

**Work** is the time. Each step is one unit; to it the operations add a price for what they do beyond the opcode that starts them: the elements they visit, compare, probe, hash or copy, the bytes they scan, and the memory they allocate (garbage costs the allocator and the collector time as well: 1 unit per 16 bytes of memory that holds pointers, per 96 bytes of memory that does not, `alloc.go`: `allocBytesPerWork`, `allocBytesPerWorkNoscan`). The work is counted whether or not there is a limit.

**Bytes** are the memory a thread has allocated, cumulative: garbage counts, so the counter never decreases and does not depend on the collector. The sizes are declared formulas, not measurements of the heap (header of `alloc.go`): a string or bytes `len`; a list `48 + 16n`; a tuple `32 + 16n` (0 when empty); a dict or set `512 + 128 * max(n - 7, 0)`; a function 96; a big int `base + 8 * words`. The result of a built-in that makes a container or a string is charged by `Call` unless the built-in charged it itself. The count is the same in every run and on every platform.

**Stack depth** counts the frames of a call (a function or a built-in), with or without recursion. The default is `DefaultMaxCallStackDepth = 100_000` (`eval.go:2140`) and `n <= 0` restores it. A frame takes 2.2 KiB of the Go stack (1.8 KiB for a built-in that calls back, `sorted(key=f)`: 5.5 KiB a level, three frames), and on the heap 0.2 KiB (0.3 with a comprehension, 0.8 with keyword arguments or a level through `sorted(key=)`), so the default takes at most 224 MiB of the Go stack, and Go's own maximum of 1 GiB holds 440 000 frames of the deepest kind (`eval.go` comment of `SetMaxCallStackDepth`; `TestMeasureStackPerLevel`). A host that lowers `debug.SetMaxStack` keeps the limit under its maximum / 2.4 KiB.

**What holds with no limit set.** The work is counted. And a few ceilings are constants, because what they prevent is a crash of the process that `recover` cannot catch:

| ceiling | value | where |
|---|---|---|
| one result of one operation | 1 GiB (`maxAlloc`): refused before it is allocated, with an error whose text contains `excessive` (`excessive allocation (N bytes)`, `list: excessive size (...)`, `excessive repeat (...)`, ...) | `eval.go:1456`, checked in `charge` (`alloc.go`) |
| decimal digits of an integer converted to or from a string | `MaxIntDigits` = 4300 | `value.go:1588` |
| bits of that integer in the string form of a value (host `String()`) | `MaxIntBits` = 14285 | `value.go:1591` |
| nesting of containers that the recursive operations descend (string form, `Hash`, `Freeze`, json) | `MaxValueDepth` = 10000 | `value.go:1578` |
| depth of the stack of calls | `DefaultMaxCallStackDepth` = 100 000 | `eval.go:2140` |
| an operand named in an error text | 256 bytes of its form (`errValueLimit`) | `value.go:1600` |

**Refusal.** A request that does not fit is refused, not run, where its size is known in advance (a price computed from the lengths of the operands, charged by `Call` before the call). Where it is not (an iterator, a search that stops at the first match, a comparison) the operation charges as it goes, to a meter, in chunks of `flushWork` = 1024 units (`work.go:226`) and at its end. So the refusal is exact: when the limit is reached the error is returned, the thread is cancelled (so that a host built-in that swallows an error is stopped at the next opcode), and `Work()` is the limit, not more (`workExhausted`, `work.go:148`). What can pass the limit is the real work done after the last charge, at most one chunk of 1024 units. For the byte budget a request that would take `AllocatedBytes()` over the limit allocates nothing and is not charged; a request for exactly what is left succeeds. An operation that fails half way is not undone (a list extended by an iterator that is refused keeps the elements it got); the refusal ends the execution, so a script does not see it. The limit is that of one thread: the threads that a program starts itself (`load`) are not limited unless the host sets the limit on each. The limit of work bounds CPU, not the clock on the wall: a goroutine that waits is not working, so a host that must bound the elapsed time calls `Thread.Cancel`, which is noticed at the next opcode and at the next charge.

**Determinism.** The prices are integers computed from sizes; there is no clock in the count. The hash of a string is a fixed function of the string (`hashString`, `hashtable.go`: no random seed, no `maphash`), because the work and the steps of a hash table depend on the lengths of its chains; the protection against keys chosen to collide is the charge of the chain, not the secrecy of the hash. The same program on the same input is charged the same steps, work and bytes in every process (`TestDeterminism_*`, `TestAllocBudget_DeterministicAcrossRuns`).

## C and K

**C** is per platform. The units of work are integers computed from sizes: deterministic, and the same on every machine. C only translates units into time on a given machine, so each calibrated platform has its own, and the limits of the engine are set in units, not in seconds. On the reference platform, darwin/arm64, C is `workNanoseconds` = 12 (`work.go`). The invariant is one sentence: a program that has done W units of work has used at most C * W nanoseconds of CPU on that platform. C is the ceiling of what a unit costs, not its average: the loop probes of the interpreter itself (`for i in range(N): pass`, `x = len(l)`, a def call, `d[i] = i`, `l.append(i)`) take 1.5 to 11.2 ns a unit ([prices.md](prices.md)), and the prices of the operations are set so that no probe goes over 12.

It was measured with 308 probes (`calibrate_probes_test.go`): each is a program that does one operation, on a large operand, in the form that is worst for it (a needle that fails at every position, keys that collide, a big integer in a loop). The harness (`TestCalibrate`) takes the least time of seven runs in a process (the calibration of a platform takes the least over six processes, below), and runs with recursion allowed, so that a probe that builds its value with a recursive `def` runs as written; the floors are in `calibrate_floors_test.go`, and the table with the ratio of each probe is `prices.md`. The worst probe of the priced operations is `any(l) all falsy` at 11.7 ns a unit (11 717 834 ns for 1 000 011 units); the next priced ones are 11.4 (`all(l)`) and 11.0 (`l.append` growth), and the worst loop probe, `loop: l.append(i)`, is 11.18. Two tests hold it: `TestWork_NoProbeCostsMoreThanC` (no probe of 2 us or more costs more than C a unit) and `TestPrices_AtLeastTheMeasuredCost` (the work charged is at least the measured time over C, for every probe of a priced operation). A test cannot depend on the clock, so these two work from the table of floors, which is a file of numbers.

**The rule: C per platform.** Each registered platform has its own floors file (`calibrate_floors*_test.go`) with its table of times and its C; there is no merge of the platforms, and nothing may rely on mixing them. The two tests judge every platform by its own floors with its own C: `TestPrices_AtLeastTheMeasuredCost` (for every platform p, the work charged is at least ns_p / C_p) and `TestWork_NoProbeCostsMoreThanC` (for every platform p, every probe costs at most C_p ns a unit). A probe that has no floor on a platform (it was added after that calibration) is not judged on that platform. The C of the generated file is the ceiling of the dearest ns/unit of any run, each run by its own times, over the probes of 2000 ns or more (not by the least times: a promise of time on a platform must hold in a bad process too), so it is deterministic from the inputs; the C of darwin/arm64 (a table converted by hand) is `workNanoseconds`. What a limit of work means in time on each platform (a limit of 120 000 000 units, for example), at C of that platform:

| platform | C (ns / unit) | a limit of 120M units is at most |
|---|---|---|
| darwin/arm64 (reference) | 12 | 1.44 s |
| linux/amd64, Intel N200 (a test stand, not a production platform) | 34 | 4.08 s |

The N200 stand is there to see how the machinery behaves on a slower, noisier machine; its C does not bind a deployment. A production platform is added when it is calibrated, with its own C.

**The procedure (v2).** A calibration of a platform is six separate processes of `TestCalibrate` (`STARLARK_CALIBRATE_REPS=7`); the floors are the least time of each probe over all six. Each process is preceded by a warm-up of 10 s of CPU, runs with `GODEBUG=disablethp=1` and, on Linux, on cores `1-3` (`taskset`), and has `uptime` and the clocks of the cores (`grep MHz /proc/cpuinfo`) written before and after it. The exact commands, for `go test` and for a cross-compiled test binary, are in the header of `calibrate_test.go`. `internal/calibrate/cal-check.awk` judges the noise of the six (the criterion is in its header): no probe missing or failed, the worst ns/unit of each run within 5% between runs, the probes that decide (at least half of the worst ns/unit) within 10% between the first three runs and the last three, and at least 90% of the other probes within 10% the same way; the load average (1 minute) must be under 2 before and after each run, checked by hand from the `uptime` lines. `TestWriteCalibrationFloors` (`STARLARK_CALIBRATE_INPUT`, `STARLARK_CALIBRATE_WRITE`) writes the floors file of that platform. `internal/calibrate/bench-compare.awk` is the other tool of that directory: it compares the benchmarks of v0.2.3 and v0.3.0 on one host, and its thresholds are a proposal.

**What has been measured.** Two platforms are registered. darwin/arm64: Go 1.26, 2026-10-01, two runs of seven repeats, with a load average of 4.8 to 12.5 at the start and end of the runs (the lines are in the header of `calibrate_floors_test.go`). That is not a quiet machine and it is not the procedure above (two runs, not six; the load criterion not met). A loaded machine only makes the times longer, so C = 12 is on the safe side for that machine, but this is not a measurement of a quiet one. linux/amd64 was measured on a test stand (Intel N200, not a production platform; `calibrate_floors_n200_test.go`): 2026-10-19, Go 1.26.3, the procedure above (six runs, the load 0.46 before the first and 1.2 to 1.9 during them, the clocks 2400 to 3300 MHz under the `powersave` governor of the stand, which was not changed). It did NOT pass the criterion on that hardware: A failed (the dearest ns/unit of the six runs is 31.49, 29.48, 33.84, 31.12, 32.33, 32.17, a spread of 1.148 against 1.05; the dearest probe in every run is `f(**d), 60000 keys`), and B′ failed (7 of the 70 probes that decide differ by more than 10% between the halves, at most 1.297, `s += 'x' x 20000`); C′ passed (91.5%), and no probe was missing. The noise is that of the hardware (the governor, the memory), not of the procedure, and a run again would show the same; the table was accepted as it is, because the stand gates no price (darwin/arm64 does). Its C is 34: the ceiling of the dearest probe of the dearest run (33.84), not of the least times (29.48), so that the time it promises holds in a bad process too. linux/arm64 builds but has NOT been measured.

**The overhead against v0.2.3.** The benchmarks of the two versions on the N200 stand (ten interleaved pairs, `internal/calibrate/bench-compare.awk`): the execution is not slower (the programs of `testdata/benchmark.star` 0.977, the small built-ins and operators 0.983, the interpreter's branches 1.017, geometric means of the ratios). Compiling is: `Program/compile` +2.7% on darwin/arm64 (+14% on the stand), and `Program/decode` within the noise (+13% on darwin under load, a spread of 1.187 on the stand), with +320 B/op in both: the index of the parameters of a function by name (`Funcode.ParamIndex`, built on its first use), which makes a call with many keyword arguments linear instead of quadratic. A platform that is not registered is held to nothing by these tests; a new platform adds its file with its own C, and does not change the C or the table of another.

**K** is the ratio of the limit of work to the limit of steps that a host chooses when it sets both. The work of a program is its steps plus what the operations add, so the work is never below the steps, and with K = 1 the limit of work stops any program that charges anything before its steps are used. The ratio of the programs that are not attacks, measured now (`TestRatios_*`, printed with `-v`; counts, so it does not depend on the load):

| programs | work / steps |
|---|---|
| the seven handlers of the review (filter and totals; report with join and `%`; report with `s += line`; count by sku in a nested dict; json encode, decode, modify; sort by a key; dedupe and update), N = 20, 200, 2000 | 1.59 to 9.32 |
| the 958 programs of 50 steps or more of the differential corpus | p50 1.54, p90 2.43, p99 5.73, max 56.86 |

The highest ratios are the forms that the work limit exists for: a float compared with an integer of 2**500 squared five times (56.9), a tuple that doubles itself ten times and is hashed (27.5), `-1.5 in l` in a loop over a list of 300 (26.9). The recommended K is 12: a handler that is not an attack stays within it (the worst is 9.3), and a form that takes 12 times its steps in work is refused by the work limit long before its steps are used. K = 12 is a recommendation, not a constant of the code: no test asserts it. With the limit of steps S the limit of work is 12 * S units, and the most CPU that a program may then use is C * 12 * S ns (S = 10 000 000 gives 1.44 s).

## The prices

Every entry of Universe and of the method tables of the string, bytes, list, dict and set types, and every operator, has a price (`prices.go`: `universePrices`, `dictPrices`, `listPrices`, `setPrices`, `stringPrices`, `bytesPrices`, `operatorPrices`); `TestEveryBuiltinHasAPrice` fails for a built-in without one and for a price without a built-in, and `TestOperatorPricesAreDeclared` for an operator. A price is one of three kinds:

| kind | what the work depends on | how it is charged |
|---|---|---|
| `o1` | nothing but the type: the same on an operand of size n and of 100n (`TestO1PricesDoNotGrow`) | nothing beyond the steps and the call |
| `work` | the sizes of the operands (lengths), known before the call | computed BEFORE the call and charged by `Call`: a call that does not fit is refused, not run |
| `inside` | the data (it stops at the first match, hashes, compares) | the function charges as it goes, to a meter |

Each call also pays `call` units for the frame, the arguments and the lookup (`baseCall` = 4 for a built-in that has no work of its own to speak of). The full table, with the formula of every price in words and the probes it was measured by, is [prices.md](prices.md); it is generated (`STARLARK_WRITE_PRICES=$PWD/doc/prices.md go test ./starlark -run TestWritePricesDoc -count=1`), not edited.

`testdata/prices.golden` pins the exact work that every priced built-in and method is charged on a fixed set of operands (strings of 1, 200 and 10000 bytes, containers of 1000 elements, an integer of 2**5000, needles of 1 to 64 bytes), one row each, `error` for a call that the operands do not suit and that is charged all the same. A price that is halved, dropped or mixed up with another changes a row (`TestPricesGolden_ExactWork`). A new built-in is a new row. The file is rewritten only by the writer, always with `-run`, with the variable set to the directory of the starlark package whose testdata is written:

```
STARLARK_WRITE_GOLDEN=$PWD/starlark go test ./starlark -run TestPricesGolden
```

## Idiom to size at refusal

The programs that a handler writes, run in a loop until a byte budget of 1 GiB says no (`TestBudgetIdioms`, `STARLARK_IDIOMS=1`; the limit of steps and the limit of work were not set). The counter is cumulative, so an idiom that builds a value by copying it is refused when the copies add up to the budget, not when the value does: `s = s + piece` is quadratic in this count. "Per iteration" is the bytes charged divided by the iterations done. The two json rows are not printed by that test (it leaves them to the package of json); they were run with the same loop, with `lib/json` as `json`, on 2026-10-19.

| idiom | refused at iteration | charged per iteration |
|---|---|---|
| `s += c` (c of 50 bytes) | 6 554 | 163 825 B |
| `s += c` (c of 1 KB) | 1 465 | 732 001 B |
| `s = s + c` (c of 50 bytes) | 6 554 | 163 825 B |
| `l = l + [i]` | 11 578 | 92 728 B |
| `d = dict(d); d[i] = i` | 4 099 | 261 889 B |
| `d = d \| {i: i}` | 4 095 | 262 145 B |
| `s = set(s); s.add(i)` | 4 099 | 261 889 B |
| `','.join(parts)`, 1000 parts of 50 bytes | 21 054 | 50 997 B |
| `l[:]` of 1000 elements | 66 908 | 16 048 B |
| `sorted(l)` of 1000 elements | 66 908 | 16 048 B |
| `list(d.items())` of 100 | 110 740 | 9 696 B |
| `s.split(',')` of 1000 fields | 33 471 | 32 079 B |
| `s.replace('a', 'xy')` on 1 KB | 715 828 | 1 500 B |
| `x = x + 1`, x of 4000 bits | 3 050 401 | 352 B |
| `json.encode(state)`, 100 keys | 147 471 | 7 281 B |
| `json.decode(state)`, 100 keys | 84 506 | 12 706 B |
| `l += [i]` | not refused in 1 000 000 | 80 B |
| `l.append(i)` | not refused in 1 000 000 | 0 B (see Known gaps) |
| `l.extend([i, i])` | not refused in 1 000 000 | 112 B |
| `d[i] = i` | not refused in 1 000 000 | 128 B |
| `d.update({i: i})` | not refused in 1 000 000 | 640 B |
| `'%s!' % c`, c of 1 KB | not refused in 1 000 000 | 1 001 B |
| `str(l)`, 100 ints | not refused in 1 000 000 | 390 B |
| `[x for x in l]`, 1000 elements | not refused in 1 000 000 | 48 B |
| `x = x + 1`, x of 40 bits | not refused in 4 000 000 | 73 B |
| `{}` literal | not refused in 1 000 000 | 512 B |
| `[]` literal | not refused in 1 000 000 | 48 B |
| call of `def f(a, b, c, d, e)` | not refused in 1 000 000 | 0 B |
| call with `*args, **kwargs` | not refused in 1 000 000 | 576 B |

(The per-iteration figure of a row that was not refused is the bytes charged divided by the iterations tried, and includes the setup.) The linear forms are `"".join(parts)`, `l.append(i)` and `l += [i]`.

## Compatibility with v0.2.0

With the limits not set, a program gives in this version what it gave in v0.2.0: the same result, error, output and steps, on the corpora of the differential tests (`diff_*_v020.golden`, about 16 000 programs, written by running the writers in a tree of v0.2.0 at `d34a821`). What differs is a deliberate bound, and each difference is a case in `compatCases()` (`compat_test.go`), a program with what v0.2.0 gave and what this version gives, both checked exactly by `TestCompatibilityDivergencesFromV020` (45 cases of scripts and 10 of a host that calls `Value.String()`). A difference that is not a bound was returned to v0.2.0 and is held by the differential tests: the equality of dicts and sets, and the lookup, insertion and deletion of a tuple key, nested 11 levels or more, are v0.2.0's (`TestDifferentialB5`, which fails on any other result, error, output or step), and so are `json.decode(s, default=d)` for an integer of 4300 digits or less and the set methods with an unhashable element.

Steps differ in a case only where the program stops at a refusal that v0.2.0 did not make.

### An operand named in an error text is cut

v0.2.0 printed the operand whole, megabytes if the key was megabytes. Now the text names it whole up to 256 bytes of its form (`errValueLimit`), as before, and cut above. The cut: a string, a tuple or a literal is its first 256 bytes of form and `...<truncated at size limit>` (a tuple is cut at an element); a name, a keyword or an attribute is its first 256 bytes and `...<N bytes>` with the full length (`%q` of that for a conversion); an integer is `<int of B bits>`, with `...<truncated at size limit>` after it if B is at most `MaxIntBits`. The first operand size that is cut is in the third column (n is the length of the string, the name or the digits; tests in `diff_errtext_test.go`, sizes 1 to 100 000; the test requires at least 500 messages identical to v0.2.0 below the bound and 100 cut ones). Steps are v0.2.0's.

| id | v0.2.0 | v0.3.0 | why |
|---|---|---|---|
| dict key not found, string | `key "aaa...a" not in dict` with the whole key | the key cut, from n = 255 | the text must not be as large as a value |
| dict key not found, tuple | `key ("aaa...", 1) not in dict` whole | cut at an element or at 256 bytes, from n = 252 | same |
| dict key not found, int | `key 99...9 not in dict`, all digits | `<int of B bits>`, from 257 digits | same |
| dict key not found, negative int | the same with `-` | from 256 digits | same |
| duplicate key in a literal | `duplicate key: "aaa..."` whole | cut, from n = 255 | same |
| multiple values for a parameter | `function f got multiple values for parameter "aaa..."` | cut, from n = 255 | same |
| unexpected keyword of a built-in | `sorted: unexpected keyword argument "aaa..."` | cut, from n = 255 | same |
| duplicate keyword argument | `dict: duplicate keyword arg: "aaa..."` | cut, from n = 255 | same |
| no such attribute (source) | `string has no .aaa... field or method` | name cut with `...<N bytes>`, from n = 257 | same |
| no such attribute (getattr) | `getattr: string has no .aaa... field or method` | same, from n = 257 | same |
| no such attribute of a list (source) | `list has no .aaa... field or method` | same, from n = 257 | same |
| key not found in a format string | `key not found: aaa...` | same, from n = 257 | same |
| invalid float literal | `invalid float literal: aaa...` | same, from n = 257 | same |
| invalid int literal | `int: invalid literal with base 10: aaa...` | same, from n = 257 | same |
| format: keyword not found | `format: keyword aaa... not found` | same, from n = 257 | same |
| format: attribute syntax | `format: attribute syntax x.y is not supported in replacement fields: aaa....x` | same, from n = 255 | same |
| format: spec | `format spec features not supported in replacement fields: aaa...` | same, from n = 257 | same |
| format: conversion | `format: unknown conversion "aaa..."` | `%q` of the cut name, from n = 257 | same |
| json.decode: invalid number | `json.decode: at offset N, invalid number: 099...9` | the number cut, from 256 digits | same |
| json.encode: key of a value that fails | `json.encode: in dict key "aaa...": cannot encode non-finite float +inf` | key cut, from n = 257 | same |
| int out of range: chr | `chr: 99...9 out of range` | `<int of B bits>`, from 257 digits | same |
| int out of range: index | `list index: 99...9 out of range` | same | same |
| int out of range: range | `range: for parameter 1: 99...9 out of range (want value in signed 64-bit range)` | same | same |
| int out of range: bytes | `bytes: at index 0, 99...9 out of range (want value in unsigned 8-bit range)` | same | same |
| int out of range: shift | `99...9 out of range` | same | same |
| int out of range: enumerate | `enumerate: for parameter 2: 99...9 out of range (want value in signed 64-bit range)` | same | same |

### Integers of more than 4300 digits are not converted

An integer longer than `MaxIntDigits` is still an integer (it is computed with, compared and hashed); only its conversion to or from a string is refused, with an error. v0.2.0 converted it, in time quadratic in the digits: `int("9" * 1000000)` took 4 s and 12 steps. Steps differ (the program stops at the refusal).

| id | v0.2.0 | v0.3.0 | why |
|---|---|---|---|
| error text: `int(s, 16)` of a string of 5000 hex digits | the integer | error `int: the string has more than 4300 digits: its conversion to an integer is not allowed` | `MaxIntDigits` |
| `str(y)`, y of 12000 digits | the string | error `str: an integer of more than 4300 decimal digits is not converted to a string` | same |
| `repr(y)` | the string | error `repr: ...` the same text | same |
| `'%d' % y` | the string | error `an integer of more than 4300 decimal digits is not converted to a string` | same |
| `'%s' % y` | the string | error `%: ...` | same |
| `'{}'.format(y)` | the string | error `format: ...` | same |
| `str([y])` | the string | error `str: ...` | same |
| `json.encode(y)` | the string | error `json.encode: ...` | same |
| `int(s)`, s of 5000 digits | the integer | error `int: the string has more than 4300 digits: its conversion to an integer is not allowed` | same |
| `json.decode` of 5000 digits | the integer | error `json.decode: number has more than 4300 digits` | same |
| `json.decode` of -5000 digits | the integer | the same error | same |
| `json.decode` of `[9...9, 1]` (5000 digits) | the list | the same error | same |
| `json.decode` of `{"a": 9...9}` (5000 digits) | the dict | the same error | same |
| `json.decode(s, default=7)`, 5000 digits | the integer | the same error: the default is not returned (a refusal is an error of the call, not a syntax error) | same |
| `json.decode(s, default=None)`, 4301 digits | the integer | the same error | same |

### The union of dicts with a deep key (`d|e`)

`d | e` where both dicts have a key that is a tuple nested 10 levels or more. Comparing two such keys is refused (`comparison exceeded maximum recursion depth`, the depth of a comparison is 10 in both versions); v0.2.0 dropped that error, so the union stopped there silently: the result was `d` as it was, the value of the key was not updated, and the keys of `e` after the failing one were not added. This version returns the error. The other uses of the depth of a comparison are the same as in v0.2.0 (see above). Steps differ.

| id | v0.2.0 | v0.3.0 | why |
|---|---|---|---|
| dict union with a tuple key nested 10 deep | `{t: 1}` (d unchanged) | error `comparison exceeded maximum recursion depth` | an error is not dropped |
| ... nested 10 deep, more keys after it (`e = {t: 2, 'z': 3}`) | `{t: 1}`: `z` lost and the value of `t` not updated | the same error | same |
| dict union with a tuple key nested 12 deep | `{t: 1}` | the same error | same |
| ... nested 12 deep, more keys after it | `{t: 1}`: `z` lost | the same error | same |

### The string form of a value in a host (`Value.String()`)

The form of a value is the host's to ask for (`String()`, `fmt`), and it has no error to return. A big integer longer than `MaxIntBits` (14285 bits, 4300 digits) in a container is written as its size, `<int of N bits>`, where v0.2.0 converted it (a conversion quadratic in the digits). The form of this version ends at the integer, and every bracket that was open is closed: everything after the marker is dropped, including the value at the marker key (a dict whose key is the integer is closed without its value, `{<int of 16610 bits>}`). A script that asks for the same through `str`, `repr`, `%` or `format` gets the error of the previous section. The cases use `10**5000` (16610 bits).

| id | v0.2.0 | v0.3.0 | why |
|---|---|---|---|
| list `[n, 1]` | `[1000...0, 1]` | `[<int of 16610 bits>]` (the 1 after the marker is dropped) | `MaxIntBits` |
| list `[1, n]`, the integer last | `[1, 1000...0]` | `[1, <int of 16610 bits>]` | same |
| tuple `(1, n)` | `(1, 1000...0)` | `(1, <int of 16610 bits>)` | same |
| tuple of one `(n,)` | `(1000...0,)` | `(<int of 16610 bits>,)` | same |
| dict `{"k": n}` | `{"k": 1000...0}` | `{"k": <int of 16610 bits>}` | same |
| dict with the integer as key `{n: 1}` | `{1000...0: 1}` | `{<int of 16610 bits>}` (the value at the marker key is dropped) | same |
| set `set([n])` | `set([1000...0])` | `set([<int of 16610 bits>])` | same |
| list in a list `[[1, n, 1], 1]` | `[[1, 1000...0, 1], 1]` | `[[1, <int of 16610 bits>]]` (everything after the marker, to the end, dropped) | same |
| dict in a tuple in a list `[({"a": [n]},), 1]` | `[({"a": [1000...0]},), 1]` | `[({"a": [<int of 16610 bits>]},)]` | same |
| set in a dict `{1: set([n])}` | `{1: set([1000...0])}` | `{1: set([<int of 16610 bits>])}` | same |

### Other differences that are not cases of the list

These are bounds that the tests hold as a bound and not as a difference of an answer: what v0.2.0 did is taken from the comments of the code, not rerun on a tree of v0.2.0.

- **Panics and fatal errors that became errors.** A request for more than 1 GiB in one operation (`list(range(1 << 60))`, `sorted(range(1 << 60))`, `bytes(range(1 << 60))`, and the like) is an error whose text contains `excessive` (the test asserts the substring) before anything is allocated; the code of v0.2.0 passed the size to `make`, a panic for a size out of range and a fatal out of memory for terabytes (`TestAllocCeiling_HugeSizesAreRefusedNotPanics`). A value nested deeper than `MaxValueDepth` (`t = (t,)` in a loop, six steps a level) took 700 bytes of the Go stack a level in the string form, `Hash`, `Freeze` and json, a stack overflow that is fatal; now the string form ends in `...<nested too deeply>` and the others return `value is nested more than 10000 levels deep` (`TestDeepNesting_ChildProcess`, in a child process with a 24 MiB stack). A chain of different functions deeper than the limit of the stack of calls was not bounded without recursion; now it is the error `Starlark stack overflow` (`TestCallStack_*`).
- **Every allocation can be refused.** With `SetMaxAllocBytes` and `SetMaxWork` set, a program that v0.2.0 ran to its step limit can stop earlier, at a refusal of work or bytes. With neither set, the work is counted and the ceilings above apply; nothing else is refused.

## Known gaps

Verified against the code of this tree.

- **`json.decode` allocates up to 6 times what it charges**, because it builds the elements of an array by `append`, which allocates up to five times the array, and charges its slots. Dicts and sets built from tables (`dict(zip(...))`, `set(l)`) allocate up to 2 times. Every other operation of the peak test is held within 1.25 of what it charged (`peak_test.go`, `peakMax`; not run under `-race`).
- **Parsing and compiling are not counted.** `syntax.Parse`, `resolve.File` and the compiler take no thread, so a program's source costs no work, steps or bytes; the host bounds the size of the source it accepts. (The ratio of memory to source bytes was not measured here.)
- **`list.append` and `list.insert` do not charge the 16 byte slot** of the element (the value in it was charged when it was made). It is bounded by the steps: a program that runs S steps and uses no operation that is charged keeps at most `allocUnchargedBytesPerStep` * S = 8 * S bytes beyond what was charged (`alloc.go:152`, `TestAllocUnchargedGrowthPerStep`).
- **`fail` with a large message** returns a large text to the host: `fail('a' * 100000000)` with no byte budget gave an error text of 100 000 006 bytes (measured on 2026-10-19); under a budget it is bounded by it. The host that shows or stores an error text bounds it itself.
- **The unit is a ceiling, not an average**, and C is per platform (see C and K): the only registered platform is darwin/arm64, measured on a loaded machine. A program does not use more than C ns a unit on a platform that is registered, with its own C; the other platforms are held to nothing until they are measured and registered.
- **K = 12 is not asserted** by a test (see C and K).

## Mutation testing

`internal/cmd/mutate` changes one place of the resource accounting at a time (the table of struct places, and the generators of calls and prices in `mutants.go`) and runs the tests of `./starlark` and `./lib/json` against each; a mutant that no test fails is a missing test, or a change that no program can see. It is an internal tool that changes the sources it is pointed at, so it is run only in a detached worktree (its header says how). The mutants that survived and are proven unobservable (for each observable of a run, the result, the error, `Work()`, `AllocatedBytes()` and `Steps`: whether it changes and why) are in [mutants-equivalent.md](mutants-equivalent.md). Of the 557 mutants of this tree, 186 survived the first run; after the tests of properties (`prices.golden`, `TestIh*`, `TestIs*`, `TestMC*`, `TestMU_*`) 181 of them are killed, one (struct-0011, the quadratic cycle check) only times out, and four survive, all of them proven there. The run is a tool of the release, not part of the test suite.
