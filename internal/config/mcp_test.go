// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPAndLanguageConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`[llm]
model_ru="ru"
model_en="en"
max_tool_calls=3
[mcp]
enabled=true
[[mcp.servers]]
name="home"
url="https://example.org/mcp"
token_env="HOME_TOKEN"
allowed_tools=["weather"]
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCP.Enabled || len(cfg.MCP.Servers) != 1 || cfg.MCP.Servers[0].AllowedTools[0] != "weather" || cfg.LLM.ModelRU != "ru" || cfg.LLM.ModelEN != "en" || cfg.LLM.MaxToolCalls != 3 {
		t.Fatalf("bad parsed config: %+v", cfg.MCP)
	}
}
func TestInvalidMCPConfig(t *testing.T) {
	for _, text := range []string{"[mcp]\nenabled=true", "[llm]\nmax_tool_calls=17", "[llm]\nmax_tool_calls=-1"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}
