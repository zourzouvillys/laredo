package pg

import (
	"fmt"
	"strconv"
	"strings"
)

// LSN is a PostgreSQL Log Sequence Number. It represents a byte position in
// the write-ahead log and is the position type used by the PostgreSQL source.
// Format: "X/XXXXXXXX" where X is uppercase hexadecimal.
type LSN uint64

// ParseLSN parses a PostgreSQL LSN string in the format "X/XXXXXXXX".
// Both upper and lower case hex digits are accepted.
func ParseLSN(s string) (LSN, error) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok || hi == "" || lo == "" {
		return 0, fmt.Errorf("invalid LSN %q: expected format X/XXXXXXXX", s)
	}

	high, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: bad high part: %w", s, err)
	}

	low, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: bad low part: %w", s, err)
	}

	return LSN(high<<32 | low), nil
}

// String returns the LSN in PostgreSQL's canonical format "X/XXXXXXXX".
func (l LSN) String() string {
	return fmt.Sprintf("%X/%08X", uint32(l>>32), uint32(l)) //nolint:gosec // intentional truncation to extract low 32 bits
}

// CompareLSN returns negative if a < b, zero if equal, positive if a > b.
func CompareLSN(a, b any) int {
	la, ok := a.(LSN)
	if !ok {
		return 0
	}
	lb, ok := b.(LSN)
	if !ok {
		return 0
	}
	switch {
	case la < lb:
		return -1
	case la > lb:
		return 1
	default:
		return 0
	}
}
