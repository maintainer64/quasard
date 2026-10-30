//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maintainer64/quasard/internal/audio"
)

func TestNativeSmoke(t *testing.T) {
	dir := os.Getenv("QUASARD_SMOKE_MODELS")
	if dir == "" {
		t.Skip("set QUASARD_SMOKE_MODELS for real-model native smoke")
	}
	vadModel := os.Getenv("QUASARD_SMOKE_VAD")
	if vadModel == "" {
		vadModel = filepath.Join(dir, "silero_vad.onnx")
	}
	e, err := New(Config{VoskModel: filepath.Join(dir, "vosk-model-small-ru-0.22"), VADModel: vadModel, PiperModel: filepath.Join(dir, "ru_RU-dmitri-medium.onnx"), EspeakData: "/opt/quasard/share/espeak-ng-data"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	short, err := e.Synthesize(ctx, "Привет.")
	if err != nil {
		t.Fatal(err)
	}
	if len(short.Samples) == 0 || short.SampleRate != 22050 {
		t.Fatalf("single-sentence final Piper chunk lost: %d samples, %d Hz", len(short.Samples), short.SampleRate)
	}
	p, err := e.Synthesize(ctx, "Привет мир. Проверка синтеза речи.")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Samples) < p.SampleRate || p.SampleRate != 22050 {
		t.Fatalf("invalid synthesis: %d samples, %d Hz", len(p.Samples), p.SampleRate)
	}
	var nonzero bool
	for _, s := range p.Samples {
		if s != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("Piper returned silence")
	}
	var ogg bytes.Buffer
	enc, err := audio.NewEncoder(ctx, &ogg)
	if err != nil {
		t.Fatal(err)
	}
	if err = enc.Add(p); err != nil {
		t.Fatal(err)
	}
	if err = enc.Gap(); err != nil {
		t.Fatal(err)
	}
	if err = enc.Add(p); err != nil {
		t.Fatal(err)
	}
	if err = enc.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := audio.Decode(ctx, ogg.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	text, err := e.Transcribe(ctx, decoded.Samples)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("Vosk did not recognize synthesized Russian speech")
	}
	t.Logf("Piper %d Hz, %d samples; Vosk: %s", p.SampleRate, len(p.Samples), text)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.NewVAD()
			if err != nil {
				t.Error(err)
				return
			}
			defer v.Close()
			probs, err := v.Feed(decoded.Samples)
			if err != nil {
				t.Error(err)
				return
			}
			if len(probs) != len(decoded.Samples)/512 {
				t.Errorf("VAD windows %d", len(probs))
			}
			speech := false
			for _, p := range probs {
				if math.IsNaN(float64(p)) || p < 0 || p > 1 {
					t.Errorf("invalid probability %f", p)
				}
				speech = speech || p > 0.5
			}
			if !speech {
				t.Error("Silero did not detect synthesized speech")
			}
		}()
	}
	wg.Wait()
}
