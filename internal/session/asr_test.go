// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"

	"github.com/maintainer64/quasard/internal/debugaudio"
	"strings"
	"testing"
)

type recognitionSpeech struct{ fakeSpeech }

func (recognitionSpeech) Transcribe(context.Context, []int16) (string, error) {
	return "Алиса, распознанная тестовая фраза", nil
}
func TestRecognitionTextOnlyAtDebug(t *testing.T) {
	original := slog.Default()
	defer slog.SetDefault(original)
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var logs diagnosticBuffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level})))
		c := &Session{svc: &Services{Speech: recognitionSpeech{}}, device: "micro"}
		result := c.transcribeVoice(&voice{ctx: context.Background(), request: "recognition-turn"}, make([]int16, 16000), nil)
		if result != "распознанная тестовая фраза" {
			t.Fatal("wake-word normalization changed")
		}
		data := logs.String()
		if strings.Contains(data, "распознанная тестовая фраза") != (level == slog.LevelDebug) {
			t.Fatal("recognition text not restricted to DEBUG")
		}
		if level == slog.LevelDebug && (!strings.Contains(data, "raw_text=") || !strings.Contains(data, "request_id=recognition-turn") || !strings.Contains(data, "recognition_audio_ms=")) {
			t.Fatal("missing ASR diagnostics")
		}
	}
}

type englishRecognitionSpeech struct{ fakeSpeech }

func (englishRecognitionSpeech) Transcribe(context.Context, []int16) (string, error) {
	return "Alice, what is the weather?", nil
}
func TestEnglishWakeWordNormalization(t *testing.T) {
	c := &Session{svc: &Services{Speech: englishRecognitionSpeech{}}, device: "english"}
	got := c.transcribeVoice(&voice{ctx: context.Background()}, make([]int16, 16000), nil)
	if got != "what is the weather?" {
		t.Fatal("English wake word not removed")
	}
}

func TestRecognitionAudioDownloadMatchesASRInput(t *testing.T) {
	original := slog.Default()
	defer slog.SetDefault(original)
	var logs diagnosticBuffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: &level})))
	captures := debugaudio.New(func() bool { return level.Level() <= slog.LevelDebug })
	c := &Session{svc: &Services{Speech: recognitionSpeech{}, DebugAudio: captures}, device: "station"}
	pcm := make([]int16, 512*50)
	flags := make([]bool, 50)
	flags[5] = true
	want := speechOnly(pcm, flags)
	c.transcribeVoice(&voice{ctx: context.Background(), request: "captured-turn"}, pcm, flags)
	match := regexp.MustCompile(`input_url=(\S+)`).FindStringSubmatch(logs.String())
	if len(match) != 2 {
		t.Fatal("missing download URL")
	}
	path := strings.Trim(match[1], `"`)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		captures.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if get(path).Body.Len() != 44+2*len(pcm) {
		t.Fatal("original audio lost")
	}
	asrPath := strings.Replace(path, "input.wav", "asr.wav", 1)
	if get(asrPath).Body.Len() != 44+2*len(want) || len(want) >= len(pcm) {
		t.Fatal("download not identical to VAD input")
	}
	var m debugaudio.Metadata
	w := get(strings.Replace(path, "input.wav", "metadata.json", 1))
	if json.Unmarshal(w.Body.Bytes(), &m) != nil || m.RequestID != "captured-turn" || m.Text != "распознанная тестовая фраза" || m.ASRStatus != "done" {
		t.Fatal("ASR metadata missing")
	}
	level.Set(slog.LevelInfo)
	if get(path).Code != 404 {
		t.Fatal("capture accessible at INFO")
	}
}
