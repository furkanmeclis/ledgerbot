package domain

import "testing"

func TestParseAmount(t *testing.T) {
	cases := map[string]Money{
		"5.000":      500000,
		"5000":       500000,
		"5.000 TL":   500000,
		"1.250,50":   125050,
		"1,250.50":   125050,
		"12,5":       1250,
		"5 bin":      500000,
		"2,5 milyon": 250000000,
		"750₺":       75000,
		"0,99":       99,
		"1.000.000":  100000000,
	}
	for in, want := range cases {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "TL", "-5"} {
		if _, err := ParseAmount(bad); err == nil {
			t.Errorf("ParseAmount(%q) expected error", bad)
		}
	}
}

func TestMoneyString(t *testing.T) {
	cases := map[Money]string{
		125050:    "1.250,50 TL",
		500000:    "5.000,00 TL",
		5:         "0,05 TL",
		100000000: "1.000.000,00 TL",
		-1250:     "-12,50 TL",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d.String() = %q; want %q", in, got, want)
		}
	}
}

func BenchmarkParseAmount(b *testing.B) {
	for b.Loop() {
		_, _ = ParseAmount("1.250,50 TL")
	}
}
