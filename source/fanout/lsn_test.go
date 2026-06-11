package fanout

import "testing"

func TestCompareLSN(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0/10", "0/20", -1},
		{"0/20", "0/10", 1},
		{"0/10", "0/10", 0},
		{"1/0", "0/FFFFFFFF", 1}, // high part dominates
		{"", "", 0},
		{"", "0/1", -1}, // empty sorts lowest
		{"0/1", "", 1},
		{"bogus", "0/1", -1}, // unparseable sorts lowest
		{"0/1", "bogus", 1},
		{"A/B", "a/b", 0}, // hex is case-insensitive
	}
	for _, tt := range tests {
		if got := compareLSN(tt.a, tt.b); got != tt.want {
			t.Errorf("compareLSN(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
