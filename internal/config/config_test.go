// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Server.Host != "0.0.0.0" || c.Server.HTTPSPort != 443 || c.Server.HTTPPort != 80 || c.Server.LogTraffic {
		t.Fatalf("server defaults: %+v", c.Server)
	}
	if c.ASR.Engine != "vosk" || c.ASR.Model != "vosk-model-small-ru-0.22" || c.ASR.Device != "cpu" || c.ASR.Language != "ru" || c.TTS.Engine != "piper" {
		t.Fatalf("speech defaults: %+v %+v", c.ASR, c.TTS)
	}
	if c.Device.UID != 100000001 || c.Device.Name != "Яндекс Станция" || c.Device.TimezoneOffsetSec != 10800 || c.LLM.Model != "qwen/qwen3.8-27b" || c.LLM.TimeoutS != 60 || !c.Music.Enabled {
		t.Fatalf("defaults: %+v", c)
	}
	c.Root = t.TempDir()
	if c.Resolve("models/test") != filepath.Join(c.Root, "models/test") {
		t.Fatal("relative path not rooted")
	}
	abs := filepath.Join(t.TempDir(), "data")
	c.Server.DataDir = abs
	if c.DataPath() != abs {
		t.Fatal("absolute path changed")
	}
}
func TestLoadTOMLBOMAndEnvironment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	data := "\xef\xbb\xbf[server]\nhttp_port=8080\nlog_traffic=true\n[asr]\nvad_threshold=0.75\n[llm]\nmax_tokens=42\nextra_body={foo='bar'}\n[device]\nname='Тест'\n"
	if e := os.WriteFile(p, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ALICE_CONFIG", p)
	t.Setenv("ALICE_SERVER_HTTP_PORT", "9090")
	t.Setenv("ALICE_SERVER_LOG_TRAFFIC", "off")
	t.Setenv("ALICE_LLM_EXTRA_BODY", `{"reasoning_effort":"low","n":2}`)
	t.Setenv("ALICE_ASR_VAD_THRESHOLD", "0.25")
	t.Setenv("ALICE_LLM_API_KEY", "")
	c, e := Load("")
	if e != nil {
		t.Fatal(e)
	}
	if c.Server.HTTPPort != 9090 || c.Server.LogTraffic || c.ASR.VADThreshold != 0.25 || c.LLM.MaxTokens != 42 || c.Device.Name != "Тест" || c.LLM.ExtraBody["reasoning_effort"] != "low" {
		t.Fatalf("load: %+v", c)
	}
	if _, ok := c.LLM.ExtraBody["foo"]; ok {
		t.Fatal("environment map must replace file map")
	}
}
func TestTOMLMapReplacesDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if e := os.WriteFile(p, []byte("[llm]\nextra_body={foo='bar'}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if len(c.LLM.ExtraBody) != 1 || c.LLM.ExtraBody["foo"] != "bar" {
		t.Fatalf("TOML dictionary merged defaults: %+v", c.LLM.ExtraBody)
	}
}
func TestLoadErrorsAndMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.toml")
	if _, e := Load(p); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("missing explicit config must fail: %v", e)
	}
	if e := os.WriteFile(p, []byte("[server\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p); e == nil {
		t.Fatal("accepted malformed TOML")
	}
	if e := os.WriteFile(p, nil, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ALICE_SERVER_HTTP_PORT", "bad")
	if _, e := Load(p); e == nil {
		t.Fatal("accepted invalid environment int")
	}
}

func TestConfigPathPriority(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	write := func(name, value string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("[device]\nname='"+value+"'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("config.toml", "default file")
	envPath := write("env.toml", "environment file")
	explicit := write("explicit.toml", "explicit file")
	t.Setenv("ALICE_CONFIG", envPath)
	for _, tc := range []struct{ path, want string }{{explicit, "explicit file"}, {"", "environment file"}} {
		c, err := Load(tc.path)
		if err != nil || c.Device.Name != tc.want {
			t.Fatalf("Load(%q): name=%q err=%v", tc.path, c.Device.Name, err)
		}
	}
	t.Setenv("ALICE_CONFIG", "")
	c, err := Load("")
	if err != nil || c.Device.Name != "default file" {
		t.Fatalf("default file: %+v %v", c.Device, err)
	}
	if err := os.Remove(filepath.Join(dir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err != nil {
		t.Fatalf("absent implicit config should use defaults: %v", err)
	}
	t.Setenv("ALICE_CONFIG", filepath.Join(dir, "missing.toml"))
	if _, err := Load(""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing environment config must fail: %v", err)
	}
	c, err = Load(explicit)
	if err != nil || c.Device.Name != "explicit file" {
		t.Fatalf("explicit must override missing environment path: %v", err)
	}
}

func TestRejectLegacySpeech(t *testing.T) {
	for _, tc := range []struct{ name, data, env, value, want string }{
		{"gigaam", "[asr]\nengine='gigaam'", "", "", "asr.engine"},
		{"f5", "[tts]\nengine='f5'", "", "", "tts.engine"},
		{"edge", "[tts]\nengine='edge'", "", "", "tts.engine"},
		{"remote asr", "[asr]\nengine='openai'", "", "", "asr.engine"},
		{"remote tts", "[tts]\nengine='openai'", "", "", "tts.engine"},
		{"cuda", "[asr]\ndevice='cuda'", "", "", "CPU-only"},
		{"auto", "[asr]\ndevice='auto'", "", "", "CPU-only"},
		{"gigaam model", "[asr]\nmodel='v3_e2e_rnnt'", "", "", "GigaAM"},
		{"f5 option", "[tts]\nf5_url='http://localhost:8777/v1'", "", "", "tts.f5_url"},
		{"empty f5 option", "[tts]\nf5_voice_id=''", "", "", "tts.f5_voice_id"},
		{"voice option", "[tts]\nvoice='ru-RU-SvetlanaNeural'", "", "", "tts.voice"},
		{"remote option", "[asr]\nbase_url='http://localhost/v1'", "", "", "asr.base_url"},
		{"env engine", "", "ALICE_ASR_ENGINE", "gigaam", "asr.engine"},
		{"env f5", "", "ALICE_TTS_F5_URL", "http://localhost/v1", "ALICE_TTS_F5_URL"},
		{"env empty f5", "", "ALICE_TTS_F5_VOICE_ID", "", "ALICE_TTS_F5_VOICE_ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(p, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.env != "" {
				t.Setenv(tc.env, tc.value)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want diagnostic %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCanonicalExample(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ASR.Engine != "vosk" || c.ASR.Device != "cpu" || c.TTS.Engine != "piper" || c.LLM.Model != "echo" {
		t.Fatalf("unsupported example defaults: %+v", c)
	}
}
