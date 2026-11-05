//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

/*
#cgo CFLAGS: -I/opt/quasard/include
#cgo CXXFLAGS: -I/opt/quasard/include
#cgo LDFLAGS: -L/opt/quasard/lib -Wl,-rpath,/opt/quasard/lib -lpiper -lonnxruntime -lstdc++ -lm -ldl
#include "bridge.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"fmt"
	"math"
	"unsafe"
)

type backend struct {
	ptr     *C.qs_engine
	gigaam  *gigaBackend
	asrSlot chan struct{}
}
type vadBackend struct{ ptr *C.qs_vad }

func cstr(s string) (*C.char, func()) {
	p := C.CString(s)
	return p, func() { C.free(unsafe.Pointer(p)) }
}
func nativeError(p *C.char) error {
	if p == nil {
		return fmt.Errorf("native speech operation failed")
	}
	defer C.free(unsafe.Pointer(p))
	return fmt.Errorf("native speech: %s", C.GoString(p))
}
func newBackend(cfg Config) (*backend, error) {
	if cfg.ASREngine != "" && cfg.ASREngine != "gigaam" {
		return nil, fmt.Errorf("unsupported ASR engine: only gigaam is available")
	}
	if cfg.ASRThreads < 0 || cfg.ASRThreads > 8 {
		return nil, fmt.Errorf("ASR threads must be between 1 and 8")
	}
	if cfg.GigaAMModel == "" || (cfg.ASRLanguage != "" && cfg.ASRLanguage != "ru") {
		return nil, fmt.Errorf("GigaAM requires a model directory and Russian language")
	}
	v, g := cstr(cfg.PiperModel)
	defer g()
	config := cfg.PiperConfig
	if config == "" {
		config = cfg.PiperModel + ".json"
	}
	c, h := cstr(config)
	defer h()
	e, k := cstr(cfg.EspeakData)
	defer k()
	var err *C.char
	p := C.qs_new(v, c, e, &err)
	if p == nil {
		return nil, nativeError(err)
	}
	if cfg.PiperModelEN != "" {
		voiceEN, freeVoiceEN := cstr(cfg.PiperModelEN)
		defer freeVoiceEN()
		configEN := cfg.PiperConfigEN
		if configEN == "" {
			configEN = cfg.PiperModelEN + ".json"
		}
		configPathEN, freeConfigEN := cstr(configEN)
		defer freeConfigEN()
		if C.qs_add_english_voice(p, voiceEN, configPathEN, e, &err) != 0 {
			C.qs_free(p)
			return nil, nativeError(err)
		}
	}
	b := &backend{ptr: p, asrSlot: make(chan struct{}, 1)}
	threads := cfg.ASRThreads
	if threads == 0 {
		threads = 2
	}
	var failure error
	b.gigaam, failure = newGigaBackend(cfg.GigaAMModel, threads)
	if failure != nil {
		C.qs_free(p)
		return nil, failure
	}
	return b, nil
}
func (b *backend) close() {
	if b.gigaam != nil {
		C.qs_gigaam_free(b.gigaam.ptr)
	}
	C.qs_free(b.ptr)
}
func (b *backend) transcribe(ctx context.Context, pcm []int16) (string, error) {
	return b.transcribeGigaAM(ctx, pcm)
}
func (b *backend) synthesize(ctx context.Context, text string, english bool) (PCM, error) {
	t, f := cstr(text)
	defer f()
	useEnglish := C.int(0)
	if english {
		useEnglish = 1
	}
	if C.qs_synth_start(b.ptr, t, useEnglish) != 0 {
		return PCM{}, fmt.Errorf("Piper synthesize start failed")
	}
	var pcm PCM
	for {
		if err := ctx.Err(); err != nil {
			return PCM{}, err
		}
		var samples *C.float
		var n C.size_t
		var rate C.int
		status := C.qs_synth_next(b.ptr, &samples, &n, &rate)
		if status != 0 && status != 1 {
			return PCM{}, fmt.Errorf("Piper synthesize next failed: %d", status)
		}
		if rate <= 0 {
			return PCM{}, fmt.Errorf("Piper returned invalid sample rate")
		}
		if pcm.SampleRate != 0 && pcm.SampleRate != int(rate) {
			return PCM{}, fmt.Errorf("Piper sample rate changed")
		}
		pcm.SampleRate = int(rate)
		for _, s := range unsafe.Slice(samples, int(n)) {
			v := math.Round(float64(s) * 32768)
			v = math.Max(-32768, math.Min(32767, v))
			pcm.Samples = append(pcm.Samples, int16(v))
		}
		// PIPER_DONE still carries the final audio chunk.
		if status == 1 {
			return pcm, nil
		}
	}
}
func (b *backend) vad(path string) (*vadBackend, error) {
	p, f := cstr(path)
	defer f()
	var err *C.char
	v := C.qs_vad_new(p, &err)
	if v == nil {
		return nil, nativeError(err)
	}
	return &vadBackend{v}, nil
}
func (v *vadBackend) feed(samples []int16) (float32, error) {
	var p C.float
	var err *C.char
	if C.qs_vad_feed(v.ptr, (*C.int16_t)(unsafe.Pointer(&samples[0])), &p, &err) != 0 {
		return 0, nativeError(err)
	}
	return float32(p), nil
}
func (v *vadBackend) close() { C.qs_vad_free(v.ptr) }
