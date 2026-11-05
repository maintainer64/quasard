//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

/*
#include "bridge.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"unsafe"
)

type gigaBackend struct {
	ptr   *C.qs_gigaam
	model *gigaManifest
}

func newGigaBackend(directory string, threads int) (*gigaBackend, error) {
	manifest, err := loadGigaManifest(directory)
	if err != nil {
		return nil, err
	}
	enc, releaseEnc := cstr(filepath.Join(directory, "encoder.onnx"))
	defer releaseEnc()
	dec, releaseDec := cstr(filepath.Join(directory, "decoder.onnx"))
	defer releaseDec()
	joint, releaseJoint := cstr(filepath.Join(directory, "joiner.onnx"))
	defer releaseJoint()
	var failure *C.char
	ptr := C.qs_gigaam_new(enc, dec, joint, (*C.float)(unsafe.Pointer(&manifest.MelFilters[0])), C.int(threads), &failure)
	if ptr == nil {
		return nil, nativeError(failure)
	}
	// Validate all graph interfaces and warm the model before readiness.
	silence := make([]int16, 16000)
	var warm C.qs_gigaam_result
	status := C.qs_gigaam_transcribe(ptr, (*C.int16_t)(unsafe.Pointer(&silence[0])), C.int(len(silence)), &warm, &failure)
	if warm.tokens != nil {
		C.free(unsafe.Pointer(warm.tokens))
	}
	if status != 0 {
		C.qs_gigaam_free(ptr)
		return nil, nativeError(failure)
	}
	return &gigaBackend{ptr: ptr, model: manifest}, nil
}

func (b *backend) transcribeGigaAM(ctx context.Context, pcm []int16) (string, error) {
	if len(pcm) == 0 {
		return "", ctx.Err()
	}
	if len(pcm) > 25*16000 {
		return "", fmt.Errorf("GigaAM utterance exceeds 25 seconds")
	}
	select {
	case b.asrSlot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-b.asrSlot }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var failure *C.char
	if C.qs_gigaam_reset(b.gigaam.ptr, &failure) != 0 {
		return "", nativeError(failure)
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			C.qs_gigaam_cancel(b.gigaam.ptr)
		case <-done:
		}
	}()
	defer func() { close(done); <-stopped }()
	var result C.qs_gigaam_result
	status := C.qs_gigaam_transcribe(b.gigaam.ptr, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)), &result, &failure)
	if result.tokens != nil {
		defer C.free(unsafe.Pointer(result.tokens))
	}
	if failure != nil {
		defer C.free(unsafe.Pointer(failure))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("GigaAM inference failed: %s", C.GoString(failure))
	}
	tokens := unsafe.Slice((*int32)(unsafe.Pointer(result.tokens)), int(result.count))
	text, err := b.gigaam.model.decode(tokens)
	requestLogger(ctx).DebugContext(ctx, "GigaAM inference finished", "language", "ru",
		"frontend_ms", float64(result.frontend_ms), "encoder_ms", float64(result.encoder_ms),
		"decoder_ms", float64(result.decoder_ms), "frames", int(result.frames), "tokens", len(tokens))
	return text, err
}

func requestLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
