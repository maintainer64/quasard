// SPDX-License-Identifier: GPL-3.0-only

package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestCleanForSpeech(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"<think>secret\nmore</think> **Привет**, _мир_!", "Привет, мир!"},
		{"[ссылка](https://example.com) и `код` > | #", "ссылка и код"},
		{"  - первый\n• второй\n1. третий\n٢) четвертый", "первый второй третий четвертый"},
		{"   \n\t слова слова  ", "слова слова"},
		{"<think>незаконченный", "<thinkнезаконченный"},
		{"[не ссылка]()", "[не ссылка]()"},
	} {
		if got := CleanForSpeech(tt.in); got != tt.want {
			t.Errorf("%q => %q; want %q", tt.in, got, tt.want)
		}
	}
}
func TestSentenceSplitter(t *testing.T) {
	s := NewSentenceSplitter(12, 120)
	if got := s.Feed("Да. "); len(got) != 0 {
		t.Fatal(got)
	}
	if got := s.Feed("Еще немного! Остаток"); !reflect.DeepEqual(got, []string{"Да. Еще немного!"}) {
		t.Fatal(got)
	}
	if got := s.Flush(); !reflect.DeepEqual(got, []string{"Остаток"}) {
		t.Fatal(got)
	}
	if len(s.Flush()) != 0 {
		t.Fatal("second flush")
	}
	// End-of-current-chunk is a boundary even before future whitespace arrives.
	s = NewSentenceSplitter(12, 120)
	if got := s.Feed("Добрый день!!»\")"); len(got) != 1 {
		t.Fatal(got)
	}
	if got := s.Feed("Да;\nНет… "); !reflect.DeepEqual(got, []string{"Да;", "Нет…"}) {
		t.Fatal(got)
	}
	s = NewSentenceSplitter(1, 120)
	if got := s.Feed("Раз.Два!Три? Финал\n"); !reflect.DeepEqual(got, []string{"Раз.Два!Три?", "Финал"}) {
		t.Fatal(got)
	}
	// Soft cuts require >soft_max total runes and boundary >=30 runes.
	s = NewSentenceSplitter(12, 120)
	long := strings.Repeat("я", 50) + ", " + strings.Repeat("я", 50) + ": " + strings.Repeat("я", 20)
	if got := s.Feed(long); !reflect.DeepEqual(got, []string{strings.Repeat("я", 50) + ", " + strings.Repeat("я", 50) + ":"}) {
		t.Fatal(got)
	}
	if got := s.Flush(); !reflect.DeepEqual(got, []string{strings.Repeat("я", 20)}) {
		t.Fatal(got)
	}
	s = NewSentenceSplitter(12, 120)
	if got := s.Feed(strings.Repeat("я", 60) + ", "); len(got) != 0 {
		t.Fatal("byte lengths used", got)
	}
	var zero SentenceSplitter
	if len(zero.Feed("Коротко.")) != 0 {
		t.Fatal("bad zero value defaults")
	}
}
