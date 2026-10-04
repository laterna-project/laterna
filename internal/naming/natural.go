package naming

import (
	"strings"
	"unicode"
)

// NaturalCompare compares two names reading runs of digits as numbers ("Season 2" before "Season
// 10"), ignoring case.
func NaturalCompare(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ad, bd := unicode.IsDigit(rune(a[0])), unicode.IsDigit(rune(b[0]))
		if ad && bd {
			na, ra := leadingDigits(a)
			nb, rb := leadingDigits(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return len(ta) - len(tb)
			}
			if c := strings.Compare(ta, tb); c != 0 {
				return c
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func leadingDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], s[i:]
}
