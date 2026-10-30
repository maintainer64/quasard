// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/llm"
	"github.com/maintainer64/quasard/internal/mcpclient"
)

type cancelTools struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (f *cancelTools) Discover(context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "weather", Parameters: map[string]any{"type": "object"}}}, nil
}
func (f *cancelTools) Call(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	f.calls.Add(1)
	close(f.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestCancelAutomaticToolChain(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"weather","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()
	cfg := llm.DefaultConfig()
	cfg.BaseURL = server.URL
	cfg.ExtraBody = nil
	cfg.SystemPromptFile = ""
	tools := &cancelTools{entered: make(chan struct{})}
	cfg.Tools = tools
	d := llm.New(cfg)
	s := services(t)
	s.Dialog = d
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := &Session{svc: s, device: "d", ctx: parent}
	ctx, _ := c.active()
	finished := make(chan error, 1)
	go func() {
		finished <- d.Stream(ctx, "d", "weather", func(string) error {
			t.Error("cancelled tool chain spoke an answer")
			return nil
		})
	}()
	select {
	case <-tools.entered:
	case <-parent.Done():
		t.Fatal("automatic tool did not start")
	}
	c.onMessage(event("Vins", "CancelRequest", "r", nil))
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-parent.Done():
		t.Fatal("tool chain did not stop")
	}
	if requests.Load() != 1 || tools.calls.Load() != 1 {
		t.Fatal("cancelled chain continued or retried")
	}
}
