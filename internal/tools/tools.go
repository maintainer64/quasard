// SPDX-License-Identifier: GPL-3.0-only
// Package tools provides local development helpers. Voice preparation only cuts
// audio with ffmpeg: no F5 cloning, ASR or VAD. Piper is not equivalent to F5
// voice cloning and this command does not claim otherwise.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const usage = `Usage: quasard-tools <command> [flags]
Commands: certs, import-har, activation-sim, station-sim, fake-llm, voice-prep, music-login
Use <command> --help for flags. No certificates are installed.
voice-prep cuts audio only; no F5 cloning/ASR/VAD. Piper is not equivalent to F5 cloning.
`

func Run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, usage)
		return err
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(stderr)
	var err error
	switch args[0] {
	case "certs":
		err = certs(f, args[1:], out)
	case "import-har":
		err = importHAR(f, args[1:], out)
	case "activation-sim":
		err = activation(ctx, f, args[1:], out)
	case "station-sim":
		err = station(ctx, f, args[1:], out)
	case "fake-llm":
		err = fakeLLM(ctx, f, args[1:], out)
	case "voice-prep":
		err = voicePrep(ctx, f, args[1:], out)
	case "music-login":
		err = musicLogin(ctx, f, args[1:], out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }
func noArgs(f *flag.FlagSet) error {
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	return nil
}
func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func text(v any) string { s, _ := v.(string); return s }

// privateDir rejects symlink components; sensitive output never follows links.
func privateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for _, p := range parts {
		current = filepath.Join(current, p)
		info, e := os.Lstat(current)
		if errors.Is(e, os.ErrNotExist) {
			if e = os.Mkdir(current, 0700); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("output directory contains non-directory or symlink")
		}
	}
	return os.Chmod(abs, 0700)
}
func privateWrite(path string, data []byte) error {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".quasard-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func privateJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return privateWrite(path, append(b, '\n'))
}
