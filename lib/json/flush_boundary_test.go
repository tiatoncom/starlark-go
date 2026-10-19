package json

import (
	"testing"

	"go.starlark.net/starlark"
)

// struct-0160: the meter charges when the work not charged reaches flushWork,
// not when it passes it: exactly flushWork units are charged at once.
func TestJSON_MeterFlushesAtExactlyAChunk(t *testing.T) {
	th := &starlark.Thread{}
	m := &workMeter{th: th}
	if err := m.add(flushWork - 1); err != nil {
		t.Fatal(err)
	}
	if got := th.Work(); got != 0 {
		t.Fatalf("work %d after %d units: charged before a chunk", got, flushWork-1)
	}
	if err := m.add(1); err != nil {
		t.Fatal(err)
	}
	if got := th.Work(); got != flushWork {
		t.Errorf("work %d after exactly %d units, want them charged", got, flushWork)
	}
	// and at the limit: a chunk that is complete is refused by the limit at
	// once, not by a later one.
	th2 := &starlark.Thread{}
	th2.SetMaxWork(flushWork - 1)
	m2 := &workMeter{th: th2}
	if err := m2.add(flushWork - 1); err != nil {
		t.Fatalf("refused under the limit: %v", err)
	}
	if err := m2.add(1); err == nil {
		t.Errorf("a chunk of %d units over a limit of %d was not refused when it was complete", flushWork, flushWork-1)
	}
}
