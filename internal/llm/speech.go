// SPDX-License-Identifier: GPL-3.0-only

package llm

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const space = `[\t\n\f\r \x0b\x{0085}\p{Z}]`

var thinkText = regexp.MustCompile(`(?s)<think>.*?</think>`)
var markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
var markdown = regexp.MustCompile("[*_#`>|]+")
var listPrefix = regexp.MustCompile(`(?m)^` + space + `*([-•]|\p{Nd}+[.)])` + space + `+`)

// CleanForSpeech removes think blocks, Markdown markup and list prefixes.
func CleanForSpeech(text string) string {
	text = thinkText.ReplaceAllString(text, "")
	text = markdownLink.ReplaceAllString(text, "$1")
	text = markdown.ReplaceAllString(text, "")
	text = listPrefix.ReplaceAllString(text, "")
	return strings.Join(strings.Fields(text), " ")
}

var sentenceEnd = regexp.MustCompile(`([.!?…;]+[»")]*|\n)(` + space + `+|$)`)
var softEnd = regexp.MustCompile(`[,:—–](` + space + `+)`)

// SentenceSplitter buffers tokens until punctuation boundaries. Lengths count
// Unicode characters, not bytes. Construct with NewSentenceSplitter(12,120)
// for the reference defaults; its zero value uses those defaults as well.
type SentenceSplitter struct {
	buf                      string
	firstMin, softMax, count int
	initialized              bool
}

func NewSentenceSplitter(firstMin, softMax int) *SentenceSplitter {
	return &SentenceSplitter{firstMin: firstMin, softMax: softMax, initialized: true}
}
func (s *SentenceSplitter) Feed(chunk string) []string {
	if !s.initialized {
		s.firstMin = 12
		s.softMax = 120
		s.initialized = true
	}
	s.buf += chunk
	var out []string
	for {
		need := 2
		if s.count == 0 {
			need = s.firstMin
		}
		cut := -1
		for _, m := range sentenceEnd.FindAllStringIndex(s.buf, -1) {
			if utf8.RuneCountInString(strings.TrimSpace(s.buf[:m[1]])) >= need {
				cut = m[1]
				break
			}
		}
		if cut < 0 && utf8.RuneCountInString(s.buf) > s.softMax {
			for _, m := range softEnd.FindAllStringIndex(s.buf, -1) {
				if utf8.RuneCountInString(s.buf[:m[1]]) >= 30 {
					cut = m[1]
				}
			}
		}
		if cut < 0 {
			break
		}
		sentence := strings.TrimSpace(s.buf[:cut])
		s.buf = s.buf[cut:]
		if sentence != "" {
			out = append(out, sentence)
			s.count++
		}
	}
	return out
}
func (s *SentenceSplitter) Flush() []string {
	rest := strings.TrimSpace(s.buf)
	s.buf = ""
	if rest != "" {
		return []string{rest}
	}
	return nil
}
