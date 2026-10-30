// SPDX-License-Identifier: GPL-3.0-only

package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSDKPaginationAndToolLimit(t *testing.T) {
	for _, allowed := range []struct {
		name  string
		tools []string
	}{
		{name: "explicit", tools: []string{"tool00"}},
		{name: "nil"},
		{name: "empty", tools: []string{}},
	} {
		for _, count := range []int{maxTools, maxTools + 1} {
			t.Run(fmt.Sprintf("%s/%d", allowed.name, count), func(t *testing.T) {
				tools := make([]*mcp.Tool, count)
				for i := range tools {
					tools[i] = exampleTool(fmt.Sprintf("tool%02d", i))
				}
				server, _ := sdkServer(t, true, tools, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
				c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: allowed.tools})
				got, err := c.Discover(context.Background())
				if count == maxTools {
					want := maxTools
					if len(allowed.tools) > 0 {
						want = len(allowed.tools)
					}
					if err != nil || len(got) != want {
						t.Fatalf("%+v %v", got, err)
					}
				} else if err == nil {
					t.Fatal("advertised tool/page bound ignored")
				}
			})
		}
	}
}

func TestDefaultAllAggregateToolLimit(t *testing.T) {
	for _, count := range []int{maxTools, maxTools + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			servers := make([]Server, 0, 2)
			for i, size := range []int{maxTools / 2, count - maxTools/2} {
				tools := make([]*mcp.Tool, size)
				for j := range tools {
					tools[j] = exampleTool(fmt.Sprintf("tool%02d", j))
				}
				server, _ := sdkServer(t, true, tools, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
				servers = append(servers, Server{Name: fmt.Sprintf("server%d", i), URL: server.URL})
			}
			c := newClient(t, servers...)
			tools, err := c.Discover(context.Background())
			if count == maxTools {
				if err != nil || len(tools) != maxTools {
					t.Fatalf("tools: %+v, error: %v", tools, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "too many discovered tools") || len(tools) != 0 {
					t.Fatalf("tools: %+v, error: %v", tools, err)
				}
				if _, err := c.Call(context.Background(), functionName(servers[0].Name, "tool00"), json.RawMessage(`{"n":1}`)); err == nil {
					t.Fatal("aggregate limit left callable tools")
				}
			}
		})
	}
}

func TestPaginationBoundAndRepeat(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprint(repeat), func(t *testing.T) {
			var pages atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					return
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "invalid test request", http.StatusBadRequest)
					return
				}
				if len(req.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := any(map[string]any{})
				switch req.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
				case "tools/list":
					page := pages.Add(1)
					cursor := fmt.Sprint(page)
					if repeat {
						cursor = "same"
					}
					result = map[string]any{"tools": []any{}, "nextCursor": cursor}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: []string{"echo"}})
			if _, err := c.Discover(context.Background()); err == nil {
				t.Fatal("unbounded pagination accepted")
			}
			expected := int32(maxPages)
			if repeat {
				expected = 2
			}
			if pages.Load() != expected {
				t.Fatalf("pages=%d want=%d", pages.Load(), expected)
			}
		})
	}
}

func TestDiscoveryBodyOverflow(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint(sse), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, strings.Repeat("x", maxBody+1))
			}))
			defer server.Close()
			c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: []string{"echo"}})
			if _, err := c.Discover(context.Background()); err == nil {
				t.Fatal("oversized discovery accepted")
			}
		})
	}
}

func TestSchemaComplexity(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","$ref":"#"}`,
		`{"type":"object","$defs":{"x":{"$ref":"#/$defs/x"}},"properties":{"n":{"$ref":"#/$defs/x"}}}`,
		`{"type":"object","oneOf":[{},{},{},{},{},{},{},{},{}]}`,
	} {
		tool := exampleTool("echo")
		tool.InputSchema = json.RawMessage(schema)
		if _, _, err := prepareTool("test", tool); err == nil {
			t.Fatal("unbounded schema accepted")
		}
	}
}
