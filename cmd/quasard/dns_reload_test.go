// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/dnsproxy"
	"github.com/miekg/dns"
)

type reloadLoader struct {
	mu       sync.Mutex
	settings config.DNS
	err      error
	calls    chan struct{}
}

func (l *reloadLoader) Load() (config.DNS, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls <- struct{}{}
	return l.settings, l.err
}

type reloadService struct {
	applied chan config.DNS
}

func (s *reloadService) Apply(settings config.DNS) error {
	s.applied <- settings
	return nil
}

func TestDNSFileReloadIntegration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	loader, err := config.NewDNSLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	service := dnsproxy.New()
	defer func() {
		if err := service.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reload := func() error {
		settings, err := loader.Load()
		if err != nil {
			return err
		}
		return service.Apply(settings)
	}
	const contents = "[dns]\nenabled=true\nlisten='127.0.0.1:0'\nanswer_ipv4='192.0.2.10'\nrewrite_hosts=['station.example']\nupstreams=['127.0.0.1:1']\n[asr]\nengine='unrelated-legacy-value'\nvad_threshold='wrong-type'\n"
	write(contents)
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	address := service.Addr()
	check := func(want string) {
		t.Helper()
		q := new(dns.Msg)
		q.SetQuestion("station.example.", dns.TypeA)
		r, _, err := (&dns.Client{Timeout: time.Second}).Exchange(q, address)
		if err != nil || r == nil || len(r.Answer) != 1 {
			t.Fatalf("query: %v, %v", r, err)
		}
		if got := r.Answer[0].(*dns.A).A.String(); got != want {
			t.Fatalf("answer %s, want %s", got, want)
		}
	}
	check("192.0.2.10")
	write(strings.Replace(contents, "192.0.2.10", "192.0.2.20", 1))
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if service.Addr() != address {
		t.Fatal("rules update rebound listener")
	}
	check("192.0.2.20")
	write("[dns]\nenabled=true\nanswer_ipv4='invalid'\n")
	if err := reload(); err == nil {
		t.Fatal("invalid edit accepted")
	}
	check("192.0.2.20")
	write("[dns]\nenabled=false\n")
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if service.Addr() != "" {
		t.Fatal("DNS not disabled")
	}
}

func TestDNSReloadWorker(t *testing.T) {
	loader := &reloadLoader{settings: config.DNS{Enabled: true}, calls: make(chan struct{}, 4)}
	service := &reloadService{applied: make(chan config.DNS, 4)}
	signals := make(chan os.Signal, 1)
	stop := runDNSReload(context.Background(), signals, loader, service)
	defer stop()
	signals <- os.Interrupt
	select {
	case got := <-service.applied:
		if !got.Enabled {
			t.Fatal("DNS settings not applied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload not applied")
	}
	<-loader.calls
	loader.mu.Lock()
	loader.err = errors.New("invalid configuration")
	loader.mu.Unlock()
	signals <- os.Interrupt
	select {
	case <-loader.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("reload not attempted")
	}
	stop()
	select {
	case <-service.applied:
		t.Fatal("failed reload applied settings")
	default:
	}
	signals <- os.Interrupt
	select {
	case <-loader.calls:
		t.Fatal("stopped worker reloaded settings")
	default:
	}
}
