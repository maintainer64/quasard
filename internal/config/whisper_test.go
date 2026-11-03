// SPDX-License-Identifier: GPL-3.0-only
package config

import "testing"

func TestWhisperAutoConfig(t *testing.T) {
	cfg, err := loadDeviceConfig(t, `[asr]
engine="whisper"
model_path="/models/ggml-base-q5_1.bin"
language="auto"
threads=2
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ASR.Engine != "whisper" || cfg.ASR.Language != "auto" || cfg.ASR.Threads != 2 {
		t.Fatal("Whisper settings not loaded")
	}
	for _, bad := range []string{"threads=0", "threads=9", `language="invalid"`} {
		_, err := loadDeviceConfig(t, "[asr]\nengine=\"whisper\"\n"+bad)
		if err == nil {
			t.Fatal("invalid Whisper setting accepted")
		}
	}
}
