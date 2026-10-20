package domain

import (
	"errors"
	"strconv"
	"strings"
)

// Money is an amount in minor units (kuruş for TRY). Floats never touch money.
type Money int64

// ErrBadAmount is returned when a string cannot be read as an amount.
var ErrBadAmount = errors.New("domain: invalid amount")

// ParseAmount reads Turkish and international amount notations without an LLM:
// "5.000", "5000", "1.250,50", "1,250.50", "12,5", "5 bin", "2,5 milyon", "750 TL".
func ParseAmount(s string) (Money, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suf := range []string{"tl", "₺", "try", "lira"} {
		s = strings.TrimSpace(strings.TrimSuffix(s, suf))
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "milyon"):
		mult, s = 1_000_000, strings.TrimSpace(strings.TrimSuffix(s, "milyon"))
	case strings.HasSuffix(s, "bin"):
		mult, s = 1_000, strings.TrimSpace(strings.TrimSuffix(s, "bin"))
	}
	if s == "" {
		return 0, ErrBadAmount
	}
	intPart, frac := splitDecimal(s)
	intPart = strings.NewReplacer(".", "", ",", "", " ", "", "'", "").Replace(intPart)
	if intPart == "" {
		intPart = "0"
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || whole < 0 {
		return 0, ErrBadAmount
	}
	var minor int64
	if frac != "" {
		if len(frac) > 2 && mult == 1 {
			return 0, ErrBadAmount
		}
		for len(frac) < 2 {
			frac += "0"
		}
		f, err := strconv.ParseInt(frac[:2], 10, 64)
		if err != nil {
			return 0, ErrBadAmount
		}
		minor = f
	}
	total := (whole*100 + minor) * mult
	return Money(total), nil
}

// splitDecimal decides which separator (if any) is the decimal mark.
// The rightmost separator is decimal only when it is followed by 1-2 digits,
// or when both separators are present.
func splitDecimal(s string) (string, string) {
	lastDot, lastComma := strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ',')
	idx := max(lastDot, lastComma)
	if idx < 0 {
		return s, ""
	}
	tail := s[idx+1:]
	both := lastDot >= 0 && lastComma >= 0
	if both || (len(tail) > 0 && len(tail) <= 2) {
		return s[:idx], tail
	}
	return s, ""
}

// String formats in Turkish notation: 1.250,50 TL.
func (m Money) String() string {
	neg := m < 0
	if neg {
		m = -m
	}
	whole := strconv.FormatInt(int64(m)/100, 10)
	var b strings.Builder
	b.Grow(len(whole) + len(whole)/3 + 8)
	if neg {
		b.WriteByte('-')
	}
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	frac := int64(m) % 100
	b.WriteByte(',')
	if frac < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.FormatInt(frac, 10))
	b.WriteString(" TL")
	return b.String()
}
