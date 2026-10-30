// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/maintainer64/quasard/internal/aliceapi"
	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/llm"
	P "github.com/maintainer64/quasard/internal/protocol"
	"github.com/maintainer64/quasard/internal/speech"
)

type fakeSpeech struct{}

func (fakeSpeech) Transcribe(ctx context.Context, pcm []int16) (string, error) {
	return "привет", ctx.Err()
}
func (fakeSpeech) Synthesize(ctx context.Context, s string) (speech.PCM, error) {
	return speech.PCM{Samples: make([]int16, 2205), SampleRate: 22050}, ctx.Err()
}
func services(t *testing.T) *Services {
	t.Helper()
	cfg := config.Default()
	cfg.LLM.HoldResponseMS = 0
	lc := llm.DefaultConfig()
	lc.Model = "echo"
	codec, err := aliceapi.Load("../../alice_server/aliceapi.desc")
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, llm.New(lc), nil, fakeSpeech{}, codec)
}
func connect(t *testing.T, s *Services) (*websocket.Conn, context.Context) {
	t.Helper()
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"X-Uprx-Device-Id": []string{"device"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() }) // Best-effort connection cleanup.
	return conn, ctx
}
func event(ns, name, ref string, p Map) Map {
	return Map{"event": Map{"header": Map{"namespace": ns, "name": name, "messageId": ref}, "payload": p}}
}
func write(t *testing.T, ctx context.Context, c *websocket.Conn, m Map) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, ctx context.Context, c *websocket.Conn) Map {
	t.Helper()
	kind, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText {
		t.Fatal("expected text")
	}
	var m Map
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestSyncAndCommand(t *testing.T) {
	s := services(t)
	c, ctx := connect(t, s)
	write(t, ctx, c, event("System", "SynchronizeState", "sync", Map{}))
	m := read(t, ctx, c)
	h := obj(obj(m["directive"])["header"])
	if h["name"] != "SynchronizeStateResponse" || h["messageId"] != nil {
		t.Fatal(m)
	}
	if obj(obj(m["directive"])["payload"])["SessionId"] == "" {
		t.Fatal(m)
	}
	read(t, ctx, c)
	write(t, ctx, c, event("Vins", "TextInput", "cmd", Map{"text": "громкость пять"}))
	m = read(t, ctx, c)
	payload := obj(obj(m["directive"])["payload"])
	dirs := obj(payload["response"])["directives"].([]any)
	a := obj(dirs[0])
	if a["name"] != "sound_set_level" || number(obj(a["payload"])["new_level"]) != 5 {
		t.Fatal(m)
	}
	if number(s.Connections()["device"]) != 1 {
		t.Fatal(s.Connections())
	}
}
func TestAAPI(t *testing.T) {
	s := services(t)
	c, ctx := connect(t, s)
	b, err := s.Codec.EncodeClient(Map{"Event": Map{"Header": Map{"MessageId": "ref"}, "SynchronizeStateRequest": Map{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		kind, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			t.Fatal("expected binary")
		}
		m, e := s.Codec.DecodeServer(b)
		if e != nil {
			t.Fatal(e)
		}
		ev := obj(m["Event"])
		if i == 0 && obj(ev["SynchronizeStateResponse"])["SessionId"] == nil {
			t.Fatal(m)
		}
	}
}
func TestStreamingTTS(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	s := services(t)
	c, ctx := connect(t, s)
	write(t, ctx, c, event("TTS", "Generate", "tts", Map{"text": "Привет"}))
	seenSpeak, seenAudio, seenClose := false, false, false
	for !seenClose {
		kind, b, e := c.Read(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if kind == websocket.MessageBinary {
			if len(b) < 4 || binary.BigEndian.Uint32(b) != 2 {
				t.Fatal("audio framing")
			}
			seenAudio = true
			continue
		}
		var m Map
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if text(obj(obj(m["directive"])["header"])["name"]) == "Speak" {
			seenSpeak = true
		}
		if sc := obj(m["streamcontrol"]); number(sc["streamId"]) == 2 {
			seenClose = true
		}
	}
	if !seenSpeak || !seenAudio {
		t.Fatalf("speak=%v audio=%v", seenSpeak, seenAudio)
	}
}

type shutdownSpeech struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	finished  chan struct{}
}

func (s *shutdownSpeech) Transcribe(ctx context.Context, pcm []int16) (string, error) {
	return "", ctx.Err()
}
func (s *shutdownSpeech) Synthesize(ctx context.Context, text string) (speech.PCM, error) {
	close(s.started)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	close(s.finished)
	return speech.PCM{}, ctx.Err()
}

func TestShutdownWaitsForSpeech(t *testing.T) {
	s := services(t)
	sp := &shutdownSpeech{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s.Speech = sp
	c, ctx := connect(t, s)
	write(t, ctx, c, event("TTS", "Generate", "tts", Map{"text": "Привет"}))
	select {
	case <-sp.started:
	case <-ctx.Done():
		t.Fatal("synthesis did not start")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-sp.cancelled:
	case <-ctx.Done():
		close(sp.release)
		t.Fatal("shutdown did not cancel synthesis")
	}
	select {
	case <-done:
		close(sp.release)
		t.Fatal("shutdown returned before synthesis stopped")
	default:
	}
	close(sp.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-sp.finished:
	default:
		t.Fatal("speech worker still running")
	}
	if len(s.Connections()) != 0 {
		t.Fatal(s.Connections())
	}
	s.Close()
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/uni.ws", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code)
	}
}

func TestGenerationRejectsStaleOutput(t *testing.T) {
	s := services(t)
	c, ctx := connect(t, s)
	write(t, ctx, c, event("System", "SynchronizeState", "sync", Map{}))
	read(t, ctx, c)
	read(t, ctx, c)
	sess := s.find("device")
	_, gen := sess.active()
	sess.cancelActive()
	if err := sess.send(ctx, P.Control(2, 0, 0), gen); err != context.Canceled {
		t.Fatal(err)
	}
}
