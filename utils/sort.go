package utils

import (
	"strings"
)

// NaturalLess compares two strings using a natural sort algorithm.
// It matches numbers numerically and compares letters case-insensitively.
func NaturalLess(s1, s2 string) bool {
	i, j := 0, 0
	for i < len(s1) && j < len(s2) {
		c1, c2 := s1[i], s2[j]
		isDigit1 := c1 >= '0' && c1 <= '9'
		isDigit2 := c2 >= '0' && c2 <= '9'

		if isDigit1 && isDigit2 {
			// Extract digits for s1
			start1 := i
			for i < len(s1) && s1[i] >= '0' && s1[i] <= '9' {
				i++
			}
			dig1 := s1[start1:i]

			// Extract digits for s2
			start2 := j
			for j < len(s2) && s2[j] >= '0' && s2[j] <= '9' {
				j++
			}
			dig2 := s2[start2:j]

			// Trim leading zeros
			trim1 := strings.TrimLeft(dig1, "0")
			trim2 := strings.TrimLeft(dig2, "0")

			if len(trim1) != len(trim2) {
				return len(trim1) < len(trim2)
			}
			if trim1 != trim2 {
				return trim1 < trim2
			}
			if len(dig1) != len(dig2) {
				return len(dig1) < len(dig2)
			}
		} else {
			// Case-insensitive character comparison
			b1 := c1
			if b1 >= 'A' && b1 <= 'Z' {
				b1 = b1 - 'A' + 'a'
			}
			b2 := c2
			if b2 >= 'A' && b2 <= 'Z' {
				b2 = b2 - 'A' + 'a'
			}
			if b1 != b2 {
				return b1 < b2
			}
			i++
			j++
		}
	}
	if len(s1) == len(s2) {
		return s1 < s2
	}
	return len(s1) < len(s2)
}
