// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/llm"
)

type diagnosticBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *diagnosticBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }
func TestSessionDiagnosticsCorrelateWithoutConversation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	var logs diagnosticBuffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(original)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Sensitive answer must stay private.\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	cfg := config.Default()
	cfg.LLM.HoldResponseMS = 0
	lc := llm.DefaultConfig()
	lc.BaseURL = provider.URL
	lc.APIKey = "session-private-key"
	lc.SystemPromptFile = ""
	s := New(cfg, llm.New(lc), nil, fakeSpeech{}, nil)
	conn, ctx := connect(t, s)
	write(t, ctx, conn, event("Vins", "TextInput", "ref", Map{"text": "sensitive user utterance", "header": Map{"request_id": "turn-visible"}}))
	for {
		kind, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageBinary {
			continue
		}
		var m Map
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if sc := obj(m["streamcontrol"]); number(sc["streamId"]) == 2 {
			break
		}
	}
	s.Close()
	data := logs.String()
	for _, want := range []string{"station connected", "answer started", "request_id=turn-visible", "device=device", "LLM first token", "TTS phrase ready", "audio output started", "answer finished"} {
		if !strings.Contains(data, want) {
			t.Fatalf("missing diagnostic stage %s", want)
		}
	}
	for _, secret := range []string{"session-private-key", "sensitive user utterance", "Sensitive answer"} {
		if strings.Contains(data, secret) {
			t.Fatal("conversation data appeared in logs")
		}
	}
}
func TestLateAudioClosedPipeIsQuiet(t *testing.T) {
	var logs diagnosticBuffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(original)
	reader, writer := io.Pipe()
	_ = reader.Close()
	defer func() { _ = writer.Close() }()
	c := &Session{voice: &voice{id: 1, pipe: writer, ctx: context.Background()}}
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, 1)
	c.onAudio(b)
	if strings.Contains(logs.String(), "audio stream write") {
		t.Fatal("late packet emitted closed-pipe noise")
	}
}
