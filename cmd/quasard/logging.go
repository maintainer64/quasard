// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
)

func watchDebugLogging(ctx context.Context, level *slog.LevelVar, configured slog.Level, onDisable ...func()) func() {
	signals, stopSignals := logToggleSignals()
	stopWorker := runLogLevelWatch(ctx, signals, level, configured, onDisable...)
	return func() { stopSignals(); stopWorker() }
}
func runLogLevelWatch(ctx context.Context, signals <-chan os.Signal, level *slog.LevelVar, configured slog.Level, onDisable ...func()) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-signals:
				if !ok || ctx.Err() != nil {
					return
				}
				next := slog.LevelDebug
				if level.Level() == slog.LevelDebug {
					next = configured
					if next == slog.LevelDebug {
						next = slog.LevelInfo
					}
				}
				level.Set(next)
				if next > slog.LevelDebug {
					for _, clear := range onDisable {
						if clear != nil {
							clear()
						}
					}
				}
				slog.Log(ctx, max(next, slog.LevelInfo), "diagnostic logging changed", "level", next.String())
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(cancel); <-done }
}
