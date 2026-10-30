// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func fakeLLMHandler(delay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) == 0 {
			http.Error(w, "invalid chat request", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", 500)
			return
		}
		answer := []rune("<think>думаю</think>Вы спросили: " + body.Messages[len(body.Messages)-1].Content + " Это проверочный ответ, он приходит по кусочкам, как у настоящей модели. Озвучка должна начаться ещё до того, как текст закончится. Что-нибудь ещё?")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		for i := 0; i < len(answer); i += 4 {
			end := i + 4
			if end > len(answer) {
				end = len(answer)
			}
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": string(answer[i:end])}}}})
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			return
		}
		flusher.Flush()
	})
	return mux
}
func fakeLLM(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	host := f.String("host", "127.0.0.1", "loopback listen IP")
	port := f.Int("port", 8099, "listen port (0 selects free port)")
	delay := f.Duration("delay", 20*time.Millisecond, "delay between four-character chunks")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := noArgs(f); e != nil {
		return e
	}
	ip := net.ParseIP(*host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("fake-llm only listens on loopback IPs")
	}
	if *port < 0 || *port > 65535 || *delay < 0 {
		return fmt.Errorf("invalid port or delay")
	}
	listener, e := net.Listen("tcp", net.JoinHostPort(*host, fmt.Sprint(*port)))
	if e != nil {
		return e
	}
	server := &http.Server{Handler: fakeLLMHandler(*delay), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	if _, e := fmt.Fprintln(out, "Local fake LLM listening:", listener.Addr(), "(no upstream requests)"); e != nil {
		_ = listener.Close()
		return e
	}
	e = server.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
