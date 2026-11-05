// SPDX-License-Identifier: GPL-3.0-only

package intents

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		text, action, say string
		reset             bool
		level             int
	}{
		{text: "СТОП!!!"}, {text: "Алиса, помолчи пожалуйста"}, {text: "слушай Алиса, тихо"},
		{"громкость ноль", "sound_set_level", "", false, 0},
		{"уровень громкости на две", "sound_set_level", "", false, 2},
		{"поставь громкость максимум", "sound_set_level", "", false, 10},
		{"сделай громкость минимум", "sound_set_level", "", false, 1},
		{"громкость ７", "sound_set_level", "", false, 7},
		{"громкость ٣", "sound_set_level", "", false, 3},
		{"громкость 4 и десять", "sound_set_level", "", false, 4},
		{"громче", "sound_louder", "", false, 0},
		{"сделай музыку погромче пожалуйста", "sound_louder", "", false, 0},
		{"сделать звук еще громче трек", "sound_louder", "", false, 0},
		{"прибавь", "sound_louder", "", false, 0},
		{"увеличь громкость", "sound_louder", "", false, 0},
		{"тише", "sound_quiter", "", false, 0},
		{"можно музыку потише пожалуйста", "sound_quiter", "", false, 0},
		{"ещё тише", "sound_quiter", "", false, 0},
		{"уменьши громкость", "sound_quiter", "", false, 0},
		{"Алиса, включи блютуз", "start_bluetooth", "Включаю блютуз.", false, 0},
		{"пожалуйста запусти мой Bluetooth сейчас", "start_bluetooth", "Включаю блютуз.", false, 0},
		{"отключи блютуз пожалуйста", "stop_bluetooth", "Выключаю блютуз.", false, 0},
		{"выключи Bluetooth", "stop_bluetooth", "Выключаю блютуз.", false, 0},
		{"давай начнём сначала", "", "Хорошо, начнём заново.", true, 0},
		{"новый разговор", "", "Хорошо, начнём заново.", true, 0},
		{"забудь всё", "", "Хорошо, начнём заново.", true, 0},
		{"забудь наш разговор", "", "Хорошо, начнём заново.", true, 0},
		{"сбрось контекст", "", "Хорошо, начнём заново.", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := Match(tt.text)
			if got == nil {
				t.Fatal("no match")
			}
			if got.Text != tt.say || got.ResetDialog != tt.reset {
				t.Fatalf("got %+v", got)
			}
			if tt.action == "" {
				if len(got.Directives) != 0 || got.Directives == nil {
					t.Fatalf("silent directives: %+v", got)
				}
				return
			}
			want := Directive{Name: tt.action, Type: "client_action", Payload: map[string]any{}}
			if tt.action == "sound_set_level" {
				want.Payload = map[string]any{"new_level": tt.level, "new_percent_level": tt.level * 10}
			}
			if !reflect.DeepEqual(got.Directives, []Directive{want}) {
				t.Fatalf("got %+v; want %+v", got.Directives, want)
			}
		})
	}
	for _, text := range []string{"", "!!!", "Алиса", "слушай Алиса", "не стоп", "стоп пожалуйста еще", "пожалуйста стоп", "громкость 11", "громкость 11 потом 3", "громкость сорок", "громкость_ 4", "тихо музыку", "сделаймузыку громче", "включить блютуз", "включи блютузик", "включи_блютуз", "новый диалог пожалуйста", "расскажи про новый диалог"} {
		if got := Match(text); got != nil {
			t.Errorf("unexpected match %q: %+v", text, got)
		}
	}
}

func TestMusic(t *testing.T) {
	tests := []struct{ text, action, query string }{
		{"мне не нравится", "dislike", ""}, {"эта песня мне не понравилось", "dislike", ""}, {"поставь дизлайк", "dislike", ""}, {"дизлайкни", "dislike", ""}, {"убери этот трек", "dislike", ""}, {"не надо такое", "dislike", ""},
		{"мне нравится", "like", ""}, {"поставь лайк", "like", ""}, {"лайкни", "like", ""}, {"добавь в понравившиеся", "like", ""}, {"классная песенка", "like", ""},
		{"дальше пожалуйста", "next", ""}, {"включи следующую песню", "next", ""}, {"следующий трек", "next", ""}, {"следующая песня", "next", ""},
		{"назад", "prev", ""}, {"предыдущая песня", "prev", ""}, {"верни предыдущий трек пожалуйста", "prev", ""}, {"верни песню", "prev", ""},
		{"поставь на паузу", "pause", ""}, {"останови музыку пожалуйста", "pause", ""}, {"выключи музыку", "pause", ""},
		{"продолжи музыку пожалуйста", "resume", ""}, {"сними с паузы", "resume", ""}, {"играй дальше", "resume", ""},
		{"кто это поёт", "what", ""}, {"что сейчас играет", "what", ""}, {"что за песня", "what", ""}, {"как называется эта песня", "what", ""},
		{"включи", "wave", ""}, {"включи пожалуйста музыку пожалуйста", "wave", ""}, {"давай послушаем что-нибудь", "wave", ""}, {"мою волну давай", "wave", ""}, {"ну музыку включи", "wave", ""}, {"музыка", "wave", ""},
		{"Алиса, поставь песни КИНО!", "search", "кино"}, {"поставь пожалуйста альбом Земфиры пожалуйста", "search", "земфиры"}, {"хочу послушать группу The Cure", "search", "the cure"},
		{"включи песню", "search", "песню"}, {"включи светлячок", "search", "светлячок"}, {"включи таймеры", "search", "таймеры"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := MatchMusic(tt.text)
			want := &MusicResult{Action: tt.action, Query: tt.query}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v; want %+v", got, want)
			}
		})
	}
	for _, text := range []string{"", "Алиса", "мне нравится?", "что тебе нравится", "почему мне не нравится", "эта песня тебе нравится", "я думаю что эта песня мне нравится", "включи свет", "включи блютуз пожалуйста", "включи bluetooth", "включи телевизор", "включи будильник", "включи таймер на три минуты", "переключи музыку", "расскажи про музыку", "лайк пожалуйста", "стоп", "как называется эта музыка"} {
		if got := MatchMusic(text); got != nil {
			t.Errorf("unexpected %q: %+v", text, got)
		}
	}
}

func TestNumberWordsAndGrammar(t *testing.T) {
	for word, n := range nums {
		got := Match("громкость " + word)
		if got == nil || got.Directives[0].Payload["new_level"] != n {
			t.Errorf("word %q: %+v", word, got)
		}
	}
	for _, verb := range []string{"включи", "включай", "поставь", "запусти", "сыграй", "играй", "давай послушаем", "хочу послушать"} {
		for _, wave := range []string{"", "музыку", "музычку", "что нибудь", "что-нибудь", "мою волну", "волну", "мою музыку", "любимое", "радио"} {
			text := verb
			if wave != "" {
				text += " " + wave
			}
			got := MatchMusic(text)
			if got == nil || got.Action != "wave" {
				t.Errorf("wave %q: %+v", text, got)
			}
		}
	}
}

func TestDirectiveJSON(t *testing.T) {
	got, err := json.Marshal(Match("тише"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"text":"","directives":[{"name":"sound_quiter","payload":{},"type":"client_action"}],"reset_dialog":false}`
	if string(got) != want {
		t.Fatalf("%s", got)
	}
}
