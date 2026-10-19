# internal/calibrate/cal-check.awk: the noise criterion of a calibration (procedure v2).
# Usage (from the root of the repository; the whole sequence is at the top of
# starlark/calibrate_test.go), POSIX awk:
#   LC_ALL=C awk -f internal/calibrate/cal-check.awk cal-1.txt ... cal-6.txt
# Input: N >= 2 run files (6 expected), the CAL lines of TestCalibrate, one file
# per run, in the order they were taken:
#   CAL <name...> <ns> ns <work> work <ns/unit> ns/unit <bytes> bytes
#   CAL <name...> ERROR <text>
# Only the CAL lines are read; the uptime and MHz lines, the machine line and the
# "worst first" lines of a run are ignored.
# Only probes of at least 2000 ns (the least time over all the runs) are judged
# (minProbeNanoseconds, floors_test.go). The runs are split into halves: runs
# 1..N/2 (rounded down) against the rest; the time of a half is the least time
# of its runs, which is what the writer keeps.
# Accepted when:
#   0. no probe is missing or ERROR in any run;
#   A. the worst ns/unit of each run (what C is taken from) differs by at most 5% between runs (max/min);
#   B. the probes that decide (ns/unit of the overall least time at least half of the
#      overall worst ns/unit): the halves differ by at most 10%, every one;
#   C. every other probe: at least 90% of them have halves within 10%.
# (The load, under 2 before and after every run, and the MHz lines, are checked by hand.)
FNR == 1 { r++ }
/^CAL / && / ERROR / {
	e = $0; sub(/^CAL /, "", e); sub(/ +ERROR .*$/, "", e)
	allname[e] = 1; errs[e]++; nerr++
	next
}
/^CAL / && $NF == "bytes" {
	name = ""
	for (i = 2; i <= NF - 8; i++) name = name (i > 2 ? " " : "") $i
	ns = $(NF - 7) + 0
	wk = $(NF - 5) + 0
	allname[name] = 1
	seen[name, r] = 1
	v[name, r] = ns
	w[name] = wk
	if (ns >= 2000) {
		u = ns / (wk > 1 ? wk : 1)
		if (u > worst[r]) { worst[r] = u; wname[r] = name }
	}
}
END {
	if (r < 2) { print "need at least two runs"; exit 2 }
	half = int(r / 2)
	# 0. missing / ERROR
	missing = 0
	for (k in allname) {
		bad = (k in errs)
		for (i = 1; i <= r; i++) if (!((k, i) in seen)) bad = 1
		if (bad) { missing++; if (missing <= 5) printf "missing or ERROR: %s\n", k }
	}
	# A
	wmin = wmax = worst[1]
	printf "runs %d (halves: 1..%d against %d..%d)\nworst ns/unit per run:", r, half, half + 1, r
	for (i = 1; i <= r; i++) {
		if (worst[i] < wmin) wmin = worst[i]
		if (worst[i] > wmax) wmax = worst[i]
		printf " %.2f", worst[i]
	}
	printf "\n"
	ws = (wmin > 0) ? wmax / wmin : 0
	printf "A. spread of the worst ns/unit %.3f (limit 1.050); worst probes: %s / %s\n", ws, wname[1], wname[r]
	# the overall least time of each probe, and the overall worst ns/unit
	overall = 0
	for (k in allname) {
		ok = 1
		for (i = 1; i <= r; i++) if (!((k, i) in seen)) ok = 0
		if (!ok) continue
		lo = v[k, 1]
		for (i = 2; i <= r; i++) if (v[k, i] < lo) lo = v[k, i]
		mn[k] = lo
		if (lo < 2000) continue
		u = lo / (w[k] > 1 ? w[k] : 1)
		uo[k] = u
		if (u > overall) { overall = u; oname = k }
	}
	printf "overall worst ns/unit (least times) %.2f (%s); deciding probes: ns/unit >= %.2f\n", overall, oname, overall / 2
	nb = 0; bbad = 0; nc = 0; cok = 0
	for (k in uo) {
		h1 = h2 = ""
		for (i = 1; i <= r; i++) {
			x = v[k, i]
			if (i <= half) { if (h1 == "" || x < h1) h1 = x }
			else { if (h2 == "" || x < h2) h2 = x }
		}
		s = (h1 > h2 ? h1 / h2 : h2 / h1)
		sp[k] = s
		if (uo[k] >= overall / 2) {
			nb++
			if (s > 1.10) { bbad++; bl[bbad] = k }
		} else {
			nc++
			if (s <= 1.10) cok++
		}
	}
	printf "B. deciding probes %d, halves over 10%%: %d\n", nb, bbad
	# the worst offenders: the largest spreads, deciding first
	for (j = 1; j <= 5; j++) {
		bk = ""; bs = 0
		for (k in uo) if (!(k in used) && sp[k] > bs && uo[k] >= overall / 2) { bs = sp[k]; bk = k }
		if (bk == "") break
		used[bk] = 1
		printf "   offender (deciding) %.3f  %.2f ns/unit  %s\n", bs, uo[bk], bk
	}
	printf "C. other probes %d, halves within 10%%: %d = %.1f%% (limit 90%%)\n", nc, cok, (nc ? 100 * cok / nc : 100)
	for (j = 1; j <= 3; j++) {
		bk = ""; bs = 0
		for (k in uo) if (!(k in used) && sp[k] > bs) { bs = sp[k]; bk = k }
		if (bk == "") break
		used[bk] = 1
		printf "   offender (other) %.3f  %.2f ns/unit  %s\n", bs, uo[bk], bk
	}
	okA = (ws <= 1.05); okB = (bbad == 0 && nb > 0); okC = (nc == 0 || cok >= 0.9 * nc)
	printf "0. probes missing or ERROR: %d\n", missing
	good = (okA && okB && okC && missing == 0)
	print (good ? "ACCEPT" : "REJECT")
	exit(good ? 0 : 1)
}
