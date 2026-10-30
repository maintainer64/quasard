// SPDX-License-Identifier: GPL-3.0-only

// Package mcpclient provides an explicitly configured, HTTP-only MCP tool client.
package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	operationTimeout = 10 * time.Second
	maxServers       = 8
	maxTools         = 32
	maxPages         = 8
	maxBody          = 1 << 20
	maxArguments     = 16 << 10
	maxResult        = 16 << 10
	maxSchema        = 16 << 10
	maxDescription   = 4 << 10
)

var (
	safeName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	safeEnv         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	errBodyTooLarge = errors.New("MCP response body exceeds 1 MiB")
)

// Server describes one explicitly configured, trusted endpoint. An empty
// AllowedTools permits all discovered tools; a nonempty list is an exact allowlist.
type Server struct {
	Name         string
	URL          string
	TokenEnv     string
	AllowedTools []string
}

// Tool is a discovered, namespaced function definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type endpoint struct {
	config    Server
	transport *boundedTransport
	session   *mcp.ClientSession
}

type binding struct {
	endpoint *endpoint
	remote   string
	schema   *jsonschema.Resolved
}

// Client reuses MCP sessions. Its methods are concurrency safe; operations are
// serialized with context-aware admission. Discover must succeed before Call.
type Client struct {
	gate      chan struct{}
	life      context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	closeDone chan struct{}
	closeErr  error
	endpoints []*endpoint
	tools     map[string]binding
}

// New validates configuration without connecting. Tokens are read only from the
// named environment variable; a configured but missing/empty token is an error.
func New(servers []Server) (*Client, error) {
	if len(servers) > maxServers {
		return nil, fmt.Errorf("MCP: at most %d servers allowed", maxServers)
	}
	life, cancel := context.WithCancel(context.Background())
	c := &Client{gate: make(chan struct{}, 1), life: life, cancel: cancel, closeDone: make(chan struct{})}
	fail := func(err error) (*Client, error) { cancel(); return nil, err }
	names := make(map[string]bool)
	functions := make(map[string]bool)
	total := 0
	for _, s := range servers {
		if !safeName.MatchString(s.Name) || names[s.Name] {
			return fail(errors.New("MCP: invalid or duplicate server name"))
		}
		names[s.Name] = true
		u, err := url.Parse(s.URL)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(s.URL, "#") || u.Opaque != "" {
			return fail(fmt.Errorf("MCP server %s: invalid HTTP endpoint", s.Name))
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fail(fmt.Errorf("MCP server %s: invalid endpoint port", s.Name))
			}
		}
		if strings.HasSuffix(u.Host, ":") {
			return fail(fmt.Errorf("MCP server %s: empty endpoint port", s.Name))
		}
		if u.Scheme == "http" {
			// Hostnames are not accepted here: even localhost can be remapped. A
			// literal loopback address keeps plaintext credentials on this machine.
			ip := net.ParseIP(u.Hostname())
			if ip == nil || !ip.IsLoopback() {
				return fail(fmt.Errorf("MCP server %s: HTTP requires a loopback IP literal", s.Name))
			}
		}
		allowed := make(map[string]bool)
		for _, name := range s.AllowedTools {
			if !safeName.MatchString(name) || allowed[name] {
				return fail(fmt.Errorf("MCP server %s: invalid or duplicate allowed tool", s.Name))
			}
			allowed[name] = true
			function := functionName(s.Name, name)
			if functions[function] {
				return fail(errors.New("MCP: namespaced tool collision"))
			}
			functions[function] = true
			total++
		}
		if total > maxTools {
			return fail(fmt.Errorf("MCP: at most %d allowed tools", maxTools))
		}
		token := ""
		if s.TokenEnv != "" {
			if !safeEnv.MatchString(s.TokenEnv) {
				return fail(fmt.Errorf("MCP server %s: invalid token environment name", s.Name))
			}
			token = os.Getenv(s.TokenEnv)
			if token == "" || strings.ContainsAny(token, "\r\n") {
				return fail(fmt.Errorf("MCP server %s: missing or invalid bearer token", s.Name))
			}
		}
		s.AllowedTools = append([]string(nil), s.AllowedTools...)
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil // Explicit endpoints only, not environment-selected proxies.
		base.ResponseHeaderTimeout = operationTimeout
		base.TLSHandshakeTimeout = operationTimeout
		base.MaxResponseHeaderBytes = 32 << 10
		base.DialContext = (&net.Dialer{Timeout: operationTimeout, KeepAlive: 30 * time.Second}).DialContext
		c.endpoints = append(c.endpoints, &endpoint{config: s, transport: &boundedTransport{base: base, endpoint: s.URL, token: token, life: life}})
	}
	return c, nil
}

func functionName(server, tool string) string {
	normalize := func(s string) string { return strings.NewReplacer(".", "_", "-", "_").Replace(s) }
	name := "mcp_" + normalize(server) + "__" + normalize(tool)
	if len(name) > 64 {
		sum := sha256.Sum256([]byte(server + "\x00" + tool))
		name = name[:47] + "_" + hex.EncodeToString(sum[:8])
	}
	return name
}

func (c *Client) enter(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	stop := context.AfterFunc(c.life, cancel)
	done := func() { stop(); cancel() }
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		done()
		return nil, nil, ctx.Err()
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		<-c.gate
		done()
		return nil, nil, errors.New("MCP client is closed")
	}
	if err := ctx.Err(); err != nil {
		<-c.gate
		done()
		return nil, nil, err
	}
	for _, e := range c.endpoints {
		e.transport.mu.Lock()
		e.transport.operationContext = ctx
		e.transport.mu.Unlock()
	}
	return ctx, func() {
		for _, e := range c.endpoints {
			e.transport.mu.Lock()
			e.transport.operationContext = nil
			e.transport.mu.Unlock()
		}
		<-c.gate
		done()
	}, nil
}

// Discover lists all tools from configured endpoints unless narrowed by an
// allowlist. Any server, pagination, schema, or allowlist mismatch fails closed
// and invalidates the previous discovery.
func (c *Client) Discover(ctx context.Context) ([]Tool, error) {
	ctx, done, err := c.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c.tools = nil
	bindings := make(map[string]binding)
	tools := make([]Tool, 0)
	for _, e := range c.endpoints {
		if e.session == nil {
			sdk := mcp.NewClient(&mcp.Implementation{Name: "quasard", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
			e.transport.mu.Lock()
			e.transport.connectContext = ctx
			e.transport.mu.Unlock()
			transport := &trackedTransport{Transport: &mcp.StreamableClientTransport{Endpoint: e.config.URL, HTTPClient: &http.Client{Transport: e.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP redirects are disabled") }}, MaxRetries: -1}}
			session, err := sdk.Connect(ctx, transport, nil)
			// v1.2.0 does not close its connection on every initialization error
			// (notably unsupported protocol versions). Own that cleanup too.
			if err != nil && transport.connection != nil {
				_ = transport.connection.Close()
			}
			e.transport.mu.Lock()
			e.transport.connectContext = nil
			e.transport.mu.Unlock()
			if err != nil {
				return nil, fmt.Errorf("MCP server %s: connection failed", e.config.Name)
			}
			e.session = session
		}
		wanted := make(map[string]bool)
		for _, name := range e.config.AllowedTools {
			wanted[name] = true
		}
		seen := make(map[string]bool)
		cursors := make(map[string]bool)
		cursor := ""
		listed := 0
		for page := 0; ; page++ {
			if page >= maxPages {
				return nil, fmt.Errorf("MCP server %s: too many tool pages", e.config.Name)
			}
			result, err := e.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return nil, fmt.Errorf("MCP server %s: tool discovery failed", e.config.Name)
			}
			if result == nil {
				return nil, fmt.Errorf("MCP server %s: empty tool response", e.config.Name)
			}
			listed += len(result.Tools)
			if listed > maxTools {
				return nil, fmt.Errorf("MCP server %s: too many advertised tools", e.config.Name)
			}
			for _, remote := range result.Tools {
				if remote == nil || !safeName.MatchString(remote.Name) || seen[remote.Name] {
					return nil, fmt.Errorf("MCP server %s: invalid or duplicate tool name", e.config.Name)
				}
				seen[remote.Name] = true
				if len(wanted) > 0 && !wanted[remote.Name] {
					continue
				}
				tool, schema, err := prepareTool(e.config.Name, remote)
				if err != nil {
					return nil, err
				}
				if _, exists := bindings[tool.Name]; exists {
					return nil, errors.New("MCP: namespaced tool collision")
				}
				bindings[tool.Name] = binding{endpoint: e, remote: remote.Name, schema: schema}
				tools = append(tools, tool)
				if len(tools) > maxTools {
					return nil, errors.New("MCP: too many discovered tools")
				}
			}
			cursor = result.NextCursor
			if cursor == "" {
				break
			}
			if len(cursor) > 1024 || cursors[cursor] {
				return nil, fmt.Errorf("MCP server %s: invalid tool pagination", e.config.Name)
			}
			cursors[cursor] = true
		}
		for name := range wanted {
			if !seen[name] {
				return nil, fmt.Errorf("MCP server %s: allowlisted tool missing", e.config.Name)
			}
		}
	}
	c.tools = bindings
	return tools, nil
}

func prepareTool(server string, remote *mcp.Tool) (Tool, *jsonschema.Resolved, error) {
	fail := func() (Tool, *jsonschema.Resolved, error) {
		return Tool{}, nil, fmt.Errorf("MCP server %s: invalid or oversized tool schema/description", server)
	}
	if len(remote.Description) > maxDescription {
		return fail()
	}
	data, err := json.Marshal(remote.InputSchema)
	if err != nil || len(data) > maxSchema {
		return fail()
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil || object["type"] != "object" {
		return fail()
	}
	if !boundedSchema(object) {
		return fail()
	}
	var schema jsonschema.Schema
	if json.Unmarshal(data, &schema) != nil {
		return fail()
	}
	// No Loader: external schema references fail instead of making network calls.
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fail()
	}
	return Tool{Name: functionName(server, remote.Name), Description: remote.Description, Parameters: object}, resolved, nil
}

// boundedSchema limits validator work. jsonschema-go v0.3.0 does not bound
// recursive references or combinator expansion, so this client accepts inline
// schemas only and caps their depth, nodes, and branching keywords. No external
// schema loader is installed, either.
func boundedSchema(value any) bool {
	nodes, branches := 0, 0
	var visit func(any, int) bool
	visit = func(v any, depth int) bool {
		nodes++
		if depth > 16 || nodes > 256 {
			return false
		}
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				switch key {
				case "$ref", "$dynamicRef", "$recursiveRef":
					return false
				case "allOf", "anyOf", "oneOf":
					if choices, ok := child.([]any); ok {
						branches += len(choices)
					} else {
						return false
					}
				case "if", "not":
					branches++
				}
				if branches > 8 || !visit(child, depth+1) {
					return false
				}
			}
		case []any:
			for _, child := range v {
				if !visit(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return visit(value, 0)
}

// Call validates a JSON object against the discovered input schema before any
// request. Text-only results are joined with newlines; other results are JSON.
// Oversized or tool-error results are rejected, never truncated.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	ctx, done, err := c.enter(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	b, ok := c.tools[name]
	if !ok {
		return "", errors.New("MCP: tool is not discovered or allowed")
	}
	if len(args) > maxArguments {
		return "", errors.New("MCP: arguments exceed 16 KiB")
	}
	var object map[string]any
	if json.Unmarshal(args, &object) != nil || object == nil {
		return "", errors.New("MCP: arguments must be a JSON object")
	}
	if err := b.schema.Validate(object); err != nil {
		return "", errors.New("MCP: arguments do not match tool schema")
	}
	result, err := b.endpoint.session.CallTool(ctx, &mcp.CallToolParams{Name: b.remote, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("MCP server %s: tool call failed", b.endpoint.config.Name)
	}
	if result == nil {
		return "", errors.New("MCP: empty tool result")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", errors.New("MCP: invalid tool result")
	}
	if len(data) > maxResult {
		return "", errors.New("MCP: tool result exceeds 16 KiB")
	}
	if result.IsError {
		return "", errors.New("MCP: tool reported an error")
	}
	if result.StructuredContent == nil {
		texts := make([]string, 0, len(result.Content))
		for _, content := range result.Content {
			text, ok := content.(*mcp.TextContent)
			if !ok || text == nil {
				return string(data), nil
			}
			texts = append(texts, text.Text)
		}
		return strings.Join(texts, "\n"), nil
	}
	return string(data), nil
}

// Close cancels pending operations, closes sessions and idle connections, and is
// idempotent. Concurrent callers receive the same close result.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.closeDone
		return c.closeErr
	}
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.gate <- struct{}{}
	defer func() { <-c.gate; close(c.closeDone) }()
	var wg sync.WaitGroup
	errs := make(chan error, len(c.endpoints))
	for _, e := range c.endpoints {
		wg.Add(1)
		go func(e *endpoint) {
			defer wg.Done()
			if e.session != nil {
				if err := e.session.Close(); err != nil {
					errs <- fmt.Errorf("MCP server %s: session close failed", e.config.Name)
				}
			}
			e.transport.base.CloseIdleConnections()
		}(e)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		c.closeErr = errors.Join(c.closeErr, err)
	}
	c.tools = nil
	return c.closeErr
}

type trackedTransport struct {
	mcp.Transport
	connection mcp.Connection
}

func (t *trackedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	t.connection = connection
	return connection, err
}

// boundedTransport enforces limits while preserving incremental POST SSE reads.
// The optional unsolicited GET stream is disabled: tools do not need it, and a
// permanently open stream conflicts with finite HTTP operation deadlines.
type boundedTransport struct {
	base             *http.Transport
	endpoint         string
	token            string
	life             context.Context
	mu               sync.Mutex
	connectContext   context.Context // bounds SDK cleanup after failed initialization
	operationContext context.Context // SDK writes can detach caller cancellation
}

func (t *boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.endpoint {
		return nil, errors.New("MCP: unconfigured endpoint")
	}
	if req.Method == http.MethodGet {
		return &http.Response{StatusCode: http.StatusMethodNotAllowed, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}
	if req.Method != http.MethodPost && req.Method != http.MethodDelete {
		return nil, errors.New("MCP: unsupported HTTP method")
	}
	parent := req.Context()
	if req.Method == http.MethodDelete {
		t.mu.Lock()
		if t.connectContext != nil {
			parent = t.connectContext
		}
		t.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, errors.New("MCP HTTP operation canceled")
	}
	stop := func() bool { return false }
	stopOperation := func() bool { return false }
	if req.Method != http.MethodDelete {
		stop = context.AfterFunc(t.life, cancel)
		t.mu.Lock()
		if t.operationContext != nil {
			if t.operationContext.Err() != nil {
				cancel()
			}
			stopOperation = context.AfterFunc(t.operationContext, cancel)
		}
		t.mu.Unlock()
	}
	cleanup := func() { stopOperation(); stop(); cancel() }
	clone := req.Clone(ctx)
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		cleanup()
		return nil, errors.New("MCP HTTP request failed")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		cleanup()
		return nil, errors.New("MCP redirects are disabled")
	}
	if resp.ContentLength > maxBody {
		_ = resp.Body.Close()
		cleanup()
		return nil, errBodyTooLarge
	}
	// SDK does not consume DELETE response bodies, so close them here.
	if req.Method == http.MethodDelete {
		_ = resp.Body.Close()
		cleanup()
		resp.Body = http.NoBody
		return resp, nil
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, remaining: maxBody, cleanup: cleanup}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
	cleanup   func()
	once      sync.Once
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n != 0 {
			_ = b.Close()
			return 0, errBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cleanup)
	return err
}
