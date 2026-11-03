// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"log/slog"
	"strings"
	"time"
)

func (c *Session) transcribeVoice(v *voice, pcm []int16, flags []bool) string {
	input := speechOnly(pcm, flags)
	started := time.Now()
	logger := slog.Default().With("device", c.device, "request_id", v.request)
	logger.DebugContext(v.ctx, "ASR started", "engine", c.svc.Config.ASR.Engine, "language", c.svc.Config.ASR.Language, "audio_ms", len(pcm)*1000/16000, "recognition_audio_ms", len(input)*1000/16000)
	raw, err := c.svc.Speech.Transcribe(v.ctx, input)
	text := strings.TrimSpace(wake.ReplaceAllString(strings.TrimSpace(raw), ""))
	// Recognition text is deliberately restricted to the opt-in DEBUG level.
	logger.DebugContext(v.ctx, "ASR finished", "elapsed_ms", time.Since(started).Milliseconds(), "recognized", text != "", "failed", err != nil, "raw_text", raw, "text", text)
	if err != nil {
		logger.WarnContext(v.ctx, "ASR failed", "error", err)
	}
	return text
}
