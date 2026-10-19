//go:build race

package json_test

// raceOn: the race detector inflates the allocations that a test measures.
const raceOn = true
