// SPDX-License-Identifier: GPL-3.0-only
package llm

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type loggerContextKey struct{}

// WithLogger carries request correlation only. Never attach prompts or secrets.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}
func requestLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}
func (d *Dialog) doRequest(req *http.Request) (*http.Response, error) {
	logger := requestLogger(req.Context())
	started := time.Now()
	resp, err := d.client.Do(req)
	// Host excludes URL credentials, paths, query strings, and request bodies.
	if err != nil {
		logger.DebugContext(req.Context(), "LLM HTTP failed", "host", req.URL.Host, "elapsed_ms", time.Since(started).Milliseconds(), "result", outcome(err))
	} else {
		logger.DebugContext(req.Context(), "LLM HTTP response", "host", req.URL.Host, "status", resp.StatusCode, "headers_ms", time.Since(started).Milliseconds())
	}
	return resp, err
}
