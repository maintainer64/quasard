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
	"strings"
	"unsafe"
)

// One resident context is serialized to bound working memory and CPU threads.
func (b *backend) transcribeWhisper(ctx context.Context, pcm []int16) (string, error) {
	if len(pcm) == 0 {
		return "", ctx.Err()
	}
	select {
	case b.whisperSlot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-b.whisperSlot }()
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
	var text *C.char
	status := C.qs_whisper_transcribe(b.whisper, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)), &text)
	if text != nil {
		defer C.free(unsafe.Pointer(text))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("Whisper transcription failed (status %d)", int(status))
	}
	return strings.TrimSpace(C.GoString(text)), nil
}
