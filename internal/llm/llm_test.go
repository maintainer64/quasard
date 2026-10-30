// SPDX-License-Identifier: GPL-3.0-only

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeTokens(w http.ResponseWriter, tokens ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, tok := range tokens {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": tok}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}
func testConfig(url string) Config {
	c := DefaultConfig()
	c.BaseURL = url
	c.SystemPromptFile = ""
	c.SystemPrompt = "System"
	c.ExtraBody = nil
	return c
}
func collect(d *Dialog, device, text string) ([]string, error) {
	var out []string
	err := d.Stream(context.Background(), device, text, func(s string) error { out = append(out, s); return nil })
	return out, err
}
func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return body
}
func TestStreamRequestAndThink(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "env-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer env-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request: %+v", r)
		}
		body := readBody(t, r)
		if body["model"] != "test-model" || body["max_tokens"] != float64(400) || body["temperature"] != 0.6 || body["stream"] != true {
			t.Errorf("body %+v", body)
		}
		messages := body["messages"].([]any)
		wantSystem := "System\nТекущие дата и время: понедельник, 05.01.2026 03:04 (Europe/Moscow)."
		if messages[0].(map[string]any)["content"] != wantSystem {
			t.Errorf("system %+v", messages[0])
		}
		_, _ = fmt.Fprint(w, ": keepalive\nevent: message\ndata: invalid\ndata: {}\ndata: {\"choices\":[]}\ndata: {\"choices\":[{\"delta\":{\"reasoning_content\":\"secret\"}}]}\n")
		writeTokens(w, "  <th", "ink>", "private thoughts. ", "</thi", "nk> **Привет, мир!** ", "[Ссылка](http://example.com)", " и хвост")
	}))
	defer server.Close()
	cfg := testConfig(server.URL + "/v1///")
	cfg.Model = "test-model"
	cfg.Now = func() time.Time { return time.Date(2026, 1, 5, 0, 4, 0, 0, time.UTC) }
	d := New(cfg)
	got, err := collect(d, "one", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Привет, мир! Ссылка и хвост"}
	// Closing Markdown stars block sentence punctuation until cleanup, so flush
	// emits this as one phrase, just like the reference.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if d.history["one"][1].Content != strings.Join(want, " ") {
		t.Fatal(d.history)
	}
}
func TestHistoryTTLResetAndTrim(t *testing.T) {
	var requests [][]message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body.Messages)
		writeTokens(w, "Полный ответ.")
	}))
	defer server.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := testConfig(server.URL)
	cfg.HistoryTurns = 2
	cfg.HistoryTTL = 10 * time.Second
	cfg.Now = func() time.Time { return now }
	d := New(cfg)
	for _, text := range []string{"one", "two", "three", "four"} {
		if _, err := collect(d, "a", text); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	if len(requests[3]) != 6 || requests[3][1].Content != "two" || len(d.history["a"]) != 4 {
		t.Fatalf("trim: %+v %+v", requests, d.history)
	}
	if _, err := collect(d, "b", "other device"); err != nil {
		t.Fatal(err)
	}
	if len(requests[4]) != 2 {
		t.Fatal("device leakage")
	}
	now = now.Add(10 * time.Second)
	if _, err := collect(d, "a", "TTL boundary"); err != nil {
		t.Fatal(err)
	}
	if len(requests[5]) != 2 {
		t.Fatal("TTL not expired")
	}
	d.Reset("a")
	if _, err := collect(d, "a", "reset"); err != nil {
		t.Fatal(err)
	}
	if len(requests[6]) != 2 {
		t.Fatal("reset")
	}
	cfg.HistoryTurns = 0
	zero := New(cfg)
	if _, err := collect(zero, "a", "zero"); err != nil {
		t.Fatal(err)
	}
	if len(zero.history["a"]) != 0 {
		t.Fatal("zero turns")
	}
}
func TestTTLStrictBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := testConfig("")
	cfg.Now = func() time.Time { return now }
	cfg.HistoryTTL = time.Second
	d := New(cfg)
	d.remember("a", "user", "assistant")
	now = now.Add(time.Second)
	m, err := d.messages("a", "at TTL")
	if err != nil || len(m) != 4 {
		t.Fatalf("%+v %v", m, err)
	}
	now = now.Add(time.Nanosecond)
	m, err = d.messages("a", "past TTL")
	if err != nil || len(m) != 2 {
		t.Fatalf("%+v %v", m, err)
	}
}
func TestHTTP400ExtraFallback(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			http.Error(w, "unknown field", 400)
			return
		}
		writeTokens(w, "Ответ готов.")
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.ExtraBody = map[string]any{"reasoning_effort": "none", "temperature": 0.2}
	d := New(cfg)
	for i := 0; i < 2; i++ {
		if _, err := collect(d, "a", "text"); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 || bodies[0]["reasoning_effort"] != "none" || bodies[0]["temperature"] != 0.2 {
		t.Fatal(bodies)
	}
	for _, body := range bodies[1:] {
		if _, ok := body["reasoning_effort"]; ok || body["temperature"] != 0.6 {
			t.Fatal(body)
		}
	}
	if len(d.history["a"]) != 4 {
		t.Fatal("fallback duplicated history", d.history)
	}
}
func TestHTTPFailuresAndEmptyOutputDoNotRemember(t *testing.T) {
	for _, code := range []int{400, 401, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var hits int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.WriteHeader(code)
				_, _ = fmt.Fprint(w, strings.Repeat("я", 350))
			}))
			defer server.Close()
			d := New(testConfig(server.URL))
			_, err := collect(d, "a", "failed")
			want := fmt.Sprintf("LLM HTTP %d: %s", code, strings.Repeat("я", 300))
			if err == nil || err.Error() != want || hits != 1 || len(d.history["a"]) != 0 {
				t.Fatalf("%v %d %+v", err, hits, d.history)
			}
		})
	}
	for _, tokens := range [][]string{{}, {"<think>never closed"}, {"<", "thi"}, {"**"}, {"  "}} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeTokens(w, tokens...) }))
			defer server.Close()
			d := New(testConfig(server.URL))
			out, err := collect(d, "a", "empty")
			if err != nil || len(out) != 0 || len(d.history["a"]) != 0 {
				t.Fatalf("%v %q %+v", err, out, d.history)
			}
		})
	}
}
func TestCallbackErrorAndCancelDoNotRemember(t *testing.T) {
	for _, cancelMode := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelMode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTokens(w, "Первая готовая фраза. ", "Хвост")
			}))
			defer server.Close()
			d := New(testConfig(server.URL))
			d.remember("a", "old", "answer")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("consumer stopped")
			calls := 0
			err := d.Stream(ctx, "a", "new", func(s string) error {
				calls++
				if cancelMode {
					cancel()
					return nil
				}
				return sentinel
			})
			want := sentinel
			if cancelMode {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 1 || len(d.history["a"]) != 2 {
				t.Fatalf("%v calls=%d hist=%+v", err, calls, d.history)
			}
		})
	}
}
func TestNetworkCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Incomplete\"}}]}\n")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			cfg := testConfig(server.URL)
			if timeout {
				cfg.Timeout = 50 * time.Millisecond
			}
			d := New(cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- d.Stream(ctx, "a", "text", func(string) error { return nil }) }()
			<-started
			if !timeout {
				cancel()
			}
			err := <-done
			expected := context.Canceled
			if timeout {
				expected = context.DeadlineExceeded
			}
			if !errors.Is(err, expected) || len(d.history["a"]) != 0 {
				t.Fatalf("%v %+v", err, d.history)
			}
		})
	}
}
func TestTruncatedStreamDoesNotFlushOrCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "9999")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Несколько слов без точки\"}}]}\n")
	}))
	defer server.Close()
	d := New(testConfig(server.URL))
	out, err := collect(d, "a", "text")
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(out) != 0 || len(d.history["a"]) != 0 {
		t.Fatalf("%v %q %+v", err, out, d.history)
	}
}
func TestPromptHotReload(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "prompt.txt")
	cfg := testConfig("")
	cfg.Root = root
	cfg.SystemPromptFile = "prompt.txt"
	d := New(cfg)
	check := func(want string) {
		t.Helper()
		m, err := d.messages("a", "text")
		if err != nil || !strings.HasPrefix(m[0].Content, want+"\nТекущие дата") {
			t.Fatalf("%+v %v want=%q", m, err, want)
		}
	}
	check("System")
	set := func(text string, second int64) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(second, 0)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	set(string(rune(0xfeff))+"  Первый промпт\n", 100)
	check("Первый промпт")
	set("Второй промпт", 101)
	check("Второй промпт")
	set("Ignored at same mtime", 101)
	check("Второй промпт")
	set(" \n ", 102)
	check("System")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("System")
	set("\xff", 103)
	if _, err := d.messages("a", "text"); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
func TestEcho(t *testing.T) {
	d := New(Config{BaseURL: "echo"})
	out, err := collect(d, "a", "**Hi**")
	if err != nil || !reflect.DeepEqual(out, []string{"Вы сказали: **Hi**", "Это тестовый ответ."}) || len(d.history) != 0 || len(d.last) != 0 {
		t.Fatalf("%v %+v", err, out)
	}
	sentinel := errors.New("callback")
	if err := d.Stream(context.Background(), "a", "text", func(string) error { return sentinel }); err != sentinel {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Stream(ctx, "a", "text", func(string) error { t.Fatal("callback after cancel"); return nil }); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestSSELineHandling(t *testing.T) {
	var out []string
	input := "data: null\r\ndata: {\"choices\":[null]}\r\ndata: {\"choices\":[{\"delta\":null}]}\ndata: {\"choices\":[{\"delta\":{\"content\":123}}]}\n" +
		`data: {"choices":[{"delta":{"content":"final"}}]}`
	if err := readSSE(strings.NewReader(input), func(s string) error { out = append(out, s); return nil }); err != nil || !reflect.DeepEqual(out, []string{"final"}) {
		t.Fatalf("%v %q", err, out)
	}
	// No Scanner token-length limit (large provider chunks are legal).
	huge := strings.Repeat("x", 100000)
	var body strings.Builder
	writeData := func(s string) {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": s}}}})
		_, _ = fmt.Fprintf(&body, "data: %s\n", data)
	}
	writeData(huge)
	body.WriteString("data: [DONE]\n")
	writeData("ignored")
	out = nil
	if err := readSSE(strings.NewReader(body.String()), func(s string) error { out = append(out, s); return nil }); err != nil || !reflect.DeepEqual(out, []string{huge}) {
		t.Fatal("large line / DONE", err)
	}
}
func TestSSEUniversalNewlines(t *testing.T) {
	for _, newline := range []string{"\r", "\r\n", "\n"} {
		input := `data: {"choices":[{"delta":{"content":"one"}}]}` + newline + `data: {"choices":[{"delta":{"content":"two"}}]}` + newline + "data: [DONE]" + newline
		var out []string
		if err := readSSE(strings.NewReader(input), func(s string) error { out = append(out, s); return nil }); err != nil || !reflect.DeepEqual(out, []string{"one", "two"}) {
			t.Fatalf("newline %q: %v %q", newline, err, out)
		}
	}
}

func TestConcurrentDevices(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeTokens(w, "Ответ для устройства.")
	}))
	defer server.Close()
	d := New(testConfig(server.URL))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprint(i)
			if _, err := collect(d, key, "text"); err != nil {
				t.Error(err)
			}
			d.Reset(key)
		}(i)
	}
	wg.Wait()
	if hits.Load() != 20 || len(d.history) != 0 {
		t.Fatalf("%d %+v", hits.Load(), d.history)
	}
}
