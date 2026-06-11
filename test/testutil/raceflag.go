//go:build !race

package testutil

import "time"

// raceTimeoutScale multiplies eventually-style test deadlines. Without the race
// detector, deadlines are used as written.
//
// It is a time.Duration so callers can write `timeout *= raceTimeoutScale`.
const raceTimeoutScale time.Duration = 1
