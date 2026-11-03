// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"context"
	"log/slog"
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
