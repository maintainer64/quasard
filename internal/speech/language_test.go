// SPDX-License-Identifier: GPL-3.0-only
package speech

import "testing"

func TestEnglishText(t *testing.T) {
	for _, tc := range []struct {
		text    string
		english bool
	}{
		{"Hello, world!", true},
		{"Привет, мир!", false},
		{"Ёжик идёт домой.", false},
		{"Hello привет мир", false},
		{"Hello мир", true},
		{"Я I", false},
		{"123 + 456", false},
		{"", false},
		{"こんにちは", false},
	} {
		if got := englishText(tc.text); got != tc.english {
			t.Errorf("englishText(%q)=%v, want %v", tc.text, got, tc.english)
		}
	}
}
