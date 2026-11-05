// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"log/slog"
	"strings"
	"time"

	"github.com/maintainer64/quasard/internal/debugaudio"
	"github.com/maintainer64/quasard/internal/speech"
)

func (c *Session) transcribeVoice(v *voice, pcm []int16, flags []bool) string {
	input := speechOnly(pcm, flags)
	if c.svc.Config.ASR.Engine == "gigaam" {
		// Preserve the contiguous waveform validated against original GigaAM.
		// VAD ends the utterance but does not splice its internal timeline.
		input = pcm
	}
	logger := slog.Default().With("device", c.device, "request_id", v.request)
	logger.DebugContext(v.ctx, "ASR started", "engine", c.svc.Config.ASR.Engine, "language", c.svc.Config.ASR.Language, "audio_ms", len(pcm)*1000/16000, "recognition_audio_ms", len(input)*1000/16000)
	captureID := ""
	if c.svc.DebugAudio != nil {
		var captureErr error
		captureID, captureErr = c.svc.DebugAudio.Capture(debugaudio.Clip{DeviceID: c.device, RequestID: v.request,
			Engine: c.svc.Config.ASR.Engine, Language: c.svc.Config.ASR.Language, Input: pcm, Recognition: input})
		if captureErr != nil {
			logger.WarnContext(v.ctx, "debug audio capture failed", "error", captureErr)
		}
		if captureID != "" {
			prefix := debugaudio.Prefix + captureID + "/"
			logger.DebugContext(v.ctx, "debug audio captured", "input_url", prefix+"input.wav", "asr_url", prefix+"asr.wav", "metadata_url", prefix+"metadata.json")
		}
	}
	started := time.Now()
	raw, err := c.svc.Speech.Transcribe(speech.WithLogger(v.ctx, logger), input)
	text := strings.TrimSpace(wake.ReplaceAllString(strings.TrimSpace(raw), ""))
	if captureID != "" {
		c.svc.DebugAudio.Finish(captureID, raw, text, err != nil, time.Since(started).Milliseconds())
	}
	// Recognition text is deliberately restricted to the opt-in DEBUG level.
	logger.DebugContext(v.ctx, "ASR finished", "elapsed_ms", time.Since(started).Milliseconds(), "recognized", text != "", "failed", err != nil, "raw_text", raw, "text", text)
	if err != nil {
		logger.WarnContext(v.ctx, "ASR failed", "error", err)
	}
	return text
}
