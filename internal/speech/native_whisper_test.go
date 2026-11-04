//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/audio"
)

func TestNativeWhisperAutoRUEN(t *testing.T) {
	dir := os.Getenv("QUASARD_SMOKE_MODELS")
	if dir == "" {
		t.Skip("set QUASARD_SMOKE_MODELS")
	}
	model := filepath.Join(dir, "ggml-base-q5_1.bin")
	if _, err := os.Stat(model); os.IsNotExist(err) {
		t.Skip("download multilingual Whisper base-q5_1")
	}
	engine, err := New(Config{ASREngine: "whisper", ASRLanguage: "auto", ASRThreads: 2, WhisperModel: model, VoskModel: "/must-not-load-vosk", PiperModel: filepath.Join(dir, "ru_RU-irina-medium.onnx"), PiperModelEN: filepath.Join(dir, "en_US-hfc_female-medium.onnx"), EspeakData: "/opt/quasard/share/espeak-ng-data"})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var last []int16
	for _, tc := range []struct{ speech, word, language string }{
		{"Привет, Алиса. Расскажи какая сегодня погода.", "погод", "ru"},
		{"Hello Alice, what is the weather like today?", "weather", "en"},
		{"Алиса, какая сегодня погода за окном?", "погод", "ru"},
	} {
		pcm, err := engine.Synthesize(ctx, tc.speech)
		if err != nil {
			t.Fatal(err)
		}
		var ogg bytes.Buffer
		enc, err := audio.NewEncoder(ctx, &ogg)
		if err != nil {
			t.Fatal(err)
		}
		if err := enc.Add(pcm); err != nil {
			t.Fatal(err)
		}
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
		decoded, err := audio.Decode(ctx, ogg.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		var diagnostics bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug})).With("device", "smoke", "request_id", tc.language)
		text, err := engine.Transcribe(WithLogger(ctx, logger), decoded.Samples)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("audio=%.2fs asr=%s transcript=%q", float64(len(decoded.Samples))/16000, time.Since(started), text)
		if !strings.Contains(strings.ToLower(text), tc.word) {
			t.Fatalf("automatic RU/EN recognition failed: %q", text)
		}
		if log := diagnostics.String(); !strings.Contains(log, "language="+tc.language+" mode=auto_ru_en") || !strings.Contains(log, "device=smoke") || !strings.Contains(log, "ru_probability=") || !strings.Contains(log, "en_probability=") {
			t.Fatalf("missing or incorrect per-utterance language diagnostics: %s", log)
		}
		last = decoded.Samples
	}
	// Abort native computation and verify the same resident model is reusable.
	aborted, abort := context.WithTimeout(ctx, 20*time.Millisecond)
	defer abort()
	started := time.Now()
	_, err = engine.Transcribe(aborted, last)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native cancellation: %v", err)
	}
	t.Logf("native cancellation=%s", time.Since(started))
	if time.Since(started) > 3*time.Second {
		t.Fatal("native cancellation took too long")
	}
	if text, err := engine.Transcribe(ctx, last); err != nil || !strings.Contains(strings.ToLower(text), "погод") {
		t.Fatalf("model unusable after cancellation: %q %v", text, err)
	}
	if peak, err := os.ReadFile("/sys/fs/cgroup/memory.peak"); err == nil {
		t.Logf("cgroup memory peak bytes: %s", strings.TrimSpace(string(peak)))
	}
}
