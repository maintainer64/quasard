// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"context"
	"log/slog"
)

type loggerContextKey struct{}

// WithLogger attaches device/request fields to native recognition diagnostics.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}
