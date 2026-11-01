// SPDX-License-Identifier: GPL-3.0-only

package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sdkServer(t *testing.T, jsonResponse bool, tools []*mcp.Tool, handler mcp.ToolHandler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	sdk := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{PageSize: 4})
	for _, tool := range tools {
		sdk.AddTool(tool, handler)
	}
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	initializations := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			if bytes.Contains(data, []byte(`"method":"initialize"`)) {
				initializations.Add(1)
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		transport.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, initializations
}

func exampleTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: "Echo an integer", InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","minimum":1}},"required":["n"],"additionalProperties":false}`)}
}

func newClient(t *testing.T, servers ...Server) *Client {
	t.Helper()
	c, err := New(servers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSDKDiscoveryAndCall(t *testing.T) {
	for _, jsonResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(jsonResponse), func(t *testing.T) {
			var calls atomic.Int32
			server, initializations := sdkServer(t, jsonResponse, []*mcp.Tool{exampleTool("echo"), exampleTool("hidden")}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
			})
			c := newClient(t, Server{Name: "local", URL: server.URL, AllowedTools: []string{"echo"}})
			tools, err := c.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 1 || tools[0].Name != "mcp_local__echo" || tools[0].Parameters == nil {
				t.Fatalf("tools: %+v", tools)
			}
			for _, invalid := range []string{`null`, `[]`, `{"n":0}`, `{"n":"1"}`, `{}`, `{"n":1,"extra":true}`, `{"n":1} {}`, strings.Repeat(" ", maxArguments+1)} {
				if _, err := c.Call(context.Background(), tools[0].Name, json.RawMessage(invalid)); err == nil {
					t.Errorf("accepted %q", invalid)
				}
			}
			if _, err := c.Call(context.Background(), "mcp_local__hidden", json.RawMessage(`{"n":1}`)); err == nil {
				t.Fatal("called hidden tool")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid args reached server")
			}
			text, err := c.Call(context.Background(), tools[0].Name, json.RawMessage(`{"n":1}`))
			if err != nil || text != `{"n":1}` {
				t.Fatalf("call: %q %v", text, err)
			}
			// Mutating returned metadata must not alter private validation state.
			tools[0].Parameters.(map[string]any)["type"] = "array"
			if _, err := c.Discover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if initializations.Load() != 1 {
				t.Fatalf("sessions not reused: %d", initializations.Load())
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.Call(context.Background(), "mcp_local__echo", json.RawMessage(`{"n":2}`)); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Discover(context.Background()); err == nil {
				t.Fatal("discovery after close")
			}
		})
	}
}

func TestDefaultAllDiscoveryAndCall(t *testing.T) {
	for _, allowed := range []struct {
		name  string
		tools []string
	}{
		{name: "nil"},
		{name: "empty", tools: []string{}},
	} {
		for _, jsonResponse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", allowed.name, jsonResponse), func(t *testing.T) {
				var calls atomic.Int32
				server, _ := sdkServer(t, jsonResponse, []*mcp.Tool{exampleTool("echo"), exampleTool("other")}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					calls.Add(1)
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: req.Params.Name}}}, nil
				})
				c := newClient(t, Server{Name: "local", URL: server.URL, AllowedTools: allowed.tools})
				tools, err := c.Discover(context.Background())
				if err != nil || len(tools) != 2 {
					t.Fatalf("tools: %+v, error: %v", tools, err)
				}
				seen := make(map[string]bool)
				for _, tool := range tools {
					seen[tool.Name] = true
					if tool.Parameters == nil {
						t.Fatalf("missing schema: %+v", tool)
					}
					if _, err := c.Call(context.Background(), tool.Name, json.RawMessage(`{"n":0}`)); err == nil {
						t.Fatal("default-all bypassed argument validation")
					}
					text, err := c.Call(context.Background(), tool.Name, json.RawMessage(`{"n":1}`))
					if err != nil || "mcp_local__"+text != tool.Name {
						t.Fatalf("call %s: %q %v", tool.Name, text, err)
					}
				}
				if !seen["mcp_local__echo"] || !seen["mcp_local__other"] || calls.Load() != 2 {
					t.Fatalf("tools: %v, calls: %d", seen, calls.Load())
				}
				if _, err := c.Call(context.Background(), "mcp_local__undiscovered", json.RawMessage(`{"n":1}`)); err == nil {
					t.Fatal("called undiscovered tool")
				}
			})
		}
	}
}

func TestDefaultAllMultipleServers(t *testing.T) {
	servers := make([]Server, 0, 3)
	for _, name := range []string{"first", "second", "explicit"} {
		server, _ := sdkServer(t, true, []*mcp.Tool{exampleTool("echo"), exampleTool("other")}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + "/" + req.Params.Name}}}, nil
		})
		config := Server{Name: name, URL: server.URL}
		switch name {
		case "second":
			config.AllowedTools = []string{}
		case "explicit":
			config.AllowedTools = []string{"echo"}
		}
		servers = append(servers, config)
	}
	c := newClient(t, servers...)
	tools, err := c.Discover(context.Background())
	if err != nil || len(tools) != 5 {
		t.Fatalf("tools: %+v, error: %v", tools, err)
	}
	for _, config := range servers {
		for _, remote := range []string{"echo", "other"} {
			text, err := c.Call(context.Background(), functionName(config.Name, remote), json.RawMessage(`{"n":1}`))
			if config.Name == "explicit" && remote == "other" {
				if err == nil {
					t.Fatal("default-all on another endpoint widened explicit allowlist")
				}
				continue
			}
			if err != nil || text != config.Name+"/"+remote {
				t.Fatalf("call %s/%s: %q %v", config.Name, remote, text, err)
			}
		}
	}
}

func TestDefaultAllDiscoveryCollisions(t *testing.T) {
	for _, test := range []struct {
		name    string
		servers []Server
		tools   [][]*mcp.Tool
	}{
		{"same endpoint", []Server{{Name: "test"}}, [][]*mcp.Tool{{exampleTool("read.x"), exampleTool("read-x")}}},
		{"across endpoints", []Server{{Name: "a.b"}, {Name: "a_b", AllowedTools: []string{}}}, [][]*mcp.Tool{{exampleTool("echo")}, {exampleTool("echo")}}},
		{"default then explicit", []Server{{Name: "a.b"}, {Name: "a_b", AllowedTools: []string{"echo"}}}, [][]*mcp.Tool{{exampleTool("echo")}, {exampleTool("echo")}}},
		{"explicit then default", []Server{{Name: "a.b", AllowedTools: []string{"echo"}}, {Name: "a_b"}}, [][]*mcp.Tool{{exampleTool("echo")}, {exampleTool("echo")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			for i := range test.servers {
				server, _ := sdkServer(t, true, test.tools[i], func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					calls.Add(1)
					return &mcp.CallToolResult{}, nil
				})
				test.servers[i].URL = server.URL
			}
			c := newClient(t, test.servers...)
			tools, err := c.Discover(context.Background())
			if err == nil || !strings.Contains(err.Error(), "namespaced tool collision") || len(tools) != 0 {
				t.Fatalf("tools: %+v, error: %v", tools, err)
			}
			name := functionName(test.servers[0].Name, test.tools[0][0].Name)
			if _, err := c.Call(context.Background(), name, json.RawMessage(`{"n":1}`)); err == nil || calls.Load() != 0 {
				t.Fatal("collision left callable tools")
			}
		})
	}
}

func TestMultipleServers(t *testing.T) {
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	a, _ := sdkServer(t, true, []*mcp.Tool{exampleTool("echo")}, handler)
	b, _ := sdkServer(t, false, []*mcp.Tool{exampleTool("echo")}, handler)
	c := newClient(t, Server{Name: "a", URL: a.URL, AllowedTools: []string{"echo"}}, Server{Name: "b", URL: b.URL, AllowedTools: []string{"echo"}})
	tools, err := c.Discover(context.Background())
	if err != nil || len(tools) != 2 {
		t.Fatalf("discovery: %+v %v", tools, err)
	}
	if tools[0].Name == tools[1].Name {
		t.Fatal("namespace collision")
	}
	for _, tool := range tools {
		if _, err := c.Call(context.Background(), tool.Name, json.RawMessage(`{"n":1}`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguration(t *testing.T) {
	good := Server{Name: "test", URL: "http://127.0.0.1:1234/mcp", AllowedTools: []string{"echo"}}
	cases := []Server{
		{Name: "bad name", URL: good.URL, AllowedTools: good.AllowedTools},
		{Name: "test", URL: "http://example.com/mcp", AllowedTools: good.AllowedTools},
		{Name: "test", URL: "file:///tmp/mcp", AllowedTools: good.AllowedTools},
		{Name: "test", URL: "https://user:pass@example.com/mcp", AllowedTools: good.AllowedTools},
		{Name: "test", URL: "https://example.com/mcp?secret=1", AllowedTools: good.AllowedTools},
		{Name: "test", URL: "https://example.com/mcp?", AllowedTools: good.AllowedTools},
		{Name: "test", URL: "https://example.com/mcp#", AllowedTools: good.AllowedTools},
		{Name: "test", URL: good.URL, AllowedTools: []string{"echo", "echo"}},
		{Name: "test", URL: good.URL, AllowedTools: []string{"*"}},
		{Name: "test", URL: good.URL, AllowedTools: good.AllowedTools, TokenEnv: "BAD-NAME"},
		{Name: "test", URL: good.URL, AllowedTools: good.AllowedTools, TokenEnv: "QUASARD_TEST_MISSING_TOKEN"},
	}
	t.Setenv("QUASARD_TEST_MISSING_TOKEN", "")
	for _, s := range cases {
		if c, err := New([]Server{s}); err == nil {
			_ = c.Close()
			t.Errorf("accepted %+v", s)
		}
	}
	if c, err := New([]Server{good, good}); err == nil {
		_ = c.Close()
		t.Fatal("duplicate server accepted")
	}
	collision := good
	collision.Name = "other"
	collision.AllowedTools = []string{"foo.bar", "foo-bar"}
	if c, err := New([]Server{collision}); err == nil {
		_ = c.Close()
		t.Fatal("normalized collision accepted")
	}
	for _, endpoint := range []string{"https://example.com/mcp", "http://[::1]:1234/mcp"} {
		s := good
		s.URL = endpoint
		c, err := New([]Server{s})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	servers := make([]Server, maxServers+1)
	for i := range servers {
		servers[i] = good
		servers[i].Name = fmt.Sprintf("s%d", i)
	}
	if c, err := New(servers); err == nil {
		_ = c.Close()
		t.Fatal("too many servers accepted")
	}
	s := good
	s.AllowedTools = nil
	for i := 0; i < maxTools+1; i++ {
		s.AllowedTools = append(s.AllowedTools, fmt.Sprintf("tool%d", i))
	}
	if c, err := New([]Server{s}); err == nil {
		_ = c.Close()
		t.Fatal("too many tools accepted")
	}
	name := functionName(strings.Repeat("s", 128), strings.Repeat("t", 128))
	if len(name) > 64 || !safeEnv.MatchString(name) {
		t.Fatalf("bad long namespace %q", name)
	}
	c := newClient(t)
	tools, err := c.Discover(context.Background())
	if err != nil || len(tools) != 0 {
		t.Fatalf("disabled client: %v %v", tools, err)
	}
}

func TestTokenAndRedirect(t *testing.T) {
	const secret = "test-secret-never-forward"
	t.Setenv("QUASARD_TEST_TOKEN", secret)
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing bearer token")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := newClient(t, Server{Name: "test", URL: source.URL, TokenEnv: "QUASARD_TEST_TOKEN", AllowedTools: []string{"echo"}})
	_, err := c.Discover(context.Background())
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("token leaked")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect destination contacted")
	}
}

func TestResultBoundsAndToolError(t *testing.T) {
	for _, test := range []struct {
		name   string
		result *mcp.CallToolResult
	}{
		{"oversize", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxResult+1)}}}},
		{"error", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "remote failure"}}}},
		{"structured", &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := sdkServer(t, false, []*mcp.Tool{exampleTool("echo")}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return test.result, nil })
			c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: []string{"echo"}})
			tools, err := c.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Call(context.Background(), tools[0].Name, json.RawMessage(`{"n":1}`))
			if test.name == "structured" {
				if err != nil || !strings.Contains(result, `"structuredContent":{"ok":true}`) {
					t.Fatalf("%q %v", result, err)
				}
			} else if err == nil || result != "" {
				t.Fatalf("accepted result %q %v", result, err)
			}
		})
	}
}

func TestSchemaBoundsAndNoRemoteFetch(t *testing.T) {
	var remoteCalls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		_, _ = io.WriteString(w, `{"type":"integer"}`)
	}))
	defer remote.Close()
	for _, schema := range []string{
		`{"type":"object","properties":{"n":{"$ref":"` + remote.URL + `/schema"}}}`,
		`{"type":"array"}`,
		`{"type":"object","description":"` + strings.Repeat("x", maxSchema) + `"}`,
	} {
		tool := exampleTool("echo")
		tool.InputSchema = json.RawMessage(schema)
		if _, _, err := prepareTool("test", tool); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
	if remoteCalls.Load() != 0 {
		t.Fatal("external schema fetched")
	}
	tool := exampleTool("echo")
	tool.Description = strings.Repeat("x", maxDescription+1)
	if _, _, err := prepareTool("test", tool); err == nil {
		t.Fatal("oversized description accepted")
	}
}

func TestDiscoveryFailsClosed(t *testing.T) {
	server, _ := sdkServer(t, true, []*mcp.Tool{exampleTool("echo")}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: []string{"echo", "missing"}})
	if tools, err := c.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "allowlisted tool missing") || len(tools) != 0 {
		t.Fatalf("missing allowlisted tool: tools %+v, error %v", tools, err)
	}
	if _, err := c.Call(context.Background(), "mcp_test__echo", json.RawMessage(`{"n":1}`)); err == nil {
		t.Fatal("partial discovery callable")
	}
}

func TestBodyBounds(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint(sse), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush() // unknown content length: exercise streaming bound.
				if sse {
					_, _ = io.WriteString(w, ":"+strings.Repeat("x", maxBody+1)+"\n\n")
				} else {
					_, _ = io.WriteString(w, strings.Repeat("x", maxBody+1))
				}
			}))
			defer server.Close()
			base := http.DefaultTransport.(*http.Transport).Clone()
			defer base.CloseIdleConnections()
			transport := &boundedTransport{base: base, endpoint: server.URL, life: context.Background()}
			req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
			response, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			data, err := io.ReadAll(response.Body)
			if err != errBodyTooLarge || len(data) != maxBody {
				t.Fatalf("body len=%d err=%v", len(data), err)
			}
		})
	}
	// Exact limit is accepted, not treated as overflow.
	body := &boundedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", maxBody))), remaining: maxBody, cleanup: func() {}}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || len(data) != maxBody {
		t.Fatalf("exact bound: %d %v", len(data), err)
	}
}

func TestTimeoutAndCloseCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	c := newClient(t, Server{Name: "test", URL: server.URL, AllowedTools: []string{"echo"}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Discover(ctx); err == nil {
		t.Fatal("timeout ignored")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not propagated")
	}
	<-entered
	result := make(chan error, 1)
	go func() { _, err := c.Discover(context.Background()); result <- err }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel operation")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("operation succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("operation leaked")
	}
}
