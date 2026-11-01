// SPDX-License-Identifier: GPL-3.0-only

// Package intents matches local station commands using the reference Russian grammar.
package intents

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Directive is a station client_action, including an empty payload when unused.
type Directive struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
	Type    string         `json:"type"`
}

type Result struct {
	Text        string      `json:"text"`
	Directives  []Directive `json:"directives"`
	ResetDialog bool        `json:"reset_dialog"`
}

// MusicResult describes a music action; Query is set only for search.
type MusicResult struct {
	Action string
	Query  string
}

func norm(text string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(strings.ToLower(text), "ё", "е") {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	t := strings.Join(strings.Fields(b.String()), " ")
	for _, prefix := range []string{"алиса ", "слушай алиса "} {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return t
}

// Go's regexp word classes are ASCII-only; Python's are Unicode-aware.
func compile(pattern string) *regexp.Regexp {
	pattern = strings.ReplaceAll(pattern, `\b.*\b`, `(?:[^\pL\pN_].*[^\pL\pN_]|[^\pL\pN_])`)
	pattern = strings.ReplaceAll(pattern, `\w`, `[\pL\pN_]`)
	pattern = strings.ReplaceAll(pattern, `\b`, `(?:^|$|[^\pL\pN_])`)
	return regexp.MustCompile(pattern)
}
func full(pattern string) *regexp.Regexp { return compile("^(?:" + pattern + ")$") }

var nums = map[string]int{"ноль": 0, "один": 1, "одна": 1, "два": 2, "две": 2, "три": 3, "четыре": 4, "пять": 5, "шесть": 6, "семь": 7, "восемь": 8, "девять": 9, "десять": 10, "максимум": 10, "минимум": 1}

func level(words []string) (int, bool) {
	for _, w := range words {
		// Unicode decimal digits accepted by Python int (Arabic, fullwidth, etc.).
		var digits strings.Builder
		for _, r := range w {
			found := false
			for _, table := range unicode.Digit.R16 {
				if r >= rune(table.Lo) && r <= rune(table.Hi) && (r-rune(table.Lo))%rune(table.Stride) == 0 {
					digits.WriteByte('0' + byte((r-rune(table.Lo))/rune(table.Stride)%10))
					found = true
					break
				}
			}
			if !found {
				for _, table := range unicode.Digit.R32 {
					if r >= rune(table.Lo) && r <= rune(table.Hi) && (r-rune(table.Lo))%rune(table.Stride) == 0 {
						digits.WriteByte('0' + byte((r-rune(table.Lo))/rune(table.Stride)%10))
						found = true
						break
					}
				}
			}
			if !found {
				digits.Reset()
				break
			}
		}
		if digits.Len() > 0 {
			n, err := strconv.Atoi(digits.String())
			if err != nil {
				return -1, true
			}
			return n, true
		}
		if n, ok := nums[w]; ok {
			return n, true
		}
	}
	return 0, false
}
func action(text, name string, payload map[string]any) *Result {
	if payload == nil {
		payload = map[string]any{}
	}
	return &Result{Text: text, Directives: []Directive{{Name: name, Payload: payload, Type: "client_action"}}}
}

var stop = full(`(стоп|хватит|замолчи|молчи|отмена|тихо|выключись|помолчи)( пожалуйста)?`)
var volume = compile(`\b(громкость|уровень громкости|поставь громкость|сделай громкость)\b`)

const what = `( музыку| музыка| звук| песню| трек)?`

var louder = full(`(сделай|сделать|можно)?` + what + ` ?(погромче|громче|прибавь|увеличь громкость|еще громче)` + what + `( пожалуйста)?`)
var quieter = full(`(сделай|сделать|можно)?` + what + ` ?(потише|тише|убавь|уменьши громкость|еще тише)` + what + `( пожалуйста)?`)
var bluetoothOn = compile(`\b(включи|запусти)\b.*\b(блютуз|bluetooth)\b`)
var bluetoothOff = compile(`\b(выключи|отключи)\b.*\b(блютуз|bluetooth)\b`)
var reset = full(`(давай )?(начнем сначала|новый (диалог|разговор)|забудь (все|наш разговор)|сбрось (диалог|контекст))`)

// Match returns nil when no local command matches. Stop is a non-nil silent result.
func Match(text string) *Result {
	t := norm(text)
	if t == "" {
		return nil
	}
	if stop.MatchString(t) {
		return &Result{Directives: []Directive{}}
	}
	if volume.MatchString(t) {
		if n, ok := level(strings.Fields(t)); ok && n >= 0 && n <= 10 {
			return action("", "sound_set_level", map[string]any{"new_level": n, "new_percent_level": n * 10})
		}
	}
	if louder.MatchString(t) {
		return action("", "sound_louder", nil)
	}
	if quieter.MatchString(t) {
		return action("", "sound_quiter", nil)
	}
	if bluetoothOn.MatchString(t) {
		return action("Включаю блютуз.", "start_bluetooth", nil)
	}
	if bluetoothOff.MatchString(t) {
		return action("Выключаю блютуз.", "stop_bluetooth", nil)
	}
	if reset.MatchString(t) {
		return &Result{Text: "Хорошо, начнём заново.", Directives: []Directive{}, ResetDialog: true}
	}
	return nil
}

const play = `(включи|включай|поставь|запусти|сыграй|играй|давай послушаем|хочу послушать)`
const wave = `(музыку|музычку|что нибудь|что-нибудь|мою волну|волну|мою музыку|любимое|радио)`

var dislike = full(`(поставь )?дизлайк|дизлайкни|убери (эту песню|этот трек)|не надо такое`)
var negativeRating = compile(`\bне (нравится|понравилось|нравятся)\b`)
var like = full(`(поставь )?лайк|лайкни|добавь в (любимые|избранное|понравившиеся|мне нравится)|(классная|крутая|отличная) (песня|музыка|песенка)`)
var positiveRating = compile(`\b(нравится|понравилось|нравятся)\b`)
var musicRules = []struct {
	re     *regexp.Regexp
	action string
}{
	{full(`(дальше|следующий( трек)?|следующую( песню)?|следующая( песня)?|переключи( трек| песню)?|другую( песню)?|пропусти|включи следующ\w+( трек| песню)?|некст)( пожалуйста)?`), "next"},
	{full(`(назад|предыдущ\w+( трек| песн\w+)?|верни( предыдущ\w+)?( трек| песню)?|включи предыдущ\w+( трек| песню)?)( пожалуйста)?`), "prev"},
	{full(`(пауза|на паузу|поставь на паузу|останови( музыку)?|выключи музыку)( пожалуйста)?`), "pause"},
	{full(`(продолжи|продолжай|играй дальше|сними с паузы|возобнови)( музыку)?( пожалуйста)?`), "resume"},
	{full(`(что|кто) (сейчас |это )?(играет|поет|за песня|за трек)|как называется (эта )?(песня|трек)`), "what"},
	{full(play + `( пожалуйста)?( ` + wave + `)?( пожалуйста)?`), "wave"},
	{full(`(давай |ну )?(музыку|музычку|мою волну|волну|музыка)( давай| включи| пожалуйста)?`), "wave"},
}
var search = full(play + `( пожалуйста)?( песню| песни| трек| альбом| группу| исполнителя| плейлист)? (.+)`)
var nonMusic = full(`(свет|блютуз|bluetooth|телевизор|будильник|таймер)\b.*`)

// MatchMusic preserves music-only grammar; callers choose local-command precedence.
func MatchMusic(text string) *MusicResult {
	t := norm(text)
	words := strings.Fields(t)
	if len(words) == 0 {
		return nil
	}
	rating := len(words) <= 6 && !strings.HasSuffix(strings.TrimSpace(text), "?")
	switch words[0] {
	case "что", "кто", "почему", "как", "какая", "какой", "какие":
		rating = false
	}
	for _, w := range words {
		if w == "тебе" {
			rating = false
		}
	}
	if dislike.MatchString(t) || (rating && negativeRating.MatchString(t)) {
		return &MusicResult{Action: "dislike"}
	}
	if like.MatchString(t) || (rating && positiveRating.MatchString(t)) {
		return &MusicResult{Action: "like"}
	}
	for _, rule := range musicRules {
		if rule.re.MatchString(t) {
			return &MusicResult{Action: rule.action}
		}
	}
	if m := search.FindStringSubmatch(t); m != nil {
		q := strings.TrimSpace(strings.TrimSuffix(m[4], " пожалуйста"))
		if q != "" && !nonMusic.MatchString(q) {
			return &MusicResult{Action: "search", Query: q}
		}
	}
	return nil
}
