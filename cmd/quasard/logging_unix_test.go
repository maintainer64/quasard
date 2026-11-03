//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestUSR1ActuallyTogglesDiagnostics(t *testing.T) {
	var level slog.LevelVar
	level.Set(slog.LevelInfo)
	stop := watchDebugLogging(context.Background(), &level, slog.LevelInfo)
	defer stop()
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		if err := process.Signal(syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(time.Second)
		for level.Level() != want {
			select {
			case <-deadline:
				t.Fatal("SIGUSR1 did not change level")
			case <-time.After(time.Millisecond):
			}
		}
	}
}
