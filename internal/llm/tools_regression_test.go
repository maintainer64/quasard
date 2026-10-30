// SPDX-License-Identifier: GPL-3.0-only
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func proposalServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ioJSON(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}]}}]}`)
	}))
}
func TestToolCallBudget(t *testing.T) {
	server := proposalServer(t)
	defer server.Close()
	tools := &fakeTools{}
	cfg := testConfig(server.URL)
	cfg.Tools, cfg.MaxToolCalls = tools, 2
	d := New(cfg)
	// Provider ignores tool_choice=none. Backend must still stop the loop.
	if _, err := collect(d, "d", "weather"); err == nil {
		t.Fatal("accepted call over budget")
	}
	if tools.calls != 2 {
		t.Fatalf("calls=%d", tools.calls)
	}
}

func TestToolEnvKey(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "env-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer env-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello."}}]}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Tools = &fakeTools{}
	if _, err := collect(New(cfg), "d", "hello"); err != nil {
		t.Fatal(err)
	}
}
func TestToolTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Tools = &fakeTools{}
	cfg.Timeout = 15 * time.Millisecond
	if _, err := collect(New(cfg), "d", "hello"); err == nil {
		t.Fatal("timeout ignored")
	}
}
func TestToolExtrasFallback(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["reasoning_effort"]; ok {
			http.Error(w, "unsupported reasoning_effort", 400)
			return
		}
		ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello."}}]}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Tools = &fakeTools{}
	cfg.ExtraBody = map[string]any{"reasoning_effort": "none"}
	d := New(cfg)
	if _, err := collect(d, "d", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(d, "d", "hello again"); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("requests=%d", count)
	}
}
func TestToolCanceledEmit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello."}}]}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Tools = &fakeTools{}
	d := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := d.Stream(ctx, "d", "weather", func(string) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(d.history["d"]) != 0 {
		t.Fatal("canceled answer remembered")
	}
}

func TestAutomaticToolChainAtBudget(t *testing.T) {
	tools := &fakeTools{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Model      string    `json:"model"`
			Messages   []message `json:"messages"`
			ToolChoice string    `json:"tool_choice"`
			Stream     bool      `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "english" || body.Stream || tools.calls != requests-1 {
			t.Errorf("model=%s stream=%v calls=%d requests=%d", body.Model, body.Stream, tools.calls, requests)
		}
		if requests > 1 {
			last := body.Messages[len(body.Messages)-1]
			prior := body.Messages[len(body.Messages)-2]
			if last.Role != "tool" || last.ToolCallID != fmt.Sprintf("call%d", requests-1) || last.Content != `{"temperature":18}` || len(prior.ToolCalls) != 1 || prior.Content != "" {
				t.Errorf("uncorrelated tool messages: %+v %+v", prior, last)
			}
		}
		if requests <= 2 {
			if body.ToolChoice != "auto" {
				t.Errorf("tool_choice=%s", body.ToolChoice)
			}
			ioJSON(w, fmt.Sprintf(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"Speculative success","tool_calls":[{"id":"call%d","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}]}}]}`, requests))
			return
		}
		if body.ToolChoice != "none" || requests != 3 {
			t.Errorf("tool_choice=%s requests=%d", body.ToolChoice, requests)
		}
		ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Done."}}]}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Tools, cfg.MaxToolCalls, cfg.ModelEN = tools, 2, "english"
	d := New(cfg)
	output, err := collect(d, "d", "Check weather twice")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || tools.calls != 2 || len(output) != 1 || output[0] != "Done." || len(d.history["d"]) != 2 {
		t.Fatalf("requests=%d calls=%d output=%v history=%v", requests, tools.calls, output, d.history["d"])
	}
}

func TestInvalidAutomaticToolNeverRuns(t *testing.T) {
	for _, call := range []string{
		`{"id":"c","type":"function","function":{"name":"unknown","arguments":"{}"}}`,
		`{"id":"","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}`,
		`{"id":"c","type":"other","function":{"name":"mcp_home__weather","arguments":"{}"}}`,
		`{"id":"c","type":"function","function":{"name":"mcp_home__weather","arguments":"{"}}`,
	} {
		t.Run(call, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ioJSON(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[`+call+`]}}]}`)
			}))
			defer server.Close()
			cfg := testConfig(server.URL)
			tools := &fakeTools{}
			cfg.Tools = tools
			if _, err := collect(New(cfg), "d", "weather"); err == nil || tools.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, tools.calls)
			}
		})
	}
}
