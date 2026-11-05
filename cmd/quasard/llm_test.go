// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/llm"
)

func TestLoadedDeviceProfilesReachConfiguredEndpoint(t *testing.T) {
	hitsA, hitsB := 0, 0
	serve := func(counter *int, key string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			*counter++
			if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
				t.Error("wrong endpoint or key")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello.\"}}]}\n\ndata: [DONE]\n\n")
		}
	}
	a := httptest.NewServer(serve(&hitsA, "common-key"))
	defer a.Close()
	b := httptest.NewServer(serve(&hitsB, "kitchen-key"))
	defer b.Close()
	t.Setenv("KITCHEN_API_KEY", "kitchen-key")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := fmt.Sprintf(`[llm]
base_url=%q
api_key="common-key"
system_prompt_file=""
[devices."id-kitchen".llm]
base_url=%q
api_key_env="KITCHEN_API_KEY"
model="kitchen-model"
`, a.URL+"/v1", b.URL+"/v1")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := dialogConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	dialog := llm.New(lc)
	for _, id := range []string{"id-kitchen", "unlisted"} {
		if err := dialog.Stream(context.Background(), id, "hello", func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if hitsA != 1 || hitsB != 1 {
		t.Fatalf("endpoint hits=%d/%d", hitsA, hitsB)
	}
}
