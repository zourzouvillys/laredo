package fanout

import (
	"strconv"
	"strings"
)

// compareLSN orders two PostgreSQL WAL-LSN strings ("X/XXXXXXXX"). It is the
// default position comparator for a fan-out source, since a fan-out almost
// always fronts a PostgreSQL upstream. The empty string sorts lowest — it
// denotes "before any change" (a fresh snapshot resets the position to ""). An
// unparseable value also sorts lowest, defensively; supply a custom comparator
// via WithPositionComparator for a non-PostgreSQL upstream.
func compareLSN(a, b string) int {
	la, aok := parseLSN(a)
	lb, bok := parseLSN(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	case la < lb:
		return -1
	case la > lb:
		return 1
	default:
		return 0
	}
}

// parseLSN parses "X/XXXXXXXX" (hex) into a uint64 byte position.
func parseLSN(s string) (uint64, bool) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok || hi == "" || lo == "" {
		return 0, false
	}
	high, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, false
	}
	low, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, false
	}
	return high<<32 | low, true
}
