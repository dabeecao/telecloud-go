package utils

import (
	"sort"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	tests := []struct {
		s1, s2   string
		expected bool
	}{
		// Standard lexicographical vs numerical sorting
		{"2.png", "10.png", true},
		{"10.png", "2.png", false},
		{"page2.png", "page10.png", true},
		{"page10.png", "page2.png", false},

		// Case insensitivity
		{"a", "B", true},
		{"B", "a", false},
		{"Page2.png", "page10.png", true},

		// Leading zeros
		{"page02.png", "page10.png", true},
		{"page2.png", "page02.png", true}, // shorter digit string first
		{"page02.png", "page2.png", false},

		// Ties resolved case-sensitively
		{"Page.png", "page.png", true},
		{"page.png", "Page.png", false},

		// Missing numbers
		{"abc", "abc1", true},
		{"abc1", "abc", false},
	}

	for _, tt := range tests {
		actual := NaturalLess(tt.s1, tt.s2)
		if actual != tt.expected {
			t.Errorf("NaturalLess(%q, %q) = %v; want %v", tt.s1, tt.s2, actual, tt.expected)
		}
	}
}

func TestNaturalSortSlice(t *testing.T) {
	input := []string{
		"page10.png",
		"page2.png",
		"page1.png",
		"Page2.png",
		"page02.png",
		"page01.png",
	}
	expected := []string{
		"page1.png",
		"page01.png",
		"Page2.png",
		"page2.png",
		"page02.png",
		"page10.png",
	}

	sort.Slice(input, func(i, j int) bool {
		return NaturalLess(input[i], input[j])
	})

	for i, v := range input {
		if v != expected[i] {
			t.Errorf("Expected element %d to be %q, got %q", i, expected[i], v)
		}
	}
}
