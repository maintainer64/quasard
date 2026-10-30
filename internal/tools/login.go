// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/maintainer64/quasard/internal/music"
)

func musicLogin(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	file := f.String("token-file", "data/music_token.json", "private OAuth token output file")
	timeout := f.Duration("timeout", 10*time.Minute, "maximum device login wait")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := noArgs(f); e != nil {
		return e
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if e := privateDir(filepath.Dir(*file)); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	_, err := music.New(music.Config{TokenFile: *file}).DeviceAuth(ctx, func(_ context.Context, code music.DeviceCode) error {
		_, e := fmt.Fprintf(out, "Open: %s\nVerification code: %s\nWaiting for approval...\n", code.VerificationURL, code.UserCode)
		return e
	})
	if err != nil {
		return fmt.Errorf("music device login failed (credentials not printed)")
	}
	_, err = fmt.Fprintln(out, "Login complete; token saved privately.")
	return err
}
