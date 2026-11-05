// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
)

type DeviceOverrides struct {
	LLM LLMOverrides `toml:"llm"`
}

// Nil inherits the common setting; explicit zero/false/empty overrides it.
type LLMOverrides struct {
	BaseURL             *string        `toml:"base_url"`
	APIKey              *string        `toml:"api_key"`
	APIKeyEnv           *string        `toml:"api_key_env"`
	Model               *string        `toml:"model"`
	ModelRU             *string        `toml:"model_ru"`
	ModelEN             *string        `toml:"model_en"`
	MaxToolCalls        *int           `toml:"max_tool_calls"`
	Temperature         *float64       `toml:"temperature"`
	MaxTokens           *int           `toml:"max_tokens"`
	TimeoutS            *float64       `toml:"timeout_s"`
	HistoryTurns        *int           `toml:"history_turns"`
	HistoryTTLS         *int           `toml:"history_ttl_s"`
	ExtraBody           map[string]any `toml:"extra_body"`
	ListenAfterQuestion *bool          `toml:"listen_after_question"`
	HoldResponseMS      *int           `toml:"hold_response_ms"`
	SystemPromptFile    *string        `toml:"system_prompt_file"`
	SystemPrompt        *string        `toml:"system_prompt"`
}

// LLMForDevice resolves a startup configuration. Unknown IDs use common settings.
// Explicit secret references never silently fall back to a shared key.
func (c Config) LLMForDevice(id string) (LLM, error) {
	settings, label := c.LLM, "llm"
	if profile, ok := c.Devices[id]; ok {
		label = fmt.Sprintf("devices.%q.llm", id)
		o := profile.LLM
		if o.APIKey != nil && o.APIKeyEnv != nil && *o.APIKey != "" && *o.APIKeyEnv != "" {
			return settings, fmt.Errorf("%s: set only one of api_key and api_key_env", label)
		}
		dst, src := reflect.ValueOf(&settings).Elem(), reflect.ValueOf(o)
		for i := 0; i < src.NumField(); i++ {
			value := src.Field(i)
			if value.IsNil() {
				continue
			}
			if value.Kind() == reflect.Pointer {
				value = value.Elem()
			}
			dst.FieldByName(src.Type().Field(i).Name).Set(value)
		}
		if o.APIKey != nil && o.APIKeyEnv == nil {
			settings.APIKeyEnv = ""
		}
		if o.APIKeyEnv != nil && o.APIKey == nil {
			settings.APIKey = ""
		}
	}
	if settings.MaxToolCalls < 0 || settings.MaxToolCalls > 16 {
		return settings, fmt.Errorf("%s.max_tool_calls must be between 0 (default 4) and 16", label)
	}
	if settings.APIKeyEnv != "" {
		if settings.APIKey != "" {
			return settings, fmt.Errorf("%s: set only one of api_key and api_key_env", label)
		}
		if !validEnvName(settings.APIKeyEnv) {
			return settings, fmt.Errorf("%s.api_key_env must be an environment variable name", label)
		}
		key := os.Getenv(settings.APIKeyEnv)
		if strings.TrimSpace(key) == "" {
			return settings, fmt.Errorf("%s.api_key_env: environment variable %s is missing or empty", label, settings.APIKeyEnv)
		}
		settings.APIKey = key
	}
	return settings, nil
}

func validEnvName(name string) bool {
	for i, r := range name {
		valid := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !valid {
			return false
		}
	}
	return name != ""
}

func (c Config) validateLLMProfiles() error {
	ids := make([]string, 0, len(c.Devices))
	for id := range c.Devices {
		if id == "" || strings.TrimSpace(id) != id {
			return fmt.Errorf("devices: device_id must be nonempty and have no surrounding whitespace")
		}
		ids = append(ids, id)
	}
	if _, err := c.LLMForDevice(""); err != nil {
		return err
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := c.LLMForDevice(id); err != nil {
			return err
		}
	}
	return nil
}

// Profile typos must not silently select the shared key.
func validateDeviceFields(raw map[string]any) error {
	value, exists := raw["devices"]
	if !exists {
		return nil
	}
	devices, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("devices must be a table keyed by device_id")
	}
	fields := map[string]bool{}
	t := reflect.TypeOf(LLMOverrides{})
	for i := 0; i < t.NumField(); i++ {
		fields[t.Field(i).Tag.Get("toml")] = true
	}
	for id, value := range devices {
		device, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("devices.%q must be a table", id)
		}
		for section, value := range device {
			if section != "llm" {
				return fmt.Errorf("devices.%q: unsupported section %q; use a nested llm table", id, section)
			}
			overrides, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("devices.%q.llm must be a table", id)
			}
			for field := range overrides {
				if !fields[field] {
					return fmt.Errorf("devices.%q.llm: unknown field %q", id, field)
				}
			}
		}
	}
	return nil
}
