//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/config"
)

func TestDNSReloadSignal(t *testing.T) {
	if os.Getenv("QUASARD_TEST_HUP_CHILD") == "1" {
		loader := &reloadLoader{settings: config.DNS{Enabled: true}, calls: make(chan struct{}, 1)}
		service := &reloadService{applied: make(chan config.DNS, 1)}
		stop := watchDNSReload(context.Background(), loader, service)
		defer stop()
		fmt.Println("READY")
		select {
		case <-service.applied:
		case <-time.After(5 * time.Second):
			t.Fatal("SIGHUP not delivered")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDNSReloadSignal$")
	cmd.Env = append(os.Environ(), "QUASARD_TEST_HUP_CHILD=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatalf("subprocess not ready: %q, %v", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		// Drain test output so the subprocess can exit.
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
