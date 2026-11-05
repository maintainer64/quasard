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
	"strings"
	"unsafe"
)

// One resident context is serialized to bound working memory and CPU threads.
func (b *backend) transcribeWhisper(ctx context.Context, pcm []int16) (string, error) {
	if len(pcm) == 0 {
		return "", ctx.Err()
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
	C.qs_whisper_reset(b.whisper)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			C.qs_whisper_cancel(b.whisper)
		case <-done:
		}
	}()
	defer func() { close(done); <-stopped }()
	var result C.qs_whisper_result
	status := C.qs_whisper_transcribe(b.whisper, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)), &result)
	if result.text != nil {
		defer C.free(unsafe.Pointer(result.text))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("Whisper transcription failed (status %d)", int(status))
	}
	logger := requestLogger(ctx)
	if result.automatic != 0 {
		logger.DebugContext(ctx, "Whisper language selected", "language", C.GoString(result.language), "mode", "auto_ru_en", "ru_probability", float32(result.ru_probability), "en_probability", float32(result.en_probability))
	} else {
		logger.DebugContext(ctx, "Whisper language selected", "language", C.GoString(result.language), "mode", "fixed")
	}
	return strings.TrimSpace(C.GoString(result.text)), nil
}

func requestLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
