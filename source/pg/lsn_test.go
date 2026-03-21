package pg

import "testing"

func TestParseLSN(t *testing.T) {
	tests := []struct {
		input   string
		want    LSN
		wantErr bool
	}{
		{"0/0", 0, false},
		{"0/00000001", 1, false},
		{"0/01", 1, false},
		{"0/FFFFFFFF", 0x00000000FFFFFFFF, false},
		{"1/0", 0x0000000100000000, false},
		{"1/00000000", 0x0000000100000000, false},
		{"1A/2B3C4D5E", 0x0000001A2B3C4D5E, false},
		{"FF/FFFFFFFF", 0x000000FFFFFFFFFF, false},
		// Lower case.
		{"1a/2b3c4d5e", 0x0000001A2B3C4D5E, false},
		// Error cases.
		{"", 0, true},
		{"noslash", 0, true},
		{"/", 0, true},
		{"/0", 0, true},
		{"0/", 0, true},
		{"G/0", 0, true},        // invalid hex
		{"0/ZZZZZZZZ", 0, true}, // invalid hex
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseLSN(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseLSN(%q) = %d (0x%X), want %d (0x%X)", tt.input, got, uint64(got), tt.want, uint64(tt.want))
			}
		})
	}
}

func TestLSN_String(t *testing.T) {
	tests := []struct {
		lsn  LSN
		want string
	}{
		{0, "0/00000000"},
		{1, "0/00000001"},
		{0xFFFFFFFF, "0/FFFFFFFF"},
		{0x100000000, "1/00000000"},
		{0x1A2B3C4D5E, "1A/2B3C4D5E"},
		{0xFFFFFFFFFF, "FF/FFFFFFFF"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.lsn.String()
			if got != tt.want {
				t.Errorf("LSN(%d).String() = %q, want %q", tt.lsn, got, tt.want)
			}
		})
	}
}

func TestLSN_RoundTrip(t *testing.T) {
	// Parse → String → Parse should be identity.
	inputs := []string{
		"0/00000000",
		"0/00000001",
		"0/FFFFFFFF",
		"1/00000000",
		"1A/2B3C4D5E",
		"FF/FFFFFFFF",
	}

	for _, s := range inputs {
		lsn, err := ParseLSN(s)
		if err != nil {
			t.Fatalf("ParseLSN(%q): %v", s, err)
		}
		roundTripped, err := ParseLSN(lsn.String())
		if err != nil {
			t.Fatalf("ParseLSN(%q): %v", lsn.String(), err)
		}
		if lsn != roundTripped {
			t.Errorf("round-trip failed: %q → %d → %q → %d", s, lsn, lsn.String(), roundTripped)
		}
	}
}

func TestCompareLSN(t *testing.T) {
	tests := []struct {
		name string
		a, b LSN
		want int
	}{
		{"equal zero", 0, 0, 0},
		{"equal nonzero", 100, 100, 0},
		{"a < b", 1, 2, -1},
		{"a > b", 2, 1, 1},
		{"cross segment a < b", LSN(0xFFFFFFFF), LSN(0x100000000), -1},
		{"cross segment a > b", LSN(0x100000000), LSN(0xFFFFFFFF), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareLSN(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("CompareLSN(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareLSN_InvalidTypes(t *testing.T) {
	// Non-LSN types should return 0.
	if got := CompareLSN("not-lsn", LSN(1)); got != 0 {
		t.Errorf("expected 0 for non-LSN a, got %d", got)
	}
	if got := CompareLSN(LSN(1), 42); got != 0 {
		t.Errorf("expected 0 for non-LSN b, got %d", got)
	}
}
