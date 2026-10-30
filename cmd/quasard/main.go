// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/maintainer64/quasard/internal/aliceapi"
	"github.com/maintainer64/quasard/internal/buildinfo"
	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/dnsproxy"
	"github.com/maintainer64/quasard/internal/httpapi"
	"github.com/maintainer64/quasard/internal/llm"
	"github.com/maintainer64/quasard/internal/mcpclient"
	"github.com/maintainer64/quasard/internal/music"
	"github.com/maintainer64/quasard/internal/session"
	"github.com/maintainer64/quasard/internal/speech"
)

type engine struct {
	mu    sync.RWMutex
	value *speech.Engine
	err   error
}

func (e *engine) ready() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.value != nil {
		return nil
	}
	if e.err != nil {
		return e.err
	}
	return errors.New("speech models warming up")
}
func (e *engine) Transcribe(ctx context.Context, pcm []int16) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.value == nil {
		return "", errors.New("speech models unavailable")
	}
	return e.value.Transcribe(ctx, pcm)
}
func (e *engine) Synthesize(ctx context.Context, text string) (speech.PCM, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.value == nil {
		return speech.PCM{}, errors.New("speech models unavailable")
	}
	return e.value.Synthesize(ctx, text)
}
func (e *engine) NewVAD() (*speech.VAD, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.value == nil {
		return nil, errors.New("speech models unavailable")
	}
	return e.value.NewVAD()
}
func main() {
	if err := run(); err != nil {
		slog.Error("quasard stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "", "TOML config path (default: ALICE_CONFIG, then config.toml)")
	healthcheck := flag.Bool("healthcheck", false, "check local readiness without loading models")
	version := flag.Bool("version", false, "show version and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.String())
		return nil
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Server.LogLevel)); err != nil {
		return fmt.Errorf("server.log_level: %w", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if *healthcheck {
		if cfg.Server.HTTPPort <= 0 {
			return errors.New("healthcheck requires HTTP listener")
		}
		client := &http.Client{Timeout: 4 * time.Second}
		response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", fmt.Sprint(cfg.Server.HTTPPort)) + "/_ready")
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("readiness HTTP %d", response.StatusCode)
		}
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dnsLoader, err := config.NewDNSLoader(*path)
	if err != nil {
		return err
	}
	dnsService := dnsproxy.New()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := dnsService.Close(shutdown); err != nil {
			slog.Warn("DNS shutdown", "error", err)
		}
	}()
	if err := dnsService.Apply(cfg.DNS); err != nil {
		return fmt.Errorf("start DNS: %w", err)
	}
	stopReload := watchDNSReload(ctx, dnsLoader, dnsService)
	defer stopReload()
	codec, err := aliceapi.Load(cfg.Resolve("alice_server/aliceapi.desc"))
	if err != nil {
		return fmt.Errorf("load AliceAPI descriptor: %w", err)
	}
	lc := llm.DefaultConfig()
	lc.BaseURL = cfg.LLM.BaseURL
	lc.APIKey = cfg.LLM.APIKey
	lc.Model = cfg.LLM.Model
	lc.SystemPrompt = cfg.LLM.SystemPrompt
	lc.SystemPromptFile = cfg.LLM.SystemPromptFile
	lc.Root = cfg.Root
	lc.Timezone = cfg.Device.Timezone
	lc.Temperature = cfg.LLM.Temperature
	lc.MaxTokens = cfg.LLM.MaxTokens
	lc.HistoryTurns = cfg.LLM.HistoryTurns
	lc.HistoryTTL = time.Duration(cfg.LLM.HistoryTTLS) * time.Second
	lc.Timeout = time.Duration(cfg.LLM.TimeoutS * float64(time.Second))
	lc.ExtraBody = cfg.LLM.ExtraBody
	lc.ModelRU, lc.ModelEN = cfg.LLM.ModelRU, cfg.LLM.ModelEN
	lc.MaxToolCalls = cfg.LLM.MaxToolCalls
	if cfg.MCP.Enabled {
		servers := make([]mcpclient.Server, 0, len(cfg.MCP.Servers))
		for _, server := range cfg.MCP.Servers {
			servers = append(servers, mcpclient.Server{Name: server.Name, URL: server.URL, TokenEnv: server.TokenEnv, AllowedTools: server.AllowedTools})
		}
		tools, err := mcpclient.New(servers)
		if err != nil {
			return fmt.Errorf("MCP configuration: %w", err)
		}
		defer func() {
			if err := tools.Close(); err != nil {
				slog.Warn("MCP shutdown failed", "error", err)
			}
		}()
		lc.Tools = tools
	}
	m := music.New(music.Config{Enabled: cfg.Music.Enabled, Token: cfg.Music.Token, Quality: cfg.Music.Quality, TokenFile: filepath.Join(cfg.DataPath(), "music_token.json")})
	sp := &engine{}
	svc := session.New(cfg, llm.New(lc), m, sp, codec)
	h, err := httpapi.NewWithError(cfg, httpapi.Hooks{WebSocket: svc, Connections: svc.Connections, Ready: sp.ready, Say: func(ctx context.Context, id, text string) error {
		err := svc.Say(ctx, id, text)
		if errors.Is(err, session.ErrNotConnected) {
			return httpapi.ErrNotConnected
		}
		return err
	}, Push: func(ctx context.Context, id string, p map[string]any) error {
		err := svc.Push(ctx, id, p)
		if errors.Is(err, session.ErrNotConnected) {
			return httpapi.ErrNotConnected
		}
		return err
	}})
	if err != nil {
		return err
	}
	var warm sync.WaitGroup
	warm.Add(1)
	go func() {
		defer warm.Done()
		dir := cfg.ASR.LibraryPath
		if dir == "" {
			dir = os.Getenv("QUASARD_LIBRARY_DIR")
		}
		if filepath.Ext(dir) != "" {
			dir = filepath.Dir(dir)
		}
		value, e := speech.New(speech.Config{VoskModel: cfg.Resolve(cfg.ASR.ModelPath), VADModel: cfg.Resolve(cfg.ASR.VADModelPath), PiperModel: cfg.Resolve(cfg.TTS.Model), PiperConfig: cfg.Resolve(cfg.TTS.ConfigPath), LibraryDir: dir, EspeakData: os.Getenv("QUASARD_ESPEAK_DATA")})
		sp.mu.Lock()
		sp.value = value
		sp.err = e
		sp.mu.Unlock()
		if e != nil {
			slog.Error("local speech startup failed", "error", e)
		} else {
			slog.Info("local speech ready")
		}
	}()
	var servers []*http.Server
	defer func() {
		stopReload()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, server := range servers {
			if e := server.Shutdown(shutdown); e != nil {
				slog.Warn("HTTP shutdown failed", "error", e)
				if closeErr := server.Close(); closeErr != nil {
					slog.Warn("HTTP close failed", "error", closeErr)
				}
			}
		}
		svc.Close()
		warm.Wait()
		sp.mu.Lock()
		defer sp.mu.Unlock()
		if sp.value != nil {
			if closeErr := sp.value.Close(); closeErr != nil {
				slog.Warn("speech close failed", "error", closeErr)
			}
		}
	}()
	errs := make(chan error, 2)
	start := func(port int, tls bool) error {
		if port <= 0 {
			return nil
		}
		listener, e := net.Listen("tcp", net.JoinHostPort(cfg.Server.Host, fmt.Sprint(port)))
		if e != nil {
			return e
		}
		server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
		servers = append(servers, server)
		go func() {
			var e error
			if tls {
				e = server.ServeTLS(listener, cfg.Resolve(cfg.Server.CertFile), cfg.Resolve(cfg.Server.KeyFile))
			} else {
				e = server.Serve(listener)
			}
			if !errors.Is(e, http.ErrServerClosed) {
				errs <- e
			}
		}()
		slog.Info("listener ready", "address", listener.Addr(), "tls", tls)
		return nil
	}
	if err = start(cfg.Server.HTTPPort, false); err != nil {
		return err
	}
	if cfg.Server.HTTPSPort > 0 {
		if _, e := os.Stat(cfg.Resolve(cfg.Server.CertFile)); e != nil {
			slog.Warn("HTTPS disabled: certificate missing")
		} else if err = start(cfg.Server.HTTPSPort, true); err != nil {
			return err
		}
	}
	if len(servers) == 0 {
		return errors.New("no HTTP or HTTPS listeners configured")
	}
	select {
	case <-ctx.Done():
	case err = <-errs:
	case dnsErr := <-dnsService.Errors():
		err = fmt.Errorf("DNS listener: %w", dnsErr)
	}
	return err
}
