//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func logToggleSignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	return ch, func() { signal.Stop(ch) }
}
