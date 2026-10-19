# Work and steps of the starlark fork (v0.3.0)


The steps of a program (`Thread.Steps`, `ExecutionSteps()`) are what they were in v0.2.0, byte for byte, whatever the operands: the interpreter counts an opcode, and nothing else is converted to steps. The time of the operations is a second counter, the work (`Thread.Work()`), with its own limit (`SetMaxWork`).

**The unit of work** is about the time of a simple step of the interpreter (the loop probes below take 1.5 to 11.2 ns a unit; C below is the bound that is asserted). Each step is a unit, and the operations charge for what they do beyond the opcodes that start them: the elements they visit, compare, probe, hash or copy, the bytes they scan, and the memory they allocate (garbage is allocator and collector time as well).

**The invariant**: a program that has done W units of work has used at most C * W nanoseconds of CPU, with C = 12 ns on the reference machine, darwin/arm64 (C is per platform: see the platforms below;the interpreter's own loops and the priced operations alike: no probe of the calibration cost more).

**The search family** (find, rfind, index, rindex, partition, rpartition) charges each window of the haystack before it scans it (windows of 64 bytes or the needle, doubling up to a chunk of 1024 units), so a call is refused before the window that does not fit, and a match that is found may be charged up to the rest of its last window. A window is at least 16 needles long (so that the overlap of a match across the join, scanned twice, is at most a sixteenth of the haystack), so for a long needle the refusal is coarser: a needle of 1000 bytes is charged 4000 units ahead of the first window (16000 bytes, a quarter of a unit a byte), and one of 10000 bytes 40000 units, where a short needle is charged at most a chunk of 1024 units.

## What a unit costs, by probe (308 probes)

The time is the least of seven runs, measured by `TestCalibrate` (the date and the load of the machine are in the header of `calibrate_floors_test.go`; doc/budgets.md says what they mean); the work is what the operation is charged now. The ratio is the nanoseconds a unit of work bought in that probe: no probe is over C = 12 ns (`TestWork_NoProbeCostsMoreThanC`) and every priced one is held to it (`TestPrices_AtLeastTheMeasuredCost`). The worst of the operations that are priced:

| probe | ns | work | ns / unit |
|---|---|---|---|
| any(l) all falsy | 11717834 | 1000011 | 11.7 |
| all(l) all truthy | 11393667 | 1000011 | 11.4 |
| l.append x 500000 (growth) | 82835334 | 7500018 | 11.0 |
| sorted: strings equal prefix | 24647417 | 2288732 | 10.8 |
| int % small (4000 words) x 3000 | 64386625 | 6030013 | 10.7 |
| max(l, key=f) | 9139875 | 900021 | 10.2 |
| sorted: strings | 43374000 | 4323458 | 10.0 |
| sorted: 2 elements x 200000 calls | 26969625 | 2700013 | 10.0 |
| startswith/endswith | 778250 | 78013 | 10.0 |
| d.update(kw) x 50000 | 10953375 | 1100013 | 10.0 |
| join: 500000 strings | 5208792 | 523449 | 10.0 |
| %s loop with a string | 19871375 | 2000013 | 9.9 |
| abs/min/max of 2 x 300000 | 139596541 | 14100013 | 9.9 |
| find: needle 63 | 1231709 | 125016 | 9.9 |
| find: needle 48 | 1225375 | 125016 | 9.8 |

All of them:

| probe | ns | work | steps | ns / unit |
|---|---|---|---|---|
| loop: for i in range(N): pass | 17931875 | 12000013 | 12000009 | 1.49 |
| loop: x = len(l) | 88694708 | 14000013 | 10000009 | 6.34 |
| loop: def call | 76581292 | 12000013 | 12000009 | 6.38 |
| loop: l.append(i) | 167632959 | 15000013 | 11000009 | 11.18 |
| loop: d[i] = i | 170735917 | 28610844 | 6000009 | 5.97 |
| find: 100-byte needle, all a | 1274125 | 250018 | 12 | 5.10 |
| find: 10000-byte needle, all a | 1282750 | 250224 | 12 | 5.13 |
| find: 1-byte needle, miss | 10583 | 7824 | 8 | 1.35 |
| find: 2-byte needle, ab... | 254833 | 62512 | 8 | 4.08 |
| rfind: 100-byte needle | 1274541 | 250018 | 12 | 5.10 |
| count: 100-byte needle | 1274083 | 254968 | 12 | 5.00 |
| count: 1-byte needle | 16208 | 507824 | 8 | 0.03 |
| count: aa | 1905916 | 312512 | 8 | 6.10 |
| in: 100-byte needle | 1273875 | 250013 | 11 | 5.10 |
| replace: 100-byte miss | 1274042 | 250019 | 13 | 5.10 |
| replace: a -> bb (n matches) | 8534459 | 2132825 | 9 | 4.00 |
| replace: a -> b | 8495375 | 2070325 | 9 | 4.10 |
| partition: 100-byte miss | 1274209 | 250018 | 12 | 5.10 |
| rpartition: 100-byte miss | 1273459 | 250018 | 12 | 5.09 |
| startswith/endswith | 778250 | 78013 | 20009 | 9.98 |
| split: , (500000 fields) | 7395417 | 3015645 | 8 | 2.45 |
| split: whitespace | 9154542 | 3250014 | 7 | 2.82 |
| split: , 100-byte sep miss | 2547750 | 500027 | 12 | 5.10 |
| rsplit: , | 7413667 | 3015645 | 8 | 2.46 |
| splitlines | 7163667 | 3015645 | 7 | 2.38 |
| join: 500000 strings | 5208792 | 523449 | 8 | 9.95 |
| strip: 5000 non-ascii cutset | 256416 | 250203 | 119 | 1.02 |
| lstrip: A4 form | 624250 | 300012 | 8 | 2.08 |
| strip: ascii cutset | 1630958 | 500012 | 7 | 3.26 |
| upper (ascii) | 2167792 | 760427 | 7 | 2.85 |
| upper (non-ascii) | 4067500 | 760427 | 7 | 5.35 |
| lower (non-ascii) | 5082042 | 760427 | 7 | 6.68 |
| title | 2594167 | 684386 | 7 | 3.79 |
| capitalize | 2620333 | 684386 | 7 | 3.83 |
| isalpha/isdigit/isspace | 3692625 | 1000035 | 19 | 3.69 |
| s + s | 77292 | 20840 | 7 | 3.71 |
| s * 8 | 9625 | 8340 | 7 | 1.15 |
| s[::-1] | 1797250 | 260426 | 10 | 6.90 |
| s[::2] | 891959 | 130217 | 9 | 6.85 |
| s == s | 1709042 | 1563513 | 1009 | 1.09 |
| s < s | 2391375 | 1563513 | 1009 | 1.53 |
| hash(s) | 763916 | 250011 | 7 | 3.06 |
| dict key: 1 MB string | 2701583 | 609702 | 251 | 4.43 |
| str(s) | 209 | 11 | 7 | 19.00 |
| repr(s): plain | 1604500 | 760427 | 7 | 2.11 |
| repr(s): escapes | 6632459 | 765636 | 7 | 8.66 |
| repr(list of str) | 5618750 | 1114594 | 7 | 5.04 |
| %s % s | 17875 | 10423 | 7 | 1.71 |
| % d | 9463458 | 1000013 | 1000009 | 9.46 |
| '{}'.format(s) | 19334 | 10434 | 8 | 1.85 |
| format of 100000 fields | 6056167 | 714597 | 10 | 8.47 |
| list(s.elems()) | 4275000 | 900020 | 9 | 4.75 |
| list(s.codepoints()) | 17300667 | 2400020 | 9 | 7.21 |
| bytes(s) | 21792 | 31261 | 7 | 0.70 |
| list(b.elems()) | 15712375 | 2400020 | 9 | 6.55 |
| str(b) | 21833 | 15636 | 7 | 1.40 |
| x in l (ints, miss) | 1797792 | 500008 | 8 | 3.60 |
| x in l (strings, miss) | 381292 | 200007 | 7 | 1.91 |
| x in l (mixed int/float) | 2606167 | 300008 | 8 | 8.69 |
| x in l (big ints) | 1826334 | 900022 | 9 | 2.03 |
| l.index(miss) / remove | 1389666 | 300012 | 8 | 4.63 |
| max(l) | 9858125 | 1500012 | 7 | 6.57 |
| min(l) mixed | 6751792 | 1500012 | 7 | 4.50 |
| sorted: random ints | 58473583 | 9050775 | 7 | 6.46 |
| sorted: ascending | 4852375 | 660397 | 7 | 7.35 |
| sorted: descending | 34680583 | 5995269 | 7 | 5.78 |
| sorted: equal | 4884625 | 660397 | 7 | 7.40 |
| sorted: strings | 43374000 | 4323458 | 7 | 10.03 |
| sorted: mixed int/float | 40322709 | 6322273 | 7 | 6.38 |
| sorted: tuples | 60401042 | 8900475 | 7 | 6.79 |
| sorted: key=lambda | 43616708 | 5968877 | 300010 | 7.31 |
| list(l) | 2400167 | 2000014 | 7 | 1.20 |
| tuple(l) | 7479834 | 2000013 | 7 | 3.74 |
| reversed(l) | 2685250 | 3000014 | 7 | 0.90 |
| enumerate(l) | 27124375 | 4000014 | 7 | 6.78 |
| zip(l, l) | 25690333 | 4500015 | 8 | 5.71 |
| l * 8 | 4450458 | 800010 | 7 | 5.56 |
| l + l | 8289292 | 1000010 | 7 | 8.29 |
| l[::2] | 389000 | 2500012 | 9 | 0.16 |
| l[::-1] | 752084 | 5000013 | 10 | 0.15 |
| l.extend(l) | 8034125 | 1500012 | 8 | 5.36 |
| l += l | 8022459 | 1500007 | 7 | 5.35 |
| l.insert(0) x 1000, 1M slots | 275485875 | 252140513 | 12009 | 1.09 |
| l.pop(0) x 1000, 1M slots | 239840125 | 249889513 | 11009 | 0.96 |
| l.clear() | 146125 | 500011 | 7 | 0.29 |
| l == m (ints) | 4081541 | 1000007 | 7 | 4.08 |
| l < m (ints) | 4094959 | 1000007 | 7 | 4.09 |
| l == m (mixed int/float) | 2869792 | 300007 | 7 | 9.57 |
| str(l) ints | 10171083 | 1223853 | 7 | 8.31 |
| str(l) empty lists depth 40 | 42871292 | 22021333 | 7 | 1.95 |
| x < 1.5, big int 32 KB | 25500 | 10013 | 10009 | 2.55 |
| d[k] = v, 500000 | 176584500 | 28610878 | 6000011 | 6.17 |
| set(l) | 157281583 | 22610874 | 7 | 6.96 |
| dict(d) | 75681458 | 12558905 | 7 | 6.03 |
| d == e | 32460750 | 9000007 | 7 | 3.61 |
| d \| e | 147930166 | 26661744 | 7 | 5.55 |
| d.update(e) same keys | 36440041 | 11400012 | 8 | 3.20 |
| d.keys()/items()/values() | 51169792 | 6900036 | 15 | 7.42 |
| d.clear() after growth, x1000000 | 25505542 | 6600013 | 4800009 | 3.86 |
| s \| t | 177018000 | 27513718 | 7 | 6.43 |
| s & t | 44999458 | 11381492 | 7 | 3.95 |
| s - t | 89987292 | 17734615 | 7 | 5.07 |
| s ^ t | 92520458 | 24261751 | 7 | 3.81 |
| s == t | 31075167 | 6000007 | 7 | 5.18 |
| s <= t | 33077459 | 5765543 | 7 | 5.74 |
| collisions: i << 16 | 212801375 | 25797083 | 280011 | 8.25 |
| collisions: i << 40 | 4788542 | 927280 | 280011 | 5.16 |
| d[k] with 30 char string keys | 32276750 | 10195478 | 2400009 | 3.17 |
| x in d, strings | 7160167 | 2905560 | 1400009 | 2.46 |
| f() with 2000 params x 2000 | 13554208 | 8024013 | 20009 | 1.69 |
| f(**d), 60000 keys | 7611666 | 959985 | 9 | 7.93 |
| f(**d) onto 2000 params | 419042 | 100078 | 64 | 4.19 |
| '{k}'.format(**d) | 7501917 | 1360951 | 10 | 5.51 |
| f(*l), 500000 | 6530625 | 1000011 | 9 | 6.53 |
| chain of 20000 functions | 9964625 | 2456285 | 60005 | 4.06 |
| big + 1 (6000 words) | 25695709 | 20080013 | 50009 | 1.28 |
| big * big (1500 words) | 21625 | 200412 | 7 | 0.11 |
| big // y (6000 / 1500 words) | 24125 | 2002512 | 7 | 0.01 |
| big << 511 | 27337417 | 20080013 | 50009 | 1.36 |
| -big | 26629292 | 20075013 | 45009 | 1.33 |
| str(int 4300 digits) x20 | 993792 | 159433 | 209 | 6.23 |
| int(str 4300 digits) x20 | 575958 | 176613 | 209 | 3.26 |
| hash of 6000-word int | 2494916 | 2023513 | 6009 | 1.23 |
| find: needle 2 | 245583 | 62512 | 8 | 3.93 |
| find: needle 4 | 240583 | 62512 | 8 | 3.85 |
| find: needle 8 | 241209 | 62516 | 12 | 3.86 |
| find: needle 16 | 368958 | 62516 | 12 | 5.90 |
| find: needle 32 | 616625 | 125016 | 12 | 4.93 |
| find: needle 48 | 1225375 | 125016 | 12 | 9.80 |
| find: needle 63 | 1231709 | 125016 | 12 | 9.85 |
| find: needle 64 | 1226958 | 250016 | 12 | 4.91 |
| find: needle 65 | 1222583 | 250016 | 12 | 4.89 |
| find: needle 1000 | 1225959 | 250036 | 12 | 4.90 |
| find: needle 100, text ab | 1224167 | 250018 | 12 | 4.90 |
| find: needle 100, text abc.. | 345500 | 260016 | 12 | 1.33 |
| find: needle 1 hit at the end | 10541 | 7824 | 8 | 1.35 |
| find: 3 bytes in text with many near matches | 508500 | 62512 | 8 | 8.13 |
| count: needle 2, matches everywhere | 1836208 | 312512 | 8 | 5.88 |
| count: needle 3 overlapping text | 1191542 | 229178 | 8 | 5.20 |
| replace: needle 2 matches everywhere | 7025291 | 1093763 | 9 | 6.42 |
| split: needle 2 matches everywhere | 6521250 | 3125021 | 8 | 2.09 |
| split: needle 1, no match | 26000 | 15645 | 8 | 1.66 |
| rsplit: needle 100 miss | 2517042 | 500027 | 12 | 5.03 |
| splitlines: no newline | 25958 | 15645 | 7 | 1.66 |
| startswith 1 MB | 1710416 | 1564013 | 1109 | 1.09 |
| s in s (long needle hit at the end) | 1258625 | 250013 | 11 | 5.03 |
| title: non-ascii | 7560917 | 912511 | 7 | 8.29 |
| isalpha: non-ascii | 942500 | 250011 | 7 | 3.77 |
| lstrip: 5000-char cutset, 200000 trimmed | 325000 | 115016 | 8 | 2.83 |
| lstrip: ascii cutset of 100 | 1556500 | 500113 | 8 | 3.11 |
| s[::3] | 592167 | 86814 | 9 | 6.82 |
| s[-1::-1] | 1774083 | 260427 | 11 | 6.81 |
| s[1000:900000] (shares memory) | 418750 | 120013 | 120009 | 3.49 |
| s * 1 and s + '' | 613250 | 104300013 | 140009 | 0.01 |
| %d loop | 18863833 | 2000013 | 2000009 | 9.43 |
| %s loop with a string | 19871375 | 2000013 | 2000009 | 9.94 |
| str(i) loop | 19024541 | 2800013 | 2000009 | 6.79 |
| repr(s) plain 1 MB | 1555125 | 760427 | 7 | 2.05 |
| repr(s) with escapes 1 MB | 6446209 | 765636 | 7 | 8.42 |
| repr(s) non-ascii 1 MB | 7207917 | 760427 | 7 | 9.48 |
| repr of 100000 short strings | 3339417 | 607302 | 7 | 5.50 |
| str of 300000 floats | 19579583 | 2430489 | 7 | 8.06 |
| str of nested dicts | 10552416 | 1214362 | 7 | 8.69 |
| str of tuples (depth 2) | 11450708 | 2025705 | 7 | 5.65 |
| str: DAG, shared lists | 20644208 | 3222927 | 7 | 6.41 |
| print of a long list | 10307875 | 1223853 | 7 | 8.42 |
| fail of a long list | 125 | 10 | 10 | 12.50 |
| fail(a long string) | 83 | 5 | 5 | 16.60 |
| '{}'.format of many ints | 6583833 | 709273 | 10 | 9.28 |
| '%s' % of a list | 10183084 | 1223853 | 8 | 8.32 |
| bytes(list) | 6087750 | 1010427 | 7 | 6.02 |
| bytes(str) 1 MB then elems | 3982000 | 1700027 | 1700023 | 2.34 |
| for c in s.elems() (300000) | 14143208 | 3000015 | 3000011 | 4.71 |
| for c in s.codepoints() (300000) | 14705250 | 3000015 | 3000011 | 4.90 |
| for c in s.elem_ords() (300000) | 7562750 | 3000015 | 3000011 | 2.52 |
| chr/ord loop | 37492000 | 4800013 | 3200009 | 7.81 |
| x in l: strings equal length, miss | 955625 | 300007 | 7 | 3.19 |
| x in l: strings 1 KB, equal prefix | 4373833 | 3203413 | 1409 | 1.37 |
| x in l: tuples | 2079208 | 600015 | 11 | 3.47 |
| x in l: lists | 2065208 | 400013 | 9 | 5.16 |
| x in l: floats | 2630709 | 600008 | 8 | 4.38 |
| x in l: mixed float key in int list | 2565167 | 300007 | 7 | 8.55 |
| x in l: None/bool | 1890917 | 300007 | 7 | 6.30 |
| x in tuple | 1796792 | 500008 | 8 | 3.59 |
| max(l) floats | 2874583 | 1200011 | 7 | 2.40 |
| max(l) strings | 1843417 | 600012 | 7 | 3.07 |
| max(l, key=f) | 9139875 | 900021 | 300010 | 10.16 |
| min(l) ints | 6535667 | 1500012 | 7 | 4.36 |
| any(l) all falsy | 11717834 | 1000011 | 7 | 11.72 |
| all(l) all truthy | 11393667 | 1000011 | 7 | 11.39 |
| any(l) of strings | 9237375 | 1000011 | 7 | 9.24 |
| sorted: 2 elements x 200000 calls | 26969625 | 2700013 | 1200009 | 9.99 |
| sorted: sorted by key tuples | 60690583 | 7879502 | 1000010 | 7.70 |
| sorted: strings equal prefix | 24647417 | 2288732 | 7 | 10.77 |
| sorted: lists | 29167709 | 4193923 | 7 | 6.95 |
| sorted(reverse=True) | 80740083 | 11464956 | 9 | 7.04 |
| sorted: organ pipe | 26599083 | 3017979 | 7 | 8.81 |
| sorted: many equal | 21527042 | 3693615 | 7 | 5.83 |
| list(range) 1M | 15114917 | 2000020 | 9 | 7.56 |
| list comprehension 500000 | 30884666 | 4500018 | 4500011 | 6.86 |
| list(str.elems()) 1M | 19141250 | 3000020 | 9 | 6.38 |
| tuple(range) | 14835583 | 2000019 | 9 | 7.42 |
| l[i] reads | 14808375 | 4200015 | 4200011 | 3.53 |
| l[a:b] copies | 493875 | 899012 | 9 | 0.55 |
| l[::2] x big | 746917 | 714302 | 9 | 1.05 |
| l.index hit | 2293667 | 500012 | 8 | 4.59 |
| l.remove hit last | 2389458 | 500012 | 8 | 4.78 |
| l.insert(0) 1M | 13160750 | 2250013 | 9 | 5.85 |
| l.pop() x 500000 | 52016667 | 7000013 | 5000009 | 7.43 |
| l.append x 500000 (growth) | 82835334 | 7500018 | 5500011 | 11.04 |
| l += [i] x 200000 | 24186334 | 4290019 | 2200011 | 5.64 |
| l = l + [i] x 4000 | 24244792 | 8074018 | 44011 | 3.00 |
| s += 'x' x 20000 (quadratic) | 185247625 | 21024599 | 200011 | 8.81 |
| tuple compare 2 x 1M | 2024708 | 500007 | 7 | 4.05 |
| list of lists == | 1528792 | 300007 | 7 | 5.10 |
| list of strings == | 762959 | 300007 | 7 | 2.54 |
| list of floats == | 4634709 | 1000007 | 7 | 4.63 |
| list < list (mixed int/float) | 2849375 | 300007 | 7 | 9.50 |
| dict == dict (str keys) | 4225917 | 2605587 | 7 | 1.62 |
| dict == dict (nested lists values) | 5535541 | 2800007 | 7 | 1.98 |
| set == set (strings) | 3148708 | 1605587 | 7 | 1.96 |
| nested == wide | 156125 | 33340 | 7 | 4.68 |
| d[k] hits, int keys | 18690083 | 6606798 | 2400011 | 2.83 |
| d.get misses, int keys | 32625667 | 8400015 | 4000011 | 3.88 |
| x in d hits, string keys | 7264709 | 2905589 | 1400009 | 2.50 |
| d[k] = v new keys, 2M entries (cold) | 547187542 | 67615552 | 14000011 | 8.09 |
| d.pop(k) 200000 | 29107916 | 5877789 | 2200009 | 4.95 |
| d.popitem 200000 | 22428042 | 5677789 | 2000009 | 3.95 |
| d.setdefault 200000 | 59263500 | 13859259 | 2400011 | 4.28 |
| for k in d / d.items() 300000 | 69490041 | 7706801 | 3600011 | 9.02 |
| dict literal 3 entries x 100000 | 17751583 | 5800013 | 2000009 | 3.06 |
| dict(zip(l, l)) | 130120500 | 15258915 | 10 | 8.53 |
| dict(list of pairs) | 106490833 | 12558905 | 7 | 8.48 |
| d.update(pairs) | 109890167 | 12558874 | 8 | 8.75 |
| d.update(kw) x 50000 | 10953375 | 1100013 | 700009 | 9.96 |
| d.keys() then list | 8578708 | 1200014 | 7 | 7.15 |
| set.add 300000 | 100880417 | 21712385 | 3300012 | 4.65 |
| set(range) | 165458875 | 22610880 | 9 | 7.32 |
| s.issubset(l) | 18914042 | 5765548 | 8 | 3.28 |
| s.issuperset(l) | 15697125 | 5700012 | 8 | 2.75 |
| s.intersection(l) | 42433708 | 11381497 | 8 | 3.73 |
| s.difference(l) | 92695708 | 17734620 | 8 | 5.23 |
| s.update(l) | 65175416 | 12558874 | 8 | 5.19 |
| s.pop() x 100000 | 8182792 | 2639931 | 1000009 | 3.10 |
| s == t, s <= t strings | 4432500 | 1543571 | 7 | 2.87 |
| tuple keys (2 ints) 200000 | 66750500 | 13022076 | 3200011 | 5.13 |
| tuple keys (2 strings) 100000 | 15212583 | 4922973 | 1200009 | 3.09 |
| float keys 200000 | 39574792 | 10581500 | 2800011 | 3.74 |
| big int keys (200 bits) 100000 | 26421708 | 6199682 | 1400011 | 4.26 |
| collisions: ints i << 32 | 5661791 | 927241 | 280011 | 6.11 |
| collisions: floats 2^k | 8013875 | 1027281 | 320011 | 7.80 |
| collisions: strings of 11 bytes | 3003375 | 767479 | 240009 | 3.91 |
| def with 3 params x 300000 | 26593584 | 4200013 | 4200009 | 6.33 |
| def with kwargs call x 200000 | 26917334 | 3400013 | 2800009 | 7.92 |
| def with *args **kwargs x 100000 | 26248708 | 5400013 | 1500009 | 4.86 |
| lambda call x 300000 | 29380083 | 4200013 | 4200009 | 7.00 |
| closure x 200000 | 47985083 | 5800013 | 4000009 | 8.27 |
| def with 100 locals x 20000 | 17862084 | 8260013 | 4220009 | 2.16 |
| def with 1000 params x 2000 | 6701291 | 4024013 | 20009 | 1.67 |
| f(**d) onto 20 params, 20 keys x 20000 | 12095625 | 3440013 | 240009 | 3.52 |
| f(*l) 20 x 50000 | 19005000 | 2700013 | 600009 | 7.04 |
| struct-like: dict of dicts lookups | 19452417 | 5100013 | 4200009 | 3.81 |
| hasattr/getattr x 200000 | 59534791 | 8000013 | 3200009 | 7.44 |
| type/len/bool x 300000 | 72439000 | 9000013 | 5400009 | 8.05 |
| range iteration 1M | 114055208 | 15606798 | 10000011 | 7.31 |
| range slice/index/in | 38387500 | 5200013 | 4400009 | 7.38 |
| list(range) of len 3 x 100000 | 26893458 | 2900013 | 1200009 | 9.27 |
| enumerate loop 300000 | 62020417 | 7406801 | 3600011 | 8.37 |
| zip loop 300000 | 64749916 | 7706802 | 3600012 | 8.40 |
| reversed loop 300000 | 44824250 | 5656547 | 3000011 | 7.92 |
| while loop 1M | 29293583 | 9000012 | 9000012 | 3.25 |
| comprehension with condition 500000 | 39317250 | 7500018 | 7500011 | 5.24 |
| dict comprehension 200000 | 35281500 | 9781500 | 2000011 | 3.61 |
| string concat in comprehension join | 20336166 | 2412519 | 2200012 | 8.43 |
| float arithmetic loop 1M | 57461709 | 12000015 | 12000011 | 4.79 |
| int arithmetic loop 1M | 54033209 | 14000015 | 14000011 | 3.86 |
| int ops above 2^31 loop 1M | 111062375 | 12000022 | 6000013 | 9.26 |
| abs/min/max of 2 x 300000 | 139596541 | 14100013 | 6300009 | 9.90 |
| int + int (6000-word) x 5000 | 30789916 | 20080013 | 50009 | 1.53 |
| int * small (4000 words) x 3000 | 18836375 | 2445013 | 30009 | 7.70 |
| int * int (1000 x 1000 words) | 17833 | 200412 | 7 | 0.09 |
| int * int (4000 x 4000 words) | 78250 | 3201612 | 7 | 0.02 |
| int // int (4000 / 1000 words) | 25000 | 2002512 | 7 | 0.01 |
| int % small (4000 words) x 3000 | 64386625 | 6030013 | 30009 | 10.68 |
| int & \| ^ (4000 words) x 2000 | 31881875 | 24062013 | 36009 | 1.32 |
| int >> 100, << 100 (4000 words) x 3000 | 33773166 | 24078013 | 42009 | 1.40 |
| -int, abs(int), ~int (4000 words) x 2000 | 44981917 | 32090013 | 34009 | 1.40 |
| int == int, < (4000 words) x 5000 | 10276083 | 40084023 | 70013 | 0.26 |
| int < float (4000 words) x 100000 | 2552542 | 1000013 | 1000009 | 2.55 |
| str(int) 4300 digits x 50 | 2523667 | 398763 | 509 | 6.33 |
| int(str) 4300 digits x 50 | 1505959 | 441663 | 509 | 3.41 |
| int(str, 16) 4000 digits x 200 | 3415000 | 1302013 | 2209 | 2.62 |
| '%x' % int (4000 bits) x 2000 | 1491541 | 166013 | 20009 | 8.98 |
| float(int) / int(float) x 200000 | 43667291 | 5400008 | 2800009 | 8.09 |
| hash of int keys above 2^31 | 78483667 | 13395299 | 3200011 | 5.86 |
| json.encode(list of int) | 12603417 | 2815985 | 8 | 4.48 |
| json.encode(list of str) | 15254709 | 2912512 | 8 | 5.24 |
| json.encode(dict) | 60669125 | 7872750 | 8 | 7.71 |
| json.encode(nested lists) | 12884375 | 2881958 | 8 | 4.47 |
| json.encode(non-ascii str) | 4546250 | 844699 | 8 | 5.38 |
| json.decode(list of int) | 13167291 | 2100017 | 8 | 6.27 |
| json.decode(list of str) | 14144583 | 1800017 | 8 | 7.86 |
| json.decode(empty lists) | 17437125 | 3000027 | 8 | 5.81 |
| json.decode(empty objects) | 27334166 | 11700056 | 8 | 2.34 |
| json.decode(dicts) | 17514916 | 5300017 | 8 | 3.30 |
| json.indent(flat) | 9274791 | 1243063 | 8 | 7.46 |
| json.indent(nested) | 141719416 | 19045008 | 8 | 7.44 |
| hash of a DAG tuple (2^22 paths) | 58648166 | 12582953 | 9 | 4.66 |

## The platforms

The units of work are the same on every platform; C turns them into time on a given machine, and each platform has its own (a platform is judged by its own floors with its own C, never by a mixture of them). The table above is that of the reference platform, darwin/arm64. Per platform, the worst probe of 2000 ns or more by the work that is charged now:

| platform | machine | date | C (ns / unit) | worst probe | ns / unit |
|---|---|---|---|---|---|
| darwin/arm64 | darwin/arm64, Go 1.26 | 2026-10-01 | 12 | any(l) all falsy | 11.7 |
| linux/amd64 | linux/amd64, go1.26.3, Intel(R) N200 | 2026-10-19 | 34 | f(**d), 60000 keys | 29.5 |

## The prices

### Universe

| name | charged | the call | work (units) |
|---|---|---|---|
| `abs` | before the call | 4 | words of a big integer |
| `all` | as it goes | 4 | one unit per element visited, until the first false one |
| `any` | as it goes | 4 | one unit per element visited, until the first true one |
| `bool` | constant | 4 | a type conversion |
| `bytes` | before the call | 4 | bytes(str): len/64; bytes(iterable): one unit per element |
| `chr` | constant | 4 | one code point |
| `dict` | as it goes | 4 | one insert (8 units + hash + chain) per entry |
| `dir` | as it goes | 12 | 12 + n * (3 + bit length of n) for n names: the names are copied, boxed and sorted (charged by dir once it has them) |
| `enumerate` | before the call | 4 | three units per element (a tuple for each) |
| `fail` | before the call | 4 | len of the message/64 |
| `float` | before the call | 4 | float(str): len/4 |
| `getattr` | constant | 12 | a lookup of an attribute by name, and the bound method |
| `hasattr` | constant | 12 | a lookup of an attribute by name |
| `hash` | before the call | 4 | len/4 (FNV, byte by byte) |
| `int` | as it goes | 4 | int(str): digits + digits^2/4096 (and at most maxIntDigits digits); other: o1 |
| `len` | constant | 4 | the length is stored |
| `list` | before the call | 4 | one unit per element |
| `max` | as it goes | 8 | one comparison (a unit and the leaves) per element |
| `min` | as it goes | 8 | one comparison (a unit and the leaves) per element |
| `ord` | constant | 4 | one code point |
| `print` | before the call | 4 | len of the output/64 |
| `range` | constant | 4 | a range is lazy |
| `repr` | as it goes | 4 | len of the result/4 |
| `reversed` | before the call | 4 | two units per element |
| `set` | as it goes | 4 | one insert (8 units + hash + chain) per element |
| `sorted` | as it goes | 4 | a unit for each comparison and each two swaps that the stable sort (sortstable.go) makes, and what the comparisons cost: ~1.4 n log2 n comparisons and ~4 n log2 n swaps on random data |
| `str` | as it goes | 4 | len of the result/4 |
| `tuple` | before the call | 4 | one unit per element |
| `type` | constant | 4 | the name of a type |
| `zip` | before the call | 4 | two units per element of each column |

### dict

| name | charged | the call | work (units) |
|---|---|---|---|
| `clear` | constant | 4 | the table is replaced by that of a new dict (not zeroed) |
| `get` | as it goes | 4 | hash of the key, chain of the bucket |
| `items` | before the call | 4 | 4 units per entry |
| `keys` | before the call | 4 | 4 units per entry |
| `pop` | as it goes | 4 | hash of the key, chain of the bucket |
| `popitem` | as it goes | 4 | hash of the key, chain of the bucket |
| `setdefault` | as it goes | 4 | hash of the key, chain of the bucket, an insert |
| `update` | as it goes | 4 | one insert (8 units + hash + chain) per entry |
| `values` | before the call | 4 | 4 units per entry |

### list

| name | charged | the call | work (units) |
|---|---|---|---|
| `append` | constant | 4 | amortized: the growth of the array is charged as memory |
| `clear` | before the call | 4 | one unit per 2 slots (zeroed) |
| `extend` | as it goes | 4 | one unit per element added, and per element there if the slice grows (listExtend) |
| `index` | as it goes | 4 | one comparison per element, until the first match |
| `insert` | before the call | 4 | slots after the index/4 (memmove) |
| `pop` | before the call | 4 | slots after the index/4 (memmove) |
| `remove` | as it goes | 4 | one comparison per element until the first match, then slots after it/4 |

### set

| name | charged | the call | work (units) |
|---|---|---|---|
| `add` | as it goes | 4 | hash of the element, chain of the bucket, an insert |
| `clear` | constant | 4 | the table is replaced by that of a new set (not zeroed) |
| `difference` | as it goes | 4 | a copy of the set and a delete per element of the argument |
| `discard` | as it goes | 4 | hash of the element, chain of the bucket |
| `intersection` | as it goes | 4 | a lookup per element of the argument |
| `issubset` | as it goes | 4 | a lookup per element of the argument, and the buckets |
| `issuperset` | as it goes | 4 | a lookup per element of the argument |
| `pop` | as it goes | 4 | the first element and its delete |
| `remove` | as it goes | 4 | hash of the element, chain of the bucket |
| `symmetric_difference` | as it goes | 4 | a copy of the set and a delete or insert per element of the argument |
| `union` | as it goes | 4 | a copy of the set and an insert per element of the arguments |
| `update` | as it goes | 4 | an insert per element of the arguments |

### string

| name | charged | the call | work (units) |
|---|---|---|---|
| `capitalize` | before the call | 4 | 3/4 of len (rune by rune) |
| `codepoint_ords` | constant | 4 | lazy: the iterator is the work |
| `codepoints` | constant | 4 | lazy: the iterator is the work |
| `count` | before the call | 4 | the search of the needle in len bytes (workSearch), and half a unit for each of the len/needle matches |
| `elem_ords` | constant | 4 | lazy: the iterator is the work |
| `elems` | constant | 4 | lazy: the iterator is the work |
| `endswith` | before the call | 4 | the sum over the suffixes of min(len(suffix), len(s))/64 (s: the string that start and end leave), and a unit for each suffix of a tuple |
| `find` | as it goes | 4 | position of the match/64 |
| `format` | as it goes | 4 | len of the result/4 |
| `index` | as it goes | 4 | position of the match/64 |
| `isalnum` | before the call | 4 | len/4 |
| `isalpha` | before the call | 4 | len/4 |
| `isdigit` | before the call | 4 | len/4 |
| `islower` | before the call | 4 | len/4 |
| `isspace` | before the call | 4 | len/4 |
| `istitle` | before the call | 4 | len/4 |
| `isupper` | before the call | 4 | len/4 |
| `join` | as it goes | 4 | total bytes/64 and one unit per element |
| `lower` | before the call | 4 | 3/4 of len (rune by rune) |
| `lstrip` | as it goes | 4 | bytes trimmed/4 |
| `partition` | as it goes | 4 | position of the match/64 |
| `removeprefix` | before the call | 4 | bytes of the prefix/64 |
| `removesuffix` | before the call | 4 | bytes of the suffix/64 |
| `replace` | as it goes | 4 | len/64, and 2 units per match |
| `rfind` | as it goes | 4 | distance of the match from the end/64 |
| `rindex` | as it goes | 4 | distance of the match from the end/64 |
| `rpartition` | as it goes | 4 | distance of the match from the end/64 |
| `rsplit` | as it goes | 4 | len/64, and 4 units per field |
| `rstrip` | as it goes | 4 | bytes trimmed/4 |
| `split` | as it goes | 4 | len/64, and 4 units per field |
| `splitlines` | as it goes | 4 | len/64, and 4 units per line |
| `startswith` | before the call | 4 | the sum over the prefixes of min(len(prefix), len(s))/64 (s: the string that start and end leave), and a unit for each prefix of a tuple |
| `strip` | as it goes | 4 | bytes trimmed/4 |
| `title` | before the call | 4 | 3/4 of len (rune by rune) |
| `upper` | before the call | 4 | 3/4 of len (rune by rune) |

### bytes

| name | charged | the call | work (units) |
|---|---|---|---|
| `elems` | constant | 4 | lazy: the iterator is the work |

### operators

| name | charged | the call | work (units) |
|---|---|---|---|
| `-x, +x, ~x, not x` | constant | 4 | big integers: words |
| `a, b = l (unpacking)` | constant | 4 | the count of targets is in the code |
| `d1 | d2, d1 |= d2` | as it goes | 4 | an insert per entry |
| `def f(*args, **kwargs) (parameters)` | as it goes | 4 | memory of the copy: 1 unit per 32 bytes (above 1 KiB) |
| `f(*l), f(**d)` | as it goes | 4 | memory of the copy: 1 unit per 32 bytes (above 1 KiB) |
| `for x in l (iteration)` | constant | 4 | one step per element |
| `freeze` | as it goes | 4 | one visit per container, a tuple once |
| `hash of a string` | as it goes | 4 | len/64 |
| `hash of a tuple` | as it goes | 4 | one unit per element visited |
| `hash of an int` | constant | 4 | big integers: words |
| `json.encode, json.decode, json.indent` | as it goes | 4 | memory of the result: 1 unit per 32 bytes, and one unit per node |
| `list comprehension, dict comprehension` | constant | 4 | one step per element |
| `load/store/constant/jump/call (opcodes)` | constant | 4 | one step each |
| `s1 | s2, s1 & s2, s1 - s2, s1 ^ s2` | as it goes | 4 | a copy and one insert or lookup per element |
| `x % y (string interpolation)` | as it goes | 4 | memory of the result; the limit of the thread bounds the form |
| `x * n (string, list, tuple)` | as it goes | 4 | memory: 1 unit per 32 bytes of the result (above 1 KiB) |
| `x * y, x // y, x % y (big int)` | as it goes | 4 | words(x) * words(y) / 8 |
| `x + y (int, float)` | constant | 4 | big integers: words of the longer |
| `x + y (list, tuple)` | as it goes | 4 | memory: 1 unit per 32 bytes (allocation, above 1 KiB) |
| `x + y (string, bytes)` | as it goes | 4 | memory: 1 unit per 32 bytes (allocation, above 1 KiB) |
| `x - y, x & y, x | y, x ^ y, x << y, x >> y (int)` | as it goes | 4 | words of the longer |
| `x == y, x != y (dict)` | as it goes | 4 | one lookup per entry, and the values compared |
| `x == y, x != y, x < y, x <= y, x > y, x >= y (list, tuple)` | as it goes | 4 | one unit per pair visited, and the leaves compared |
| `x == y, x != y, x < y, x <= y, x > y, x >= y (string)` | as it goes | 4 | min(len)/64 |
| `x == y, x != y, x < y, x > y (int, float, bool, None)` | constant | 4 | big integers: words |
| `x == y, x != y, x <= y, x < y, x >= y, x > y (set)` | as it goes | 4 | one lookup per element |
| `x in d (dict), x in s (set)` | as it goes | 4 | hash of the key, chain of the bucket |
| `x in l (list, tuple)` | as it goes | 4 | one unit per element visited, and the leaves compared |
| `x in r (range)` | constant | 4 | arithmetic |
| `x in s (string, bytes)` | as it goes | 4 | len(s)/64 |
| `x.attr, x.method` | constant | 4 | a lookup of an attribute by name |
| `x[i:j:k] (list, tuple, string, bytes)` | as it goes | 4 | memory of the result: 1 unit per 32 bytes (above 1 KiB) |
| `x[i] (list, tuple, string, range)` | constant | 4 | an index |
| `x[i] = v (list)` | constant | 4 | an index |
| `x[k] (dict)` | as it goes | 4 | hash of the key, chain of the bucket |
| `x[k] = v (dict)` | as it goes | 4 | hash of the key, chain of the bucket, an insert |
| `{k: v}, [a, b], (a, b), lambda` | constant | 4 | the number of elements is in the code |
