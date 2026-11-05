// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnglishVoiceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`[tts]
model='ru.onnx'
model_en='en.onnx'
config_path_en='en.onnx.json'
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTS.Model != "ru.onnx" || cfg.TTS.ModelEN != "en.onnx" || cfg.TTS.ConfigPathEN != "en.onnx.json" {
		t.Fatalf("voice config: %+v", cfg.TTS)
	}
	t.Setenv("ALICE_TTS_MODEL_EN", "other.onnx")
	t.Setenv("ALICE_TTS_CONFIG_PATH_EN", "other.onnx.json")
	cfg, err = Load(path)
	if err != nil || cfg.TTS.ModelEN != "other.onnx" || cfg.TTS.ConfigPathEN != "other.onnx.json" {
		t.Fatalf("voice environment: %+v, %v", cfg.TTS, err)
	}
}

func TestEnglishVoiceConfigRequiresModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[tts]\nconfig_path_en='orphan.json'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "tts.model_en") {
		t.Fatalf("missing English model diagnostic: %v", err)
	}
}
