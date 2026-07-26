package lsn

import "testing"

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int // sign
	}{
		{"0/10", "0/20", -1},
		{"0/20", "0/10", 1},
		{"0/10", "0/10", 0},
		{"1/0", "0/FFFFFFFF", 1}, // high part dominates
		{"0/10", "0/2", 1},       // hex, not lexical
		{"", "", 0},
		{"", "0/1", -1}, // empty sorts lowest
		{"0/1", "", 1},
		{"bogus", "0/1", -1}, // unparseable sorts lowest
		{"0/1", "bogus", 1},
		{"A/B", "a/b", 0}, // hex is case-insensitive
	}
	for _, tt := range tests {
		if got := sign(Compare(tt.a, tt.b)); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want sign %d", tt.a, tt.b, Compare(tt.a, tt.b), tt.want)
		}
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		s    string
		want uint64
		ok   bool
	}{
		{"0/0", 0, true},
		{"0/10", 0x10, true},
		{"1/0", 1 << 32, true},
		{"1/FFFFFFFF", 1<<32 | 0xFFFFFFFF, true},
		{"a/b", 0xa<<32 | 0xb, true}, // lowercase hex
		{"", 0, false},
		{"0/", 0, false},
		{"/0", 0, false},
		{"nope", 0, false},
		{"0/zz", 0, false},
	}
	for _, tt := range tests {
		got, ok := Parse(tt.s)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Parse(%q) = (%d, %t), want (%d, %t)", tt.s, got, ok, tt.want, tt.ok)
		}
	}
}
