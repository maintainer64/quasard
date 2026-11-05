// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadDeviceConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}
func TestDeviceLLMInheritanceAndExplicitZero(t *testing.T) {
	t.Setenv("KITCHEN_LLM_KEY", "kitchen-private")
	t.Setenv("ALICE_LLM_MODEL", "common-env-model")
	c, err := loadDeviceConfig(t, `[llm]
api_key="shared-private"
model="common-file-model"
temperature=0.8
history_turns=8
extra_body={reasoning_effort="none"}
[devices."kitchen-id".llm]
api_key_env="KITCHEN_LLM_KEY"
model="kitchen-model"
temperature=0.0
history_turns=0
listen_after_question=false
hold_response_ms=0
extra_body={}
[devices."bedroom-id".llm]
max_tokens=70
`)
	if err != nil {
		t.Fatal(err)
	}
	kitchen, err := c.LLMForDevice("kitchen-id")
	if err != nil {
		t.Fatal(err)
	}
	if kitchen.APIKey != "kitchen-private" || kitchen.Model != "kitchen-model" || kitchen.Temperature != 0 || kitchen.HistoryTurns != 0 || kitchen.ListenAfterQuestion || kitchen.HoldResponseMS != 0 || len(kitchen.ExtraBody) != 0 {
		t.Fatal("profile overrides not respected")
	}
	bedroom, err := c.LLMForDevice("bedroom-id")
	if err != nil {
		t.Fatal(err)
	}
	if bedroom.APIKey != "shared-private" || bedroom.Model != "common-env-model" || bedroom.MaxTokens != 70 || bedroom.ExtraBody["reasoning_effort"] != "none" {
		t.Fatal("common settings not inherited")
	}
	unknown, err := c.LLMForDevice("unlisted")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Model != "common-env-model" || unknown.APIKey != "shared-private" || unknown.MaxTokens != 400 {
		t.Fatal("unknown device did not use common config")
	}
	if c.LLM.APIKey != "shared-private" || c.LLM.Model != "common-env-model" || len(c.LLM.ExtraBody) != 1 {
		t.Fatal("profile mutated common configuration")
	}
}
func TestDeviceKeysOverrideCommonEnvReference(t *testing.T) {
	t.Setenv("DEFAULT_LLM_KEY", "default-private")
	t.Setenv("DEVICE_LLM_KEY", "device-private")
	c, err := loadDeviceConfig(t, `[llm]
api_key_env="DEFAULT_LLM_KEY"
[devices."one".llm]
api_key="inline-private"
[devices."two".llm]
api_key_env="DEVICE_LLM_KEY"
`)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"one": "inline-private", "two": "device-private", "unknown": "default-private"} {
		l, err := c.LLMForDevice(id)
		if err != nil || l.APIKey != want {
			t.Fatalf("wrong key for %s (error: %v)", id, err)
		}
	}
}
func TestInvalidDeviceProfilesFailWithoutSecrets(t *testing.T) {
	t.Setenv("ABSENT_DEVICE_KEY", "")
	for _, body := range []string{
		`[devices."one".llm]
api_key_env="ABSENT_DEVICE_KEY"`,
		`[devices."one".llm]
api_key="private-value-must-not-appear"
api_key_env="ABSENT_DEVICE_KEY"`,
		`[devices."one".llm]
api_key_env="invalid-name"`,
		`[devices."one".llm]
max_tool_calls=17`,
		`[devices."one".llm]
api_kei="private-value-must-not-appear"`,
		`[devices."one"]
api_key="private-value-must-not-appear"`,
		`[devices." ".llm]
model="x"`,
		`devices=[]`,
	} {
		t.Run(body[:min(22, len(body))], func(t *testing.T) {
			_, err := loadDeviceConfig(t, body)
			if err == nil {
				t.Fatal("accepted invalid profile")
			}
			if strings.Contains(err.Error(), "private-value-must-not-appear") {
				t.Fatal("error disclosed API key")
			}
		})
	}
}
