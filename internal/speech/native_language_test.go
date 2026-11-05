//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNativeBilingualSmoke(t *testing.T) {
	dir := os.Getenv("QUASARD_SMOKE_MODELS")
	if dir == "" {
		t.Skip("set QUASARD_SMOKE_MODELS for native voice smoke")
	}
	ru := filepath.Join(dir, "ru_RU-irina-medium.onnx")
	en := filepath.Join(dir, "en_US-hfc_female-medium.onnx")
	for _, path := range []string{ru, en} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Skip("download Irina and hfc_female for bilingual smoke")
		}
	}
	cfg := Config{GigaAMModel: os.Getenv("QUASARD_GIGAAM_MODEL"), PiperModel: ru, PiperModelEN: en, EspeakData: "/opt/quasard/share/espeak-ng-data"}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, text := range []string{"Привет, это Ирина.", "Hello, this is the English voice.", "Снова русский голос."} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			pcm, err := e.Synthesize(context.Background(), text)
			if err != nil {
				t.Error(err)
				return
			}
			if len(pcm.Samples) == 0 || pcm.SampleRate != 22050 {
				t.Errorf("empty or invalid speech for %q", text)
			}
			nonzero := false
			for _, s := range pcm.Samples {
				nonzero = nonzero || s != 0
			}
			if !nonzero {
				t.Errorf("silent output for %q", text)
			}
		}(text)
	}
	wg.Wait()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Synthesize(context.Background(), "Closed"); err == nil {
		t.Fatal("closed voice engine accepted synthesis")
	}
	cfg.PiperModelEN = filepath.Join(dir, "missing-english.onnx")
	if broken, err := New(cfg); err == nil {
		broken.Close()
		t.Fatal("invalid English model silently fell back to the default voice")
	}
}
