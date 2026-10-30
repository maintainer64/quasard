// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/maintainer64/quasard/internal/config"
)

type dnsSettingsLoader interface {
	Load() (config.DNS, error)
}

type dnsSettingsService interface {
	Apply(config.DNS) error
}

func watchDNSReload(ctx context.Context, loader dnsSettingsLoader, service dnsSettingsService) func() {
	signals, stopSignals := dnsReloadSignals()
	stopWorker := runDNSReload(ctx, signals, loader, service)
	return func() {
		stopSignals()
		stopWorker()
	}
}

func runDNSReload(ctx context.Context, signals <-chan os.Signal, loader dnsSettingsLoader, service dnsSettingsService) func() {
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
				settings, err := loader.Load()
				if err == nil {
					err = service.Apply(settings)
				}
				if err != nil {
					slog.Warn("DNS reload rejected; previous settings retained", "error", err)
					continue
				}
				slog.Info("DNS settings reloaded", "enabled", settings.Enabled, "listen", settings.Listen, "rewrite_count", len(settings.RewriteHosts))
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(cancel)
		<-done
	}
}
