//go:build race

package testutil

import "time"

// raceTimeoutScale multiplies eventually-style test deadlines under the race
// detector, whose instrumentation can stretch async pipeline dispatch well past
// a deadline that is comfortable in an uninstrumented run. Scaling the ceiling
// (not adding a fixed delay) keeps healthy runs fast while removing the flake.
const raceTimeoutScale time.Duration = 3
