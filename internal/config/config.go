// SPDX-License-Identifier: GPL-3.0-only

// Package config loads TOML configuration and ALICE_SECTION_KEY overrides.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Server struct {
	Host              string `toml:"host"`
	HTTPSPort         int    `toml:"https_port"`
	HTTPPort          int    `toml:"http_port"`
	CertFile          string `toml:"certfile"`
	KeyFile           string `toml:"keyfile"`
	PublicUniproxyURL string `toml:"public_uniproxy_url"`
	DataDir           string `toml:"data_dir"`
	LogLevel          string `toml:"log_level"`
	LogTraffic        bool   `toml:"log_traffic"`
}

// Legacy remote speech fields remain only for migration diagnostics in Load.
type ASR struct {
	Engine            string  `toml:"engine"`
	Model             string  `toml:"model"`
	ModelPath         string  `toml:"model_path"`
	LibraryPath       string  `toml:"library_path"`
	VADModelPath      string  `toml:"vad_model_path"`
	Device            string  `toml:"device"`
	Language          string  `toml:"language"`
	BaseURL           string  `toml:"base_url"`
	APIKey            string  `toml:"api_key"`
	VADSilenceMS      int     `toml:"vad_silence_ms"`
	VADThreshold      float64 `toml:"vad_threshold"`
	VADEnergyDB       float64 `toml:"vad_energy_db"`
	MaxUtteranceMS    int     `toml:"max_utterance_ms"`
	NoSpeechTimeoutMS int     `toml:"no_speech_timeout_ms"`
	PartialIntervalMS int     `toml:"partial_interval_ms"`
}
type LLM struct {
	BaseURL             string         `toml:"base_url"`
	APIKey              string         `toml:"api_key"`
	Model               string         `toml:"model"`
	ModelRU             string         `toml:"model_ru"`
	ModelEN             string         `toml:"model_en"`
	MaxToolCalls        int            `toml:"max_tool_calls"`
	Temperature         float64        `toml:"temperature"`
	MaxTokens           int            `toml:"max_tokens"`
	TimeoutS            float64        `toml:"timeout_s"`
	HistoryTurns        int            `toml:"history_turns"`
	HistoryTTLS         int            `toml:"history_ttl_s"`
	ExtraBody           map[string]any `toml:"extra_body"`
	ListenAfterQuestion bool           `toml:"listen_after_question"`
	HoldResponseMS      int            `toml:"hold_response_ms"`
	SystemPromptFile    string         `toml:"system_prompt_file"`
	SystemPrompt        string         `toml:"system_prompt"`
}
type TTS struct {
	Engine      string `toml:"engine"`
	Model       string `toml:"model"`
	ConfigPath  string `toml:"config_path"`
	LibraryPath string `toml:"library_path"`
	Voice       string `toml:"voice"`
	Rate        string `toml:"rate"`
	F5URL       string `toml:"f5_url"`
	F5VoiceID   string `toml:"f5_voice_id"`
	BaseURL     string `toml:"base_url"`
	APIKey      string `toml:"api_key"`
	OpenAIModel string `toml:"openai_model"`
	Cache       bool   `toml:"cache"`
}
type Music struct {
	Enabled bool   `toml:"enabled"`
	Token   string `toml:"token"`
	Quality string `toml:"quality"`
}
type Device struct {
	Name              string  `toml:"name"`
	UID               int64   `toml:"uid"`
	DisplayName       string  `toml:"display_name"`
	Latitude          float64 `toml:"latitude"`
	Longitude         float64 `toml:"longitude"`
	Timezone          string  `toml:"timezone"`
	TimezoneOffsetSec int     `toml:"timezone_offset_sec"`
}
type MCPServer struct {
	Name         string   `toml:"name"`
	URL          string   `toml:"url"`
	TokenEnv     string   `toml:"token_env"`
	AllowedTools []string `toml:"allowed_tools"`
}

type MCP struct {
	Enabled bool        `toml:"enabled"`
	Servers []MCPServer `toml:"servers"`
}

type Config struct {
	Server Server `toml:"server"`
	ASR    ASR    `toml:"asr"`
	LLM    LLM    `toml:"llm"`
	TTS    TTS    `toml:"tts"`
	Music  Music  `toml:"music"`
	Device Device `toml:"device"`
	DNS    DNS    `toml:"dns"`
	MCP    MCP    `toml:"mcp"`
	// Root resolves relative resource paths; normally process working directory.
	Root string `toml:"-"`
}

func Default() Config {
	root, _ := os.Getwd()
	return Config{
		Root:   root,
		DNS:    defaultDNS(),
		Server: Server{Host: "0.0.0.0", HTTPSPort: 443, HTTPPort: 80, CertFile: "certs/server.crt", KeyFile: "certs/server.key", DataDir: "data", LogLevel: "INFO", LogTraffic: false},
		ASR:    ASR{Engine: "vosk", Model: "vosk-model-small-ru-0.22", ModelPath: "models/vosk-model-small-ru-0.22", VADModelPath: "alice_server/assets/silero_vad.onnx", Device: "cpu", Language: "ru", VADSilenceMS: 700, VADThreshold: 0.5, VADEnergyDB: -68, MaxUtteranceMS: 10000, NoSpeechTimeoutMS: 5000, PartialIntervalMS: 300},
		LLM:    LLM{BaseURL: "https://api.groq.com/openai/v1", Model: "qwen/qwen3.8-27b", Temperature: 0.6, MaxTokens: 400, TimeoutS: 60, HistoryTurns: 8, HistoryTTLS: 300, ExtraBody: map[string]any{"reasoning_effort": "none"}, ListenAfterQuestion: true, HoldResponseMS: 400, SystemPromptFile: "system_prompt.txt", SystemPrompt: "Ты — голосовой ассистент Алиса в умной колонке. Отвечай по-русски, коротко и по делу (1–3 предложения), разговорным языком. Никакой разметки, списков, эмодзи и ссылок — текст будет озвучен синтезатором речи. Числа и единицы пиши так, как их произносят."},
		TTS:    TTS{Engine: "piper", Model: "models/ru_RU-dmitri-medium.onnx"},
		Music:  Music{Enabled: true, Quality: "high"},
		Device: Device{Name: "Яндекс Станция", UID: 100000001, DisplayName: "Пользователь", Latitude: 55.7558, Longitude: 37.6173, Timezone: "Europe/Moscow", TimezoneOffsetSec: 10800},
	}
}
func Defaults() Config { return Default() }
func (c Config) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.Root, path)
}
func (c Config) DataPath() string { return c.Resolve(c.Server.DataDir) }

// Load selects explicit path, then ALICE_CONFIG, then cwd config.toml.
// Only an absent implicit config.toml retains defaults; selected files must exist.
// Relative resource paths resolve from working directory, not config directory.
func Load(path string) (Config, error) {
	c := Default()
	selected := path != "" || os.Getenv("ALICE_CONFIG") != ""
	path, err := ConfigPath(path)
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(path)
	if err != nil && (selected || !os.IsNotExist(err)) {
		return c, fmt.Errorf("read config %q: %w", path, err)
	}
	if err == nil {
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		// Python assigns entire dictionary fields, rather than merging defaults.
		var raw map[string]any
		if err = toml.Unmarshal(data, &raw); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
		if err := rejectLegacySpeechFields(raw); err != nil {
			return c, err
		}
		if llm, ok := raw["llm"].(map[string]any); ok {
			if _, ok := llm["extra_body"]; ok {
				c.LLM.ExtraBody = nil
			}
		}
		if err = toml.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
	}
	if err := rejectLegacySpeechFields(nil); err != nil {
		return c, err
	}
	sections := reflect.ValueOf(&c).Elem()
	for i := 0; i < sections.NumField(); i++ {
		section := sections.Field(i)
		if section.Kind() != reflect.Struct || sections.Type().Field(i).Name == "DNS" {
			continue
		}
		name := sections.Type().Field(i).Tag.Get("toml")
		for j := 0; j < section.NumField(); j++ {
			key := "ALICE_" + strings.ToUpper(name+"_"+section.Type().Field(j).Tag.Get("toml"))
			if value, ok := os.LookupEnv(key); ok {
				if err := setEnv(section.Field(j), value); err != nil {
					return c, fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	}
	if err := applyDNSEnv(&c.DNS, captureDNSEnv()); err != nil {
		return c, err
	}
	if err := ValidateDNS(c.DNS); err != nil {
		return c, err
	}
	if c.LLM.MaxToolCalls < 0 || c.LLM.MaxToolCalls > 16 {
		return c, fmt.Errorf("llm.max_tool_calls must be between 0 (default 4) and 16")
	}
	if c.MCP.Enabled && (len(c.MCP.Servers) == 0 || len(c.MCP.Servers) > 8) {
		return c, fmt.Errorf("mcp requires between 1 and 8 servers")
	}
	if c.ASR.Engine != "vosk" {
		return c, fmt.Errorf("asr.engine=%q is unsupported: Go CPU-only backend requires vosk; migrate using config.example.toml (no GigaAM/remote ASR)", c.ASR.Engine)
	}
	if c.TTS.Engine != "piper" {
		return c, fmt.Errorf("tts.engine=%q is unsupported: Go CPU-only backend requires piper; F5 voice cloning and remote TTS are not implemented", c.TTS.Engine)
	}
	if c.ASR.Device != "cpu" {
		return c, fmt.Errorf("asr.device=%q is unsupported: Go backend is CPU-only; use cpu", c.ASR.Device)
	}
	if strings.HasPrefix(strings.ToLower(c.ASR.Model), "v3_") {
		return c, fmt.Errorf("asr.model=%q is a legacy GigaAM model: use a Vosk model and asr.model_path from config.example.toml", c.ASR.Model)
	}
	return c, nil
}

// These options belong to removed Python adapters. Reject their presence, even
// when empty, rather than accepting a config that silently uses Vosk/Piper.
func rejectLegacySpeechFields(raw map[string]any) error {
	for _, section := range []struct {
		name string
		keys []string
	}{
		{"asr", []string{"base_url", "api_key"}},
		{"tts", []string{"voice", "rate", "f5_url", "f5_voice_id", "base_url", "api_key", "openai_model", "cache"}},
	} {
		values, _ := raw[section.name].(map[string]any)
		for _, key := range section.keys {
			_, inFile := values[key]
			env := "ALICE_" + strings.ToUpper(section.name+"_"+key)
			_, inEnv := os.LookupEnv(env)
			if inFile || inEnv {
				return fmt.Errorf("unsupported legacy speech setting %s.%s / %s: Go backend supports only local Vosk/Piper; remove this option and migrate using config.example.toml", section.name, key, env)
			}
		}
	}
	return nil
}

func LoadConfig(path string) (Config, error) { return Load(path) }
func setEnv(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "on":
			v.SetBool(true)
		default:
			v.SetBool(false)
		}
	case reflect.Int, reflect.Int64:
		n, e := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if e != nil {
			return e
		}
		v.SetInt(n)
	case reflect.Float64:
		n, e := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if e != nil {
			return e
		}
		v.SetFloat(n)
	case reflect.Map:
		var m map[string]any
		if e := json.Unmarshal([]byte(s), &m); e != nil {
			return e
		}
		v.Set(reflect.ValueOf(m))
	}
	return nil
}
