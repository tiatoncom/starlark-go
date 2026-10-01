package starlark_test

// The ratio of the work to the steps for the programs that are not attacks: the
// handlers of the reviews, at sizes of a few dozen to a few thousand records, and
// the programs of the differential. The host that sets a limit of work as a
// multiple K of its limit of steps reads K from here (steps-prices.md).

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

type ratioRow struct {
	src         string
	steps, work uint64
	alloc       uint64
}

func runRatio(src string) (row ratioRow, err error) {
	th := &starlark.Thread{Name: "ratio", Print: func(*starlark.Thread, string) {}}
	th.SetMaxAllocBytes(1 << 30)
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	_, err = runDiffWith(th, src)
	return ratioRow{src: src, steps: th.Steps, work: th.Work(), alloc: th.AllocatedBytes()}, err
}

func TestRatios_WorkToStepsOfTheHandlers(t *testing.T) {
	order := "orders = [{'id': i, 'cust': 'c%d' % (i % 37), 'sku': 'sku-%d' % (i % 211), 'qty': i % 7 + 1, 'amt': (i * 31) % 997, 'status': 'open' if i % 3 else 'closed'} for i in range(N)]\n"
	names := []string{"filter and totals", "report: join and %", "report: s += line", "count by sku in a nested dict", "json encode, decode, modify", "sort by a key", "dedupe and update"}
	var handlers []string
	for _, src := range reviewHandlers(order) {
		handlers = append(handlers, src)
	}
	min, max := 1e9, 0.0
	for _, n := range []int{20, 200, 2000} {
		for i, h := range handlers {
			row, err := runRatio(fmt.Sprintf("N = %d\n%s", n, h))
			if err != nil {
				t.Errorf("%s: %v", names[i], err)
				continue
			}
			ratio := float64(row.work) / float64(row.steps)
			if ratio < min {
				min = ratio
			}
			if ratio > max {
				max = ratio
			}
			fmt.Printf("RATIO %-32s N=%-5d steps %8d work %9d  work/steps %5.2f  charged %9d bytes\n", names[i], n, row.steps, row.work, ratio, row.alloc)
		}
	}
	fmt.Printf("RATIO handlers: work/steps from %.2f to %.2f\n", min, max)
}

func TestRatios_WorkToStepsOfTheDifferentialPrograms(t *testing.T) {
	var ratios []float64
	type top struct {
		r   float64
		src string
	}
	var tops []top
	for _, golden := range []string{goldenSmall, goldenBig, goldenReview} {
		for _, g := range readGolden(t, golden) {
			if g.Status == "panic" || g.Steps < 50 {
				continue
			}
			row, err := runRatio(g.Src)
			_ = err
			if row.steps == 0 {
				continue
			}
			ratios = append(ratios, float64(row.work)/float64(row.steps))
			tops = append(tops, top{float64(row.work) / float64(row.steps), g.Src})
		}
	}
	sort.Float64s(ratios)
	sort.Slice(tops, func(i, j int) bool { return tops[i].r > tops[j].r })
	for i := 0; i < 5 && i < len(tops); i++ {
		src := strings.ReplaceAll(tops[i].src, "\n", "; ")
		if len(src) > 110 {
			src = src[:110] + "..."
		}
		fmt.Printf("RATIO top %d: %.1f  %s\n", i+1, tops[i].r, src)
	}
	at := func(p float64) float64 { return ratios[int(float64(len(ratios)-1)*p)] }
	fmt.Printf("RATIO differential programs (%d of 50+ steps): work/steps p50 %.2f p90 %.2f p99 %.2f max %.2f\n", len(ratios), at(0.5), at(0.9), at(0.99), ratios[len(ratios)-1])
}

func reviewHandlers(order string) []string {
	var out []string
	for _, h := range []string{
		"open_big = [o for o in orders if o['status'] == 'open' and o['amt'] > 100]\nby_cust = {}\nfor o in open_big:\n    by_cust[o['cust']] = by_cust.get(o['cust'], 0) + o['amt'] * o['qty']\nresult = [len(open_big), len(by_cust)]\n",
		"lines = ['ID  CUSTOMER  SKU  QTY  AMOUNT']\nfor o in orders:\n    lines.append('%d  %s  %s  %d  %d' % (o['id'], o['cust'], o['sku'], o['qty'], o['amt']))\nreport = '\\n'.join(lines)\nresult = len(report)\n",
		"report = 'ID  CUSTOMER  SKU  QTY  AMOUNT\\n'\nfor o in orders:\n    report += '%d  %s  %s  %d  %d\\n' % (o['id'], o['cust'], o['sku'], o['qty'], o['amt'])\nresult = len(report)\n",
		"cnt = {}\nfor o in orders:\n    c = cnt.setdefault(o['cust'], {})\n    c[o['sku']] = c.get(o['sku'], 0) + o['qty']\ntop = sorted(cnt.items(), key=lambda kv: -len(kv[1]))[:5]\nresult = [(k, len(v)) for k, v in top]\n",
		"s = json.encode({'orders': orders, 'status': 'open'})\nd = json.decode(s)\nfor o in d['orders']:\n    o['amt'] += 1\ns2 = json.encode(d)\nresult = [len(s), len(s2)]\n",
		"srt = sorted(orders, key=lambda o: (o['cust'], -o['amt']))\nresult = [o['id'] for o in srt[:10]]\n",
		"seen = set()\nstate = {'a': 1, 'b': 2, 'c': 3, 'd': 4, 'e': 5, 'f': 6}\npatch = {'a': 2, 'b': 3, 'c': 4, 'd': 5, 'e': 6, 'f': 7}\nids = [o['id'] % 50 for o in orders]\nuniq = set(ids)\nfor o in orders:\n    seen.update([o['cust'], o['sku']])\n    state.update(patch)\n    state |= patch\nresult = [len(uniq), len(seen)]\n",
	} {
		out = append(out, order+h)
	}
	_ = strings.Repeat
	return out
}
