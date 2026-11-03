// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/llm"
	"time"
)

func dialogConfig(cfg config.Config, tools llm.ToolClient) (llm.Config, error) {
	common, err := cfg.LLMForDevice("")
	if err != nil {
		return llm.Config{}, err
	}
	result := llmConfig(cfg, common, tools)
	result.Devices = make(map[string]llm.Config, len(cfg.Devices))
	for id := range cfg.Devices {
		settings, err := cfg.LLMForDevice(id)
		if err != nil {
			return llm.Config{}, err
		}
		result.Devices[id] = llmConfig(cfg, settings, tools)
	}
	return result, nil
}
func llmConfig(cfg config.Config, settings config.LLM, tools llm.ToolClient) llm.Config {
	result := llm.DefaultConfig()
	result.BaseURL, result.APIKey, result.Model = settings.BaseURL, settings.APIKey, settings.Model
	result.SystemPrompt, result.SystemPromptFile = settings.SystemPrompt, settings.SystemPromptFile
	result.Root, result.Timezone = cfg.Root, cfg.Device.Timezone
	result.Temperature, result.MaxTokens = settings.Temperature, settings.MaxTokens
	result.HistoryTurns = settings.HistoryTurns
	result.HistoryTTL = time.Duration(settings.HistoryTTLS) * time.Second
	result.Timeout = time.Duration(settings.TimeoutS * float64(time.Second))
	result.ExtraBody = settings.ExtraBody
	result.ModelRU, result.ModelEN = settings.ModelRU, settings.ModelEN
	result.MaxToolCalls, result.Tools = settings.MaxToolCalls, tools
	return result
}
