// SPDX-License-Identifier: GPL-3.0-only
// Package speech owns resident Vosk and Piper models and per-request Silero VAD.
package speech

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/maintainer64/quasard/internal/audio"
)

type PCM = audio.PCM
type Config struct{ VoskModel, VADModel, PiperModel, PiperConfig, EspeakData, LibraryDir string }

var ErrUnavailable = errors.New("native speech unavailable: build Linux with CGO_ENABLED=1 -tags native and install pinned native libraries")

type Engine struct {
	mu      sync.RWMutex
	synth   sync.Mutex
	backend *backend
	cfg     Config
	closed  bool
}

func New(cfg Config) (*Engine, error) {
	b, err := newBackend(cfg)
	if err != nil {
		return nil, err
	}
	return &Engine{backend: b, cfg: cfg}, nil
}

// Transcribe uses a fresh recognizer while keeping the Vosk model resident.
func (e *Engine) Transcribe(ctx context.Context, pcm []int16) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return "", errors.New("speech engine closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return e.backend.transcribe(ctx, pcm)
}

// Synthesize serializes calls into the resident libpiper synthesizer.
func (e *Engine) Synthesize(ctx context.Context, text string) (PCM, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return PCM{}, errors.New("speech engine closed")
	}
	if err := ctx.Err(); err != nil {
		return PCM{}, err
	}
	if strings.TrimSpace(text) == "" {
		return PCM{SampleRate: 22050}, nil
	}
	e.synth.Lock()
	defer e.synth.Unlock()
	return e.backend.synthesize(ctx, text)
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.backend.close()
		e.closed = true
	}
	return nil
}

// NewVAD allocates an independent ONNX CPU session and recurrent state. Close it
// at request end. Engine.Close must follow closing all VAD instances.
func (e *Engine) NewVAD() (*VAD, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, errors.New("speech engine closed")
	}
	b, err := e.backend.vad(e.cfg.VADModel)
	if err != nil {
		return nil, err
	}
	return &VAD{backend: b}, nil
}

type VAD struct {
	mu      sync.Mutex
	backend *vadBackend
	pending []int16
	closed  bool
}

// Feed preserves incomplete windows, context, and recurrent state across calls.
// One probability corresponds to 512 samples (32 ms at 16 kHz).
func (v *VAD) Feed(pcm []int16) ([]float32, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errors.New("VAD closed")
	}
	v.pending = append(v.pending, pcm...)
	var out []float32
	for len(v.pending) >= 512 {
		p, err := v.backend.feed(v.pending[:512])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
		v.pending = v.pending[512:]
	}
	if len(v.pending) == 0 {
		v.pending = nil
	}
	return out, nil
}
func (v *VAD) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.closed {
		v.backend.close()
		v.closed = true
	}
	return nil
}
