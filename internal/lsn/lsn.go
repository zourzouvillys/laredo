// Package lsn compares PostgreSQL WAL-LSN position strings ("X/XXXXXXXX").
//
// It is the default position comparator for laredo sources whose upstream is
// PostgreSQL — the overwhelming common case — and for the offline archive tools
// (reconstruct, the archive source). Sources fronting a non-PostgreSQL upstream
// supply their own comparator instead. Positions are opaque strings at the
// laredo.SyncSource boundary; this package is the one place that knows the LSN
// encoding, so fan-out, the archive source, and the CLI share it rather than
// each carrying a copy.
package lsn

import (
	"strconv"
	"strings"
)

// Compare orders two PostgreSQL WAL-LSN strings ("X/XXXXXXXX"): negative if
// a < b, zero if equal, positive if a > b. The empty string denotes "before any
// change" (a fresh snapshot resets the position to "") and sorts lowest; an
// unparseable value also sorts lowest, defensively. Hex is case-insensitive.
func Compare(a, b string) int {
	la, aok := Parse(a)
	lb, bok := Parse(b)
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

// Parse parses "X/XXXXXXXX" (hex) into a uint64 byte position. The bool is
// false when s is empty or not a valid LSN.
func Parse(s string) (uint64, bool) {
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
