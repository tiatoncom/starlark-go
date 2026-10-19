# internal/calibrate/cal-check.awk: the noise criterion of a calibration of three runs.
# Usage (from the root of the repository; the whole sequence is at the top of
# starlark/calibrate_test.go): LC_ALL=C awk -f internal/calibrate/cal-check.awk cal-1.txt cal-2.txt cal-3.txt
# Input: the CAL lines of TestCalibrate (one file per run):
#   CAL <name...> <ns> ns <work> work <ns/unit> ns/unit <bytes> bytes
# Only these lines are read; the uptime lines, the machine line and the ERROR
# and "worst first" lines of a run are ignored.
# Only probes of at least 2000 ns are judged (minProbeNanoseconds, floors_test.go).
# Accepted when:
#   A. the worst ns/unit of each run (what C is taken from) differs by at most 5% between runs;
#   B. every probe's least time differs by at most 15% between runs;
#   C. at least 90% of the probes differ by at most 5%.
# (The load, under 2 before and after every run, is checked by hand from the uptime lines.)
FNR == 1 { r++ }
/^CAL / && $NF == "bytes" {
	name = ""
	for (i = 2; i <= NF - 8; i++) name = name (i > 2 ? " " : "") $i
	ns = $(NF - 7) + 0
	u = $(NF - 3) + 0
	if (ns < 2000) next
	if (u > worst[r]) { worst[r] = u; wname[r] = name }
	key[name] = 1
	v[name, r] = ns
}
END {
	if (r < 2) { print "need at least two runs"; exit 2 }
	wmin = wmax = worst[1]
	for (i = 2; i <= r; i++) { if (worst[i] < wmin) wmin = worst[i]; if (worst[i] > wmax) wmax = worst[i] }
	printf "runs %d\nworst ns/unit per run:", r
	for (i = 1; i <= r; i++) printf " %.2f (%s)", worst[i], wname[i]
	ws = wmax / wmin
	printf "\nA. spread of the worst ns/unit %.3f (limit 1.050)\n", ws
	n = 0; within = 0; maxs = 0; missing = 0
	for (k in key) {
		ok = 1
		for (i = 1; i <= r; i++) if (!((k, i) in v)) ok = 0
		if (!ok) { missing++; continue }
		lo = hi = v[k, 1]
		for (i = 2; i <= r; i++) { if (v[k, i] < lo) lo = v[k, i]; if (v[k, i] > hi) hi = v[k, i] }
		s = hi / lo; n++
		if (s <= 1.05) within++
		if (s > maxs) { maxs = s; mk = k }
	}
	printf "B. probes judged %d (missing in a run %d); largest spread %.3f (%s) (limit 1.150)\n", n, missing, maxs, mk
	printf "C. probes within 5%%: %d of %d = %.1f%% (limit 90%%)\n", within, n, (n ? 100 * within / n : 0)
	okA = ws <= 1.05; okB = maxs <= 1.15; okC = n > 0 && within >= 0.9 * n
	print ((okA && okB && okC && missing == 0) ? "ACCEPT" : "REJECT")
	exit((okA && okB && okC && missing == 0) ? 0 : 1)
}
