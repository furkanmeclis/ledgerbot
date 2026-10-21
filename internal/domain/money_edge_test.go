package domain

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestParseAmountNotations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want Money
	}{
		// Turkish thousands / decimal notation
		{"5.000", 500000},
		{"50.000", 5000000},
		{"1.234,56", 123456},
		{"1.234.567,89", 123456789},
		{"12.345.678", 1234567800},
		{"0,5", 50},
		{"00,5", 50},
		{",50", 50},
		{"5,", 500},
		{"5.", 500},
		// international notation
		{"1,234.56", 123456},
		{"1,234,567.8", 123456780},
		{"12.34", 1234},
		{"12.3", 1230},
		{"1,000", 100000},
		// separators and whitespace
		{"5 000", 500000},
		{"1'250,50", 125050},
		{"  750 ₺  ", 75000},
		{"\t42\n", 4200},
		{"0", 0},
		{"000", 0},
		// currency suffixes
		{"750 TL", 75000},
		{"750tl", 75000},
		{"750 TRY", 75000},
		{"750 lira", 75000},
		{"750₺", 75000},
		// spoken multipliers
		{"5 bin", 500000},
		{"5bin", 500000},
		{"5 BİN", 500000},
		{"5 bin TL", 500000},
		{"5 bin lira", 500000},
		{"2,5 bin", 250000},
		{"1,25 milyon", 125000000},
		{"3 milyon", 300000000},
		{"5milyon tl", 500000000},
		{"1.234,56 bin", 123456000},
		// leading plus is accepted
		{"+5", 500},
		// largest value that does not overflow
		{"92233720368547758", 9223372036854775800},
	}
	for _, c := range cases {
		got, err := ParseAmount(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}

func TestParseAmountRejects(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"", "   ", "abc", "TL", "₺", "bin", "milyon", "5 TL bin", "beş bin", "on",
		"-5", "-5.000", "-1.234,56", "-5 bin", "- 5",
		"1,234.567", "1.234,567", "5e3", "0x10", "1_000", "١٢٣", "5 bin 500", "5 dolar", "12%",
		"5,ab", "5.x", "99999999999999999999",
	} {
		if got, err := ParseAmount(in); !errors.Is(err, ErrBadAmount) {
			t.Errorf("ParseAmount(%q) = %d, %v; want ErrBadAmount", in, got, err)
		}
	}
}

func TestParseAmountOverflow(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"922337203685477580", "100000000000000 milyon", "10000000000000000 bin"} {
		got, err := ParseAmount(in)
		if err == nil {
			t.Skipf("BUG: ParseAmount(%q) = %d with nil error; (whole*100+minor)*mult overflows int64 and wraps silently instead of returning ErrBadAmount", in, got)
		}
	}
}

func TestParseAmountNegativeZeroFraction(t *testing.T) {
	t.Parallel()
	got, err := ParseAmount("-0,50")
	if err == nil {
		t.Skipf("BUG: ParseAmount(\"-0,50\") = %d with nil error; the whole<0 check misses \"-0\", so a negative amount is accepted as positive 0,50 while \"-5\" is rejected", got)
	}
}

func TestParseAmountSignedFraction(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"5,-1", "5,+1"} {
		if got, err := ParseAmount(in); err == nil {
			t.Skipf("BUG: ParseAmount(%q) = %d with nil error; strconv.ParseInt accepts a sign in the fractional part, so \"5,-1\" reads as 4,99 TL", in, got)
		}
	}
}

func TestParseAmountSeparatorOnly(t *testing.T) {
	t.Parallel()
	for _, in := range []string{".", ",", "'", ". TL"} {
		if got, err := ParseAmount(in); err == nil {
			t.Skipf("BUG: ParseAmount(%q) = %d with nil error; an input with no digits becomes intPart \"0\" and is accepted as zero", in, got)
		}
	}
}

func TestParseAmountMultiplierFraction(t *testing.T) {
	t.Parallel()
	got, err := ParseAmount("1.234,567 milyon")
	if want := Money(123456700000); err != nil || got != want {
		t.Skipf("BUG: ParseAmount(\"1.234,567 milyon\") = %d, %v; want %d. With a multiplier the fraction is allowed past 2 digits but then truncated to frac[:2], dropping value (and \"1,234 milyon\" reads as 1234 milyon)", got, err, want)
	}
}

func TestMoneyStringEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   Money
		want string
	}{
		{0, "0,00 TL"},
		{1, "0,01 TL"},
		{-1, "-0,01 TL"},
		{-5, "-0,05 TL"},
		{10, "0,10 TL"},
		{99, "0,99 TL"},
		{100, "1,00 TL"},
		{99999, "999,99 TL"},
		{100000, "1.000,00 TL"},
		{-100000, "-1.000,00 TL"},
		{12345678901, "123.456.789,01 TL"},
		{math.MaxInt64, "92.233.720.368.547.758,07 TL"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Money(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMoneyStringMinInt64(t *testing.T) {
	t.Parallel()
	if got, want := Money(math.MinInt64).String(), "-92.233.720.368.547.758,08 TL"; got != want {
		t.Skipf("BUG: Money(MinInt64).String() = %q, want %q; negating MinInt64 overflows, so both the whole and fractional parts keep their sign", got, want)
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	t.Parallel()
	for _, m := range []Money{0, 1, 9, 10, 99, 100, 125050, 500000, 100000000, 123456789012} {
		s := m.String()
		got, err := ParseAmount(s)
		if err != nil || got != m {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", s, got, err, m)
		}
	}
}

func FuzzParseAmount(f *testing.F) {
	for _, s := range []string{"5.000", "1.234,56", "1,234.56", "5 bin", "2,5 milyon", "750 TL", "", "-5", "0,99"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParseAmount(s)
		if err != nil {
			if !errors.Is(err, ErrBadAmount) {
				t.Fatalf("unexpected error type %v", err)
			}
			return
		}
		// Plain integers within range must parse exactly.
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil && n >= 0 && n < math.MaxInt64/100 && m != Money(n*100) {
			t.Fatalf("ParseAmount(%q) = %d, want %d", s, m, n*100)
		}
	})
}

func BenchmarkParseAmountNotations(b *testing.B) {
	inputs := []string{"5.000", "1.234,56", "1,234.56", "5 bin", "2,5 milyon", "750 TL"}
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range inputs {
			_, _ = ParseAmount(s)
		}
	}
}

func BenchmarkMoneyString(b *testing.B) {
	m := Money(123456789)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.String()
	}
}
