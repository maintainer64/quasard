//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func dnsReloadSignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	return ch, func() { signal.Stop(ch) }
}
