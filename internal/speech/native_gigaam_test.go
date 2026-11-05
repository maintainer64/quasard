//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/audio"
)

func TestNativeGigaAM(t *testing.T) {
	directory, voices := os.Getenv("QUASARD_GIGAAM_MODEL"), os.Getenv("QUASARD_SMOKE_MODELS")
	if directory == "" || voices == "" {
		t.Skip("set QUASARD_GIGAAM_MODEL and QUASARD_SMOKE_MODELS")
	}
	engine, err := New(Config{ASREngine: "gigaam", ASRLanguage: "ru", ASRThreads: 2,
		GigaAMModel: directory, VoskModel: "/must-not-load-vosk",
		PiperModel:   filepath.Join(voices, "ru_RU-irina-medium.onnx"),
		PiperModelEN: filepath.Join(voices, "en_US-hfc_female-medium.onnx"),
		EspeakData:   "/opt/quasard/share/espeak-ng-data"})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var last []int16
	for _, test := range []struct{ text, word string }{
		{"Привет, Алиса. Какая сейчас температура на улице?", "температур"},
		{"Включи свет в комнате.", "свет"},
		{"Расскажи новости.", "новост"},
	} {
		pcm, err := engine.Synthesize(ctx, test.text)
		if err != nil {
			t.Fatal(err)
		}
		var ogg bytes.Buffer
		encoder, err := audio.NewEncoder(ctx, &ogg)
		if err != nil {
			t.Fatal(err)
		}
		if err := encoder.Add(pcm); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Close(); err != nil {
			t.Fatal(err)
		}
		decoded, err := audio.Decode(ctx, ogg.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		text, err := engine.Transcribe(ctx, decoded.Samples)
		if err != nil || !strings.Contains(strings.ToLower(text), test.word) {
			t.Fatalf("GigaAM recognition: %q %v", text, err)
		}
		t.Logf("audio=%.2fs ASR=%s text=%q", float64(len(decoded.Samples))/16000, time.Since(started), text)
		last = decoded.Samples
	}
	// Exercise all subsampling length residues and repeated Run allocations.
	// Encoder input lengths are int64, but encoded_len is an int32 tensor.
	for repeat := 0; repeat < 12; repeat++ {
		clipped := last[:len(last)-(repeat%4)*80]
		if text, err := engine.Transcribe(ctx, clipped); err != nil || !strings.Contains(strings.ToLower(text), "новост") {
			t.Fatalf("repeated variable-length input: %q %v", text, err)
		}
	}
	interrupted, interrupt := context.WithTimeout(ctx, 20*time.Millisecond)
	started := time.Now()
	_, err = engine.Transcribe(interrupted, last)
	interrupt()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatalf("native cancellation took %s: %v", time.Since(started), err)
	}
	if text, err := engine.Transcribe(ctx, last); err != nil || !strings.Contains(strings.ToLower(text), "новост") {
		t.Fatalf("model unusable after cancellation: %q %v", text, err)
	}
	engine.backend.asrSlot <- struct{}{}
	waiting, stopWaiting := context.WithTimeout(ctx, 20*time.Millisecond)
	_, err = engine.Transcribe(waiting, last)
	stopWaiting()
	<-engine.backend.asrSlot
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request did not cancel: %v", err)
	}
	if text, err := engine.Transcribe(ctx, make([]int16, 200)); err != nil || text != "" {
		t.Fatalf("short input: %q %v", text, err)
	}
	if _, err := engine.Transcribe(ctx, make([]int16, 25*16000+1)); err == nil {
		t.Fatal("oversized GigaAM input was silently truncated")
	}
	if peak, err := os.ReadFile("/sys/fs/cgroup/memory.peak"); err == nil {
		t.Logf("cgroup memory peak bytes: %s", strings.TrimSpace(string(peak)))
	}
}
