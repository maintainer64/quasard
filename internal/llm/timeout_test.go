// SPDX-License-Identifier: GPL-3.0-only

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTimeoutIsReadInactivityNotTotalLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 5; i++ {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "Готовая длинная фраза. "}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(30 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Timeout = 100 * time.Millisecond
	d := New(cfg)
	calls := 0
	if err := d.Stream(context.Background(), "a", "text", func(string) error { calls++; time.Sleep(110 * time.Millisecond); return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || len(d.history["a"]) != 2 {
		t.Fatalf("calls=%d history=%+v", calls, d.history)
	}
}
