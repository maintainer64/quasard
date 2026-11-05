//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: GPL-3.0-only
package main

import "os"

func dnsReloadSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
