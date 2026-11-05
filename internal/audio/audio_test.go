// SPDX-License-Identifier: GPL-3.0-only
package audio

import (
	"bytes"
	"context"
	"math"
	"os/exec"
	"testing"
	"time"
)

func TestContinuousOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	samples := make([]int16, 22050)
	for i := range samples {
		samples[i] = int16(1000 * math.Sin(2*math.Pi*440*float64(i)/22050))
	}
	var encoded bytes.Buffer
	enc, err := NewEncoder(ctx, &encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = enc.Add(PCM{Samples: samples, SampleRate: 22050}); err != nil {
		t.Fatal(err)
	}
	if err = enc.Gap(); err != nil {
		t.Fatal(err)
	}
	if err = enc.Add(PCM{Samples: samples, SampleRate: 22050}); err != nil {
		t.Fatal(err)
	}
	if err = enc.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(encoded.Bytes(), []byte("OpusHead")) != 1 {
		t.Fatal("expected a single continuous Ogg stream")
	}
	pcm, err := Decode(ctx, encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	duration := float64(len(pcm.Samples)) / float64(pcm.SampleRate)
	if duration < 2.10 || duration > 2.15 {
		t.Fatalf("duration %f", duration)
	}
}
func TestCancelledCodec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	enc, err := NewEncoder(ctx, &output)
	if err == nil {
		_ = enc.Close()
		t.Fatal("started encoder with cancelled context")
	}
}
