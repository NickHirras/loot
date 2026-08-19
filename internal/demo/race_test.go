//go:build race

package demo

// raceEnabled reports whether this test binary was built with -race. The race
// detector costs roughly ten times the CPU, so a wall-clock assertion that is
// meaningful in a normal run is only noise under it.
const raceEnabled = true
