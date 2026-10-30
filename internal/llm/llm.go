// SPDX-License-Identifier: GPL-3.0-only

// Package llm streams provider-neutral OpenAI-compatible Chat Completions.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

// Config is independent of application config. Start with DefaultConfig to
// obtain reference defaults. Zero numeric fields in an explicit Config remain
// zero (notably HistoryTurns=0 disables retained history).
type Config struct {
	BaseURL, APIKey, Model                         string
	SystemPrompt, SystemPromptFile, Root, Timezone string
	Temperature                                    float64
	MaxTokens, HistoryTurns                        int
	HistoryTTL, Timeout                            time.Duration
	ExtraBody                                      map[string]any
	HTTPClient                                     *http.Client
	Now                                            func() time.Time
	ModelRU, ModelEN                               string
	Tools                                          ToolClient
	MaxToolCalls                                   int
}

func DefaultConfig() Config {
	return Config{
		BaseURL: "https://api.groq.com/openai/v1", Model: "qwen/qwen3.8-27b", Temperature: 0.6, MaxTokens: 400,
		Timeout: 60 * time.Second, HistoryTurns: 8, HistoryTTL: 300 * time.Second,
		ExtraBody: map[string]any{"reasoning_effort": "none"}, SystemPromptFile: "system_prompt.txt", Timezone: "Europe/Moscow",
		SystemPrompt: "Ты — голосовой ассистент Алиса в умной колонке. Отвечай по-русски, коротко и по делу (1–3 предложения), разговорным языком. Никакой разметки, списков, эмодзи и ссылок — текст будет озвучен синтезатором речи. Числа и единицы пиши так, как их произносят.",
	}
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Dialog owns shared per-device history and an HTTP client. Concurrent calls
// are safe; requests snapshot history at start and append only on full success.
type Dialog struct {
	cfg          Config
	client       *http.Client
	mu           sync.Mutex
	history      map[string][]message
	last         map[string]time.Time
	extraOK      bool
	promptMtime  time.Time
	promptLoaded bool
	promptText   string
}

func New(cfg Config) *Dialog {
	if cfg.Timezone == "" {
		cfg.Timezone = "Europe/Moscow"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ExtraBody != nil {
		extra := make(map[string]any, len(cfg.ExtraBody))
		for k, v := range cfg.ExtraBody {
			extra[k] = v
		}
		cfg.ExtraBody = extra
	}
	client := cfg.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		client = &http.Client{Transport: transport}
	}
	return &Dialog{cfg: cfg, client: client, history: make(map[string][]message), last: make(map[string]time.Time), extraOK: true}
}

// Reset clears history but leaves last-use timestamp, matching reference behavior.
func (d *Dialog) Reset(device string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.history, device)
}

func (d *Dialog) systemPromptLocked() (string, error) {
	path := d.cfg.SystemPromptFile
	if path == "" {
		return d.cfg.SystemPrompt, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(d.cfg.Root, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return d.cfg.SystemPrompt, nil
	}
	if !d.promptLoaded || !st.ModTime().Equal(d.promptMtime) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("system prompt %s is not valid UTF-8", path)
		}
		d.promptText = strings.TrimSpace(strings.TrimPrefix(string(data), string(rune(0xfeff))))
		d.promptMtime = st.ModTime()
		d.promptLoaded = true
	}
	if d.promptText == "" {
		return d.cfg.SystemPrompt, nil
	}
	return d.promptText, nil
}

var weekdays = []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

func (d *Dialog) messages(device, text string) ([]message, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.cfg.Now()
	if now.Sub(d.last[device]) > d.cfg.HistoryTTL {
		d.history[device] = nil
	}
	prompt, err := d.systemPromptLocked()
	if err != nil {
		return nil, err
	}
	zone, err := time.LoadLocation(d.cfg.Timezone)
	if err != nil {
		return nil, err
	}
	dt := now.In(zone)
	system := fmt.Sprintf("%s\nТекущие дата и время: %s, %s (%s).", prompt, weekdays[int(dt.Weekday())], dt.Format("02.01.2006 15:04"), d.cfg.Timezone)
	messages := make([]message, 0, len(d.history[device])+2)
	messages = append(messages, message{Role: "system", Content: system})
	messages = append(messages, d.history[device]...)
	return append(messages, message{Role: "user", Content: text}), nil
}
func (d *Dialog) remember(device, text, answer string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	hist := append(d.history[device], message{Role: "user", Content: text}, message{Role: "assistant", Content: answer})
	turns := d.cfg.HistoryTurns
	if turns < 0 {
		turns = 0
	}
	if len(hist)/2 > turns {
		hist = hist[len(hist)-2*turns:]
	}
	d.history[device] = hist
	d.last[device] = d.cfg.Now()
}

// Stream emits complete, cleaned speech phrases. Neither partial output nor
// callback/HTTP/cancellation errors commit a user/assistant history pair. Echo
// mode never touches history. An empty successful answer is not remembered.
func (d *Dialog) Stream(ctx context.Context, device, text string, emit func(string) error) error {
	if emit == nil {
		return errors.New("llm: nil stream callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.cfg.BaseURL == "echo" {
		for _, s := range []string{"Вы сказали: " + text, "Это тестовый ответ."} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(s); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	messages, err := d.messages(device, text)
	if err != nil {
		return err
	}
	model, language := d.route(text)
	if language != "" {
		messages[0].Content += "\nRespond in " + language + ", matching the user's language."
	}
	if d.cfg.Tools != nil {
		return d.toolTurn(ctx, device, text, model, messages, emit)
	}
	splitter := NewSentenceSplitter(12, 120)
	var full []string
	state, pending := "start", ""
	send := func(sentences []string) error {
		for _, s := range sentences {
			s = CleanForSpeech(s)
			if s != "" {
				if err := ctx.Err(); err != nil {
					return err
				}
				full = append(full, s)
				if err := emit(s); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err = d.tokens(ctx, model, messages, func(tok string) error {
		if state != "normal" {
			pending += tok
			head := strings.TrimLeftFunc(pending, func(r rune) bool { return strings.TrimSpace(string(r)) == "" })
			if state == "start" {
				if strings.HasPrefix(head, "<think>") {
					state = "think"
				} else if head != "" && !strings.HasPrefix("<think>", head) {
					state = "normal"
					tok = pending
				}
			}
			if state == "think" {
				_, tail, ok := strings.Cut(pending, "</think>")
				if !ok {
					return nil
				}
				state = "normal"
				tok = tail
			}
			if state != "normal" {
				return nil
			}
			pending = ""
		}
		return send(splitter.Feed(tok))
	})
	if err != nil {
		return err
	}
	if err = send(splitter.Flush()); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if answer := strings.Join(full, " "); answer != "" {
		d.remember(device, text, answer)
	}
	return nil
}

func (d *Dialog) tokens(ctx context.Context, model string, messages []message, emit func(string) error) error {
	for {
		body := map[string]any{"model": model, "messages": messages, "temperature": d.cfg.Temperature, "max_tokens": d.cfg.MaxTokens, "stream": true}
		d.mu.Lock()
		extras := d.extraOK
		d.mu.Unlock()
		if extras {
			for k, v := range d.cfg.ExtraBody {
				body[k] = v
			}
		}
		// Routing and conversation boundaries cannot be replaced by extra_body.
		body["model"], body["messages"], body["stream"] = model, messages, true
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		// httpx's timeout is an inactivity timeout, not a total generation
		// deadline: restart it per read and never charge time spent in emit.
		requestCtx, cancelCause := context.WithCancelCause(ctx)
		cancel := func() { cancelCause(context.Canceled) }
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(d.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Authorization", "Bearer "+d.apiKey())
		req.Header.Set("Content-Type", "application/json")
		stop := startReadTimeout(d.cfg.Timeout, cancelCause)
		resp, err := d.client.Do(req)
		stop()
		if err != nil {
			cause := context.Cause(requestCtx)
			cancel()
			if cause != nil {
				return cause
			}
			return err
		}
		resp.Body = &timeoutBody{ReadCloser: resp.Body, timeout: d.cfg.Timeout, cancel: cancelCause, ctx: requestCtx}
		if resp.StatusCode == 400 && extras && len(d.cfg.ExtraBody) > 0 {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			cancel()
			if err != nil {
				return err
			}
			d.mu.Lock()
			d.extraOK = false
			d.mu.Unlock()
			continue
		}
		if resp.StatusCode >= 400 {
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			cancel()
			if readErr != nil {
				return readErr
			}
			text := []rune(strings.ToValidUTF8(string(data), "�"))
			if len(text) > 300 {
				text = text[:300]
			}
			return fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(text))
		}
		err = readSSE(resp.Body, emit)
		_ = resp.Body.Close()
		requestErr := context.Cause(requestCtx)
		cancel()
		if err != nil {
			return err
		}
		if requestErr != nil {
			return requestErr
		}
		return nil
	}
}

func (d *Dialog) apiKey() string {
	if d.cfg.APIKey != "" {
		return d.cfg.APIKey
	}
	if key, ok := os.LookupEnv("GROQ_API_KEY"); ok {
		return key
	}
	return "none"
}

func startReadTimeout(timeout time.Duration, cancel context.CancelCauseFunc) func() {
	if timeout <= 0 {
		return func() {}
	}
	timer := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
	return func() { timer.Stop() }
}

type timeoutBody struct {
	io.ReadCloser
	timeout time.Duration
	cancel  context.CancelCauseFunc
	ctx     context.Context
}

func (r *timeoutBody) Read(p []byte) (int, error) {
	stop := startReadTimeout(r.timeout, r.cancel)
	n, err := r.ReadCloser.Read(p)
	stop()
	if cause := context.Cause(r.ctx); cause != nil {
		return n, cause
	}
	return n, err
}

// readLine supports LF, CRLF and bare CR like httpx's universal line decoder.
func readLine(r *bufio.Reader) (string, error) {
	var line strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return line.String(), err
		}
		if b == '\n' {
			return line.String(), nil
		}
		if b == '\r' {
			next, err := r.ReadByte()
			if err == nil && next != '\n' {
				_ = r.UnreadByte()
			}
			return line.String(), nil
		}
		line.WriteByte(b)
	}
}

// readSSE intentionally treats each data: line independently, as does Python's
// aiter_lines loop. Ignore metadata, malformed JSON and reasoning-only deltas.
func readSSE(reader io.Reader, emit func(string) error) error {
	r := bufio.NewReader(reader)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(line[5:])
				if data == "[DONE]" {
					return nil
				}
				var event struct {
					Choices []struct {
						Delta *struct {
							Content json.RawMessage `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal([]byte(data), &event) == nil && len(event.Choices) > 0 && event.Choices[0].Delta != nil {
					var content string
					if json.Unmarshal(event.Choices[0].Delta.Content, &content) == nil && content != "" {
						if err := emit(content); err != nil {
							return err
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
