// SPDX-License-Identifier: GPL-3.0-only
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/mcpclient"
)

type fakeTools struct {
	calls int
	fail  bool
}

func (f *fakeTools) Discover(context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "mcp_home__weather", Description: "Weather", Parameters: map[string]any{"type": "object"}}}, nil
}
func (f *fakeTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.calls++
	if f.fail {
		return "", errors.New("private transport failure")
	}
	return `{"temperature":18}`, nil
}

func TestStreamLanguageModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		if body["model"] != "english" || body["stream"] != true {
			t.Errorf("wrong routed request: %v", body)
		}
		writeTokens(w, "Hello from English model.")
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.ModelEN = "english"
	cfg.ExtraBody = map[string]any{"model": "override", "stream": false}
	if _, err := collect(New(cfg), "d", "Hello world"); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageRouting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelRU = "ru"
	cfg.ModelEN = "en"
	d := New(cfg)
	for _, tt := range []struct{ text, want string }{{"Привет мир", "ru"}, {"Hello world", "en"}, {"123", cfg.Model}, {"Я I", cfg.Model}, {"Hello привет мир", "ru"}} {
		got, _ := d.route(tt.text)
		if got != tt.want {
			t.Fatalf("%q: %s", tt.text, got)
		}
	}
}

func TestToolAutoRoundtrip(t *testing.T) {
	f := &fakeTools{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["model"]) != `"ru"` {
			t.Errorf("wrong model %s", body["model"])
		}
		if requests == 1 {
			ioJSON(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call1","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}]}}]}`)
		} else {
			if !strings.Contains(string(body["messages"]), `"tool_call_id":"call1"`) {
				t.Error("missing correlated tool result")
			}
			ioJSON(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Сейчас восемнадцать градусов."}}]}`)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.BaseURL = server.URL
	cfg.ModelRU = "ru"
	cfg.Tools = f
	cfg.Timeout = time.Second
	d := New(cfg)
	var output []string
	emit := func(s string) error { output = append(output, s); return nil }
	if err := d.Stream(context.Background(), "device", "Какая погода?", emit); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || requests != 2 {
		t.Fatalf("calls=%d requests=%d", f.calls, requests)
	}
	if strings.Join(output, " ") != "Сейчас восемнадцать градусов." {
		t.Fatalf("unexpected speech: %v", output)
	}
	if len(d.history["device"]) != 2 {
		t.Fatal("missing completed history")
	}
}

func ioJSON(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(text))
}

func TestToolFailureNoRetry(t *testing.T) {
	server := proposalServer(t)
	defer server.Close()
	for _, emitError := range []bool{false, true} {
		f := &fakeTools{fail: true}
		cfg := testConfig(server.URL)
		cfg.Tools = f
		d := New(cfg)
		var speech string
		callbackErr := errors.New("closed")
		err := d.Stream(context.Background(), "d", "weather", func(text string) error {
			speech = text
			if emitError {
				return callbackErr
			}
			return nil
		})
		if (emitError && !errors.Is(err, callbackErr)) || (!emitError && err != nil) {
			t.Fatal(err)
		}
		if f.calls != 1 || len(d.history["d"]) != 0 || !strings.Contains(speech, "no automatic retry") || strings.Contains(speech, "private") {
			t.Fatalf("calls=%d speech=%q", f.calls, speech)
		}
	}
}

func TestIncompleteToolCompletionNeverRuns(t *testing.T) {
	for _, finish := range []string{"length", "content_filter", "stop"} {
		t.Run(finish, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ioJSON(w, `{"choices":[{"finish_reason":"`+finish+`","message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"mcp_home__weather","arguments":"{}"}}]}}]}`)
			}))
			defer server.Close()
			f := &fakeTools{}
			cfg := DefaultConfig()
			cfg.BaseURL = server.URL
			cfg.Tools = f
			d := New(cfg)
			if err := d.Stream(context.Background(), "d", "weather", func(string) error { return nil }); err == nil {
				t.Fatal("accepted invalid completion")
			}
			if f.calls != 0 {
				t.Fatal("unsafe call accepted")
			}
		})
	}
}
