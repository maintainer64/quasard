// SPDX-License-Identifier: GPL-3.0-only
package llm

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticTraceHasStagesButNoSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeTokens(w, "private response must never be logged.") }))
	defer server.Close()
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})).With("request_id", "turn-123")
	cfg := testConfig(server.URL + "/v1?hidden-query=private-query-value")
	cfg.APIKey = "private-key-value"
	cfg.SystemPrompt = "private-system-prompt"
	d := New(cfg)
	ctx := WithLogger(context.Background(), logger)
	if err := d.Stream(ctx, "station-a", "private-user-utterance", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	for _, want := range []string{"LLM turn started", "LLM HTTP response", "LLM first token", "LLM turn finished", "request_id=turn-123", "device=station-a"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing stage %s", want)
		}
	}
	for _, secret := range []string{"private-key-value", "private-query-value", "private-system-prompt", "private-user-utterance", "private response", "Authorization"} {
		if strings.Contains(logs, secret) {
			t.Fatal("diagnostic trace exposed sensitive data")
		}
	}
}
