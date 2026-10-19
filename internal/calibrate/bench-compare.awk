# internal/calibrate/bench-compare.awk: the overhead of v0.3.0 against v0.2.3 on the same host.
# Usage: LC_ALL=C awk -f internal/calibrate/bench-compare.awk old.txt new.txt
# Input: the raw output of the benchmarks (lines "BenchmarkName-P  N  X ns/op ..."),
# all the repeats of one version in one file (old = v0.2.3, new = v0.3.0).
# The two versions are run on ONE host, as 10 interleaved pairs (old, new, old,
# new, ...), so that the drift of the machine falls on both alike; the load,
# under 2 before and after each pair, is checked by hand from the uptime lines.
# Per benchmark: the median of the repeats, the spread max/min of each version, the ratio new/old.
# The thresholds N, I, M and S below are a PROPOSAL, to be revised after the
# first quiet run. OVER is a decision of the architect, not an automatic
# rejection: the script says which benchmark is over, it does not judge it.
# Accepted when:
#   N. noise: the spread of every benchmark in each version is at most 1.10;
#   I. the interpreter and the compiler (Program/*, Branchy*): every ratio at most 1.10;
#   M. the mixed programs of testdata/benchmark.star (Starlark/*): geometric mean at most 1.25;
#   S. the small built-ins and operators (Small_*): every ratio at most 1.60, their geometric mean at most 1.35.
function med(a, n,    i, j, t) {
	for (i = 2; i <= n; i++) { t = a[i]; for (j = i - 1; j >= 1 && a[j] > t; j--) a[j + 1] = a[j]; a[j + 1] = t }
	return (n % 2) ? a[(n + 1) / 2] : (a[n / 2] + a[n / 2 + 1]) / 2
}
FNR == 1 { f++ }
/^Benchmark/ && $4 == "ns/op" {
	name = $1; sub(/-[0-9]+$/, "", name); sub(/^Benchmark/, "", name)
	k = name SUBSEP f
	n[k]++; v[k, n[k]] = $3 + 0
	names[name] = 1
}
END {
	if (f != 2) { print "need two files: old new"; exit 2 }
	okN = okI = okS = 1; gs = 0; gn = 0; missing = 0
	printf "%-34s %12s %12s %7s %6s %6s\n", "benchmark", "old ns/op", "new ns/op", "ratio", "sp.old", "sp.new"
	for (name in names) {
		for (g = 1; g <= 2; g++) {
			k = name SUBSEP g
			if (!(k in n)) { missing++; continue }
			lo = hi = v[k, 1]
			for (i = 1; i <= n[k]; i++) { a[i] = v[k, i]; if (a[i] < lo) lo = a[i]; if (a[i] > hi) hi = a[i] }
			m[g] = med(a, n[k]); sp[g] = hi / lo
		}
		if (!((name SUBSEP 1) in n) || !((name SUBSEP 2) in n)) { printf "%-34s missing in a version\n", name; continue }
		r = m[2] / m[1]
		flag = ""
		if (sp[1] > 1.10 || sp[2] > 1.10) { okN = 0; flag = flag " NOISE" }
		if (name ~ /^Small_/) {
			gs += log(r); gn++
			if (r > 1.60) { okS = 0; flag = flag " OVER" }
		} else if (name ~ /^(Program|Branchy)/) {
			if (r > 1.10) { okI = 0; flag = flag " OVER" }
		} else if (name ~ /^Starlark\//) {
			gm += log(r); gmn++
		}
		printf "%-34s %12.0f %12.0f %7.3f %6.3f %6.3f%s\n", name, m[1], m[2], r, sp[1], sp[2], flag
	}
	geo = gn ? exp(gs / gn) : 0
	if (geo > 1.35) okS = 0
	geom = gmn ? exp(gm / gmn) : 0
	okM = (geom <= 1.25)
	printf "N. noise (every spread <= 1.10): %s\n", okN ? "ok" : "NO"
	printf "I. interpreter (Program/*, Branchy*: every ratio <= 1.10): %s\n", okI ? "ok" : "NO"
	printf "M. mixed programs (Starlark/*: geomean <= 1.25): geomean %.3f over %d: %s\n", geom, gmn, okM ? "ok" : "NO"
	printf "S. small (every ratio <= 1.60, geomean <= 1.35): geomean %.3f over %d: %s\n", geo, gn, okS ? "ok" : "NO"
	verdict = (okN && okI && okM && okS && missing == 0)
	print (verdict ? "ACCEPT" : (okN ? "OVER: a decision of the architect" : "REJECT: noise, run again"))
	exit(verdict ? 0 : 1)
}
