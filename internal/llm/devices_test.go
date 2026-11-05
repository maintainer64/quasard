// SPDX-License-Identifier: GPL-3.0-only
package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDeviceProfilesConcurrentCredentialsHistoryAndReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		model, _ := body["model"].(string)
		want := map[string]string{"shared": "shared-key", "kitchen": "kitchen-key", "bedroom": "bedroom-key"}[model]
		if r.Header.Get("Authorization") != "Bearer "+want {
			t.Error("request used another profile's key")
		}
		messages := body["messages"].([]any)
		for _, message := range messages[1:] {
			text := message.(map[string]any)["content"].(string)
			if !strings.HasPrefix(text, model+":") {
				t.Errorf("history crossed profiles for model %s", model)
			}
		}
		writeTokens(w, model+": answer.")
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Model, cfg.APIKey = "shared", "shared-key"
	kitchen, bedroom := cfg, cfg
	kitchen.Model, kitchen.APIKey = "kitchen", "kitchen-key"
	bedroom.Model, bedroom.APIKey = "bedroom", "bedroom-key"
	cfg.Devices = map[string]Config{"id-kitchen": kitchen, "id-bedroom": bedroom}
	d := New(cfg)
	// New snapshots profiles; later map updates must not alter live routing.
	cfg.Devices["id-kitchen"] = bedroom
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for id, model := range map[string]string{"id-kitchen": "kitchen", "id-bedroom": "bedroom", "unlisted": "shared"} {
			wg.Add(1)
			go func(id, model string) {
				defer wg.Done()
				if _, err := collect(d, id, model+": hello"); err != nil {
					t.Error(err)
				}
			}(id, model)
		}
		wg.Wait()
	}
	if len(d.devices["id-kitchen"].history["id-kitchen"]) != 6 || len(d.devices["id-bedroom"].history["id-bedroom"]) != 6 || len(d.history["unlisted"]) != 6 {
		t.Fatal("history not retained independently")
	}
	d.Reset("id-kitchen")
	if len(d.devices["id-kitchen"].history["id-kitchen"]) != 0 || len(d.devices["id-bedroom"].history["id-bedroom"]) != 6 || len(d.history["unlisted"]) != 6 {
		t.Fatal("reset affected wrong history")
	}
}
func TestDeviceProfileToolRoundTripUsesSameKey(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer device-key" {
			t.Error("tool completion used wrong key")
		}
		var body struct {
			Model    string
			Messages []message
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "device-model" {
			t.Error("tool completion used wrong model")
		}
		if body.Messages[len(body.Messages)-1].Role == "tool" {
			ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Done."}}]}`)
		} else {
			ioJSON(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}]}}]}`)
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.APIKey = "global-key"
	profile := cfg
	profile.APIKey, profile.Model = "device-key", "device-model"
	tools := &fakeTools{}
	profile.Tools = tools
	cfg.Devices = map[string]Config{"station": profile}
	if _, err := collect(New(cfg), "station", "weather"); err != nil {
		t.Fatal(err)
	}
	if hits != 2 || tools.calls != 1 {
		t.Fatalf("hits=%d tool calls=%d", hits, tools.calls)
	}
}
