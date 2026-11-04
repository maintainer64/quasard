// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestDebugSignalToggleAndShutdown(t *testing.T) {
	for _, configured := range []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelDebug} {
		t.Run(configured.String(), func(t *testing.T) {
			var level slog.LevelVar
			level.Set(configured)
			signals := make(chan os.Signal, 2)
			stop := runLogLevelWatch(context.Background(), signals, &level, configured)
			defer stop()
			wait := func(want slog.Level) {
				t.Helper()
				deadline := time.After(time.Second)
				for level.Level() != want {
					select {
					case <-deadline:
						t.Fatal("level was not toggled")
					case <-time.After(time.Millisecond):
					}
				}
			}
			expected := slog.LevelDebug
			if configured == slog.LevelDebug {
				expected = slog.LevelInfo
			}
			signals <- os.Interrupt
			wait(expected)
			signals <- os.Interrupt
			wait(configured)
			stop() // Idempotent and releases the worker.
			signals <- os.Interrupt
			if level.Level() != configured {
				t.Fatal("stopped watcher changed the log level")
			}
		})
	}
}

func TestDebugOffClearsAudioCapture(t *testing.T) {
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	var clears atomic.Int32
	signals := make(chan os.Signal, 2)
	stop := runLogLevelWatch(context.Background(), signals, &level, slog.LevelDebug, func() { clears.Add(1) })
	defer stop()
	signals <- os.Interrupt
	deadline := time.After(time.Second)
	for clears.Load() != 1 {
		select {
		case <-deadline:
			t.Fatal("DEBUG off did not clear audio")
		case <-time.After(time.Millisecond):
		}
	}
	if level.Level() != slog.LevelInfo {
		t.Fatal("debug still enabled")
	}
}
