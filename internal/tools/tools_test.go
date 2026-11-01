// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/maintainer64/quasard/internal/aliceapi"
)

func tempDir(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	e := Run(context.Background(), args, &out, &out)
	return out.String(), e
}
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, e := run(t, args...)
	if e != nil {
		t.Fatalf("%v: %v (%s)", args, e, out)
	}
	return out
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func private(t *testing.T, p string) {
	t.Helper()
	s, e := os.Stat(p)
	if e != nil {
		t.Fatal(e)
	}
	if s.Mode().Perm() != 0600 {
		t.Fatalf("mode %v: %s", s.Mode(), p)
	}
}
func TestHelpAndInvalidFlags(t *testing.T) {
	for _, c := range []string{"certs", "import-har", "activation-sim", "station-sim", "fake-llm", "voice-prep", "music-login"} {
		if !strings.Contains(mustRun(t, c, "--help"), "Usage") {
			t.Fatal(c)
		}
		if _, e := run(t, c, "--does-not-exist"); e == nil {
			t.Fatal(c)
		}
	}
	if _, e := run(t, "bogus"); e == nil {
		t.Fatal("unknown command accepted")
	}
}
func TestCertificates(t *testing.T) {
	dir := filepath.Join(tempDir(t), "certs")
	mustRun(t, "certs", "--out", dir, "--ip", "192.0.2.1", "--host", "local.test")
	key := read(t, filepath.Join(dir, "ca.key"))
	block, _ := pem.Decode(read(t, filepath.Join(dir, "ca.crt")))
	ca, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	block, _ = pem.Decode(read(t, filepath.Join(dir, "server.crt")))
	leaf, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, host := range append(append([]string{}, knownHosts...), "localhost", "127.0.0.1", "192.0.2.1", "local.test") {
		if _, e = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host}); e != nil {
			t.Fatal(host, e)
		}
	}
	for _, name := range []string{"ca.key", "ca.crt", "server.key", "server.crt"} {
		private(t, filepath.Join(dir, name))
	}
	mustRun(t, "certs", "--out", dir)
	if !bytes.Equal(key, read(t, filepath.Join(dir, "ca.key"))) {
		t.Fatal("CA replaced")
	}
	if e = os.Remove(filepath.Join(dir, "ca.crt")); e != nil {
		t.Fatal(e)
	}
	if _, e = run(t, "certs", "--out", dir); e == nil {
		t.Fatal("partial CA replaced")
	}
	if _, e = run(t, "certs", "--out", dir, "--ip", "bad"); e == nil {
		t.Fatal("invalid IP")
	}
}
func TestPrivateWriteRejectsSymlinks(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "target")
	if e := os.WriteFile(target, []byte("untouched"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	if privateWrite(link, []byte("bad")) == nil {
		t.Fatal("followed symlink")
	}
	if string(read(t, target)) != "untouched" {
		t.Fatal("target changed")
	}
	dlink := filepath.Join(dir, "dirlink")
	if e := os.Symlink(dir, dlink); e != nil {
		t.Fatal(e)
	}
	if privateWrite(filepath.Join(dlink, "x"), nil) == nil {
		t.Fatal("followed directory symlink")
	}
}
func TestHARPrivateAndSafe(t *testing.T) {
	dir := tempDir(t)
	har := filepath.Join(dir, "input.har")
	out := filepath.Join(dir, "output")
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, 1)
	frame = append(frame, []byte("OggSsample")...)
	entries := []any{}
	for _, value := range []string{"first-secret", "last-secret"} {
		entries = append(entries, map[string]any{"request": map[string]any{"url": "https://quasar.yandex.net/get_sync_info?device_id=../../escape"}, "response": map[string]any{"status": 200, "content": map[string]any{"text": base64.StdEncoding.EncodeToString([]byte(`{"glagol":{"token":"` + value + `"}}`)), "encoding": "base64"}}})
	}
	entries = append(entries, map[string]any{"request": map[string]any{"url": "wss://example.test/uni.ws"}, "_webSocketMessages": []any{map[string]any{"type": "send", "opcode": 2, "data": base64.StdEncoding.EncodeToString(frame)}}})
	b, err := json.Marshal(map[string]any{"log": map[string]any{"entries": entries}})
	if err != nil {
		t.Fatal(err)
	}
	if e := os.WriteFile(har, b, 0600); e != nil {
		t.Fatal(e)
	}
	printed := mustRun(t, "import-har", har, "--out", out)
	if strings.Contains(printed, "secret") {
		t.Fatal("leaked HAR")
	}
	for _, name := range []string{"captured/get_sync_info.json", "devices/escape.json", "samples/voice_2_1.ogg"} {
		private(t, filepath.Join(out, name))
	}
	if !bytes.Contains(read(t, filepath.Join(out, "captured/get_sync_info.json")), []byte("last-secret")) {
		t.Fatal("not last response")
	}
	if _, e := os.Stat(filepath.Join(dir, "escape.json")); !os.IsNotExist(e) {
		t.Fatal("escaped output")
	}
	if safeName("../../") == "" || strings.Contains(safeName("../../"), ".") {
		t.Fatal("unsafe name")
	}
}
func TestFakeLLMStreaming(t *testing.T) {
	s := httptest.NewServer(fakeLLMHandler(0))
	defer s.Close()
	r, e := http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[{"content":"привет"}],"stream":true}`))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Body.Close() }()
	b, e := io.ReadAll(r.Body)
	if e != nil {
		t.Fatal(e)
	}
	if r.Header.Get("Content-Type") != "text/event-stream" || !bytes.Contains(b, []byte("data: [DONE]\n\n")) {
		t.Fatal("missing SSE terminator")
	}
	var answer strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var v struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if e = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); e != nil {
			t.Fatal(e)
		}
		answer.WriteString(v.Choices[0].Delta.Content)
	}
	if !strings.Contains(answer.String(), "привет") {
		t.Fatal("Unicode corrupted")
	}
	if _, e := run(t, "fake-llm", "--host", "0.0.0.0"); e == nil {
		t.Fatal("nonlocal listener")
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestOutputWriteErrors(t *testing.T) {
	writeErr := errors.New("output unavailable")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Error(err)
		}
	}))
	defer s.Close()
	input := filepath.Join(tempDir(t), "input.wav")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"activation-sim", "--base", s.URL},
		{"fake-llm", "--port", "0"},
		{"voice-prep", "--out", filepath.Join(tempDir(t), "clips"), "--ffmpeg", "unused", input},
	} {
		t.Run(args[0], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := Run(ctx, args, failingWriter{writeErr}, io.Discard); !errors.Is(err, writeErr) {
				t.Fatalf("got %v, want writer error", err)
			}
		})
	}
}

func TestActivationNoSecretLogging(t *testing.T) {
	count := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Host == "" || r.Header.Get("User-Agent") != userAgent {
			t.Error("headers")
		}
		if _, err := fmt.Fprint(w, `{"access_token":"super-secret"}`); err != nil {
			t.Error(err)
		}
	}))
	defer s.Close()
	out := mustRun(t, "activation-sim", "--base", s.URL)
	if count != 14 {
		t.Fatalf("requests %d", count)
	}
	if strings.Contains(out, "super-secret") {
		t.Fatal("secret printed")
	}
}
func TestOggValidation(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("OggS"), make([]byte, 27)} {
		if _, e := oggPages(b); e == nil {
			t.Fatal("invalid Ogg accepted")
		}
	}
	p := make([]byte, 27)
	copy(p, "OggS")
	pages, e := oggPages(p)
	if e != nil || len(pages) != 1 {
		t.Fatal(e)
	}
}
func TestStationJSONAndProto(t *testing.T) {
	codec, e := aliceapi.Load("../../alice_server/aliceapi.desc")
	if e != nil {
		t.Fatal(e)
	}
	for _, protoMode := range []bool{false, true} {
		for _, voice := range []bool{false, true} {
			t.Run(fmt.Sprintf("proto=%t/voice=%t", protoMode, voice), func(t *testing.T) {
				dir := tempDir(t)
				received := make(chan error, 1)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ws, err := websocket.Accept(w, r, nil)
					if err != nil {
						received <- err
						return
					}
					defer func() { _ = ws.CloseNow() }()
					ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
					defer cancel()
					for i := 0; i < 2; i++ {
						typ, b, err := ws.Read(ctx)
						if err != nil {
							received <- err
							return
						}
						var v wireMap
						if protoMode {
							if typ != websocket.MessageBinary {
								received <- fmt.Errorf("wrong frame type")
								return
							}
							v, err = codec.DecodeClient(b)
							if err != nil {
								received <- err
								return
							}
							ev := object(v["Event"])
							kind := "SynchronizeStateRequest"
							if i == 1 {
								kind = "TextInput"
								if voice {
									kind = "VoiceInput"
								}
							}
							if _, ok := ev[kind]; !ok {
								received <- fmt.Errorf("missing %s: %v", kind, v)
								return
							}
						} else {
							err = json.Unmarshal(b, &v)
							if err != nil {
								received <- err
								return
							}
							if len(object(v["event"])) == 0 {
								received <- fmt.Errorf("missing event")
								return
							}
						}
					}
					if voice {
						for {
							typ, b, err := ws.Read(ctx)
							if err != nil {
								received <- err
								return
							}
							if protoMode && bytes.HasPrefix(b, aliceapi.Magic) {
								v, err := codec.DecodeClient(b)
								if err != nil {
									received <- err
									return
								}
								if _, ok := object(v["StreamControl"])["Close"]; ok {
									break
								}
							} else if typ == websocket.MessageText {
								var v wireMap
								if err := json.Unmarshal(b, &v); err != nil {
									received <- err
									return
								}
								if fmt.Sprint(object(v["streamcontrol"])["action"]) == "0" {
									break
								}
							}
						}
					}
					b := []byte{0, 0, 0, 2}
					b = append(b, []byte("OggSanswer")...)
					if err = ws.Write(ctx, websocket.MessageBinary, b); err != nil {
						received <- err
						return
					}
					if protoMode {
						b, err = codec.EncodeServer(wireMap{"StreamControl": wireMap{"StreamId": 2, "Close": wireMap{"Reason": "OK"}}})
						if err == nil {
							err = ws.Write(ctx, websocket.MessageBinary, b)
						}
					} else {
						err = ws.Write(ctx, websocket.MessageText, []byte(`{"streamcontrol":{"streamId":2,"action":0}}`))
					}
					received <- err
				}))
				defer s.Close()
				args := []string{"station-sim", "--url", "ws" + strings.TrimPrefix(s.URL, "http") + "/uni.ws", "--out", dir, "--timeout", "3s", "--tail-silence", "0"}
				if protoMode {
					args = append(args, "--proto", "--descriptor", "../../alice_server/aliceapi.desc")
				}
				if voice {
					p := make([]byte, 27)
					copy(p, "OggS")
					path := filepath.Join(dir, "input.ogg")
					if err := os.WriteFile(path, p, 0600); err != nil {
						t.Fatal(err)
					}
					args = append(args, path)
				} else {
					args = append(args, "--text", "private-query")
				}
				out, err := run(t, args...)
				if err != nil {
					t.Error(err)
				}
				if err = <-received; err != nil {
					t.Error(err)
				}
				if strings.Contains(out, "private-query") {
					t.Fatal("query logged")
				}
				if err == nil {
					private(t, filepath.Join(dir, "sim_answer_2.ogg"))
				}
			})
		}
	}
}
func TestVoicePrepFFmpeg(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := tempDir(t)
	input := filepath.Join(dir, "input.wav")
	cmd := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-y", input)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate fixture: %v %s", e, b)
	}
	out := filepath.Join(dir, "clips")
	printed := mustRun(t, "voice-prep", input, "--out", out, "--lengths", "0.1", "0.2", "--top", "1", "--ffmpeg", ffmpeg)
	if !strings.Contains(printed, "Piper is not equivalent") {
		t.Fatal("missing scope warning")
	}
	for _, name := range []string{"ref_0.1s_1.wav", "ref_0.2s_1.wav"} {
		p := filepath.Join(out, name)
		private(t, p)
		if !bytes.HasPrefix(read(t, p), []byte("RIFF")) {
			t.Fatal("not WAV")
		}
	}
}
