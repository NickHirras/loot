//go:build !race

package demo

// raceEnabled reports whether this test binary was built with -race; see
// race_test.go.
const raceEnabled = false
