// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func voicePrep(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	dir := f.String("out", "voice", "private clip output directory")
	lengths := f.String("lengths", "10,15,25", "clip lengths in seconds, comma- or space-separated")
	top := f.Int("top", 3, "number of consecutive clips per length (not ranked)")
	ffmpeg := f.String("ffmpeg", "ffmpeg", "ffmpeg executable")
	// Preserve Python's --lengths 10 15 25 syntax while using FlagSet.
	var normalized []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--lengths" || args[i] == "-lengths" {
			normalized = append(normalized, args[i])
			var vals []string
			for i+1 < len(args) {
				if _, err := strconv.ParseFloat(args[i+1], 64); err != nil {
					break
				}
				i++
				vals = append(vals, args[i])
			}
			if len(vals) > 0 {
				normalized = append(normalized, strings.Join(vals, ","))
			}
			continue
		}
		normalized = append(normalized, args[i])
	}
	if e := parseFlags(f, normalized); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: voice-prep [flags] audio-file")
	}
	if *top <= 0 || *top > 100 {
		return fmt.Errorf("top must be 1..100")
	}
	var durations []float64
	for _, s := range strings.Split(*lengths, ",") {
		n, e := strconv.ParseFloat(s, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 3600 {
			return fmt.Errorf("lengths must be finite seconds in (0,3600]")
		}
		durations = append(durations, n)
	}
	input, e := filepath.Abs(f.Arg(0))
	if e != nil {
		return e
	}
	info, e := os.Stat(input)
	if e != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("audio input must be a local regular file")
	}
	if e = privateDir(*dir); e != nil {
		return e
	}
	if _, e := fmt.Fprintln(out, "Audio cuts only: no ranking, VAD, ASR, transcripts or F5 cloning. Piper is not equivalent to F5 cloning."); e != nil {
		return e
	}
	for _, length := range durations {
		for n := 1; n <= *top; n++ {
			tmp, e := os.CreateTemp(*dir, ".voice-*.wav")
			if e != nil {
				return e
			}
			path := tmp.Name()
			if e := tmp.Close(); e != nil {
				_ = os.Remove(path)
				return e
			}
			cmd := exec.CommandContext(ctx, *ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-ss", strconv.FormatFloat(float64(n-1)*length, 'f', 3, 64), "-i", input, "-t", strconv.FormatFloat(length, 'f', 3, 64), "-map", "0:a:0", "-vn", "-ac", "1", "-c:a", "pcm_s16le", "-f", "wav", path)
			// ffmpeg can echo sensitive input paths/metadata: never relay subprocess output.
			if e = cmd.Run(); e != nil {
				_ = os.Remove(path)
				return fmt.Errorf("ffmpeg failed preparing clip (check input and executable)")
			}
			name := filepath.Join(*dir, fmt.Sprintf("ref_%gs_%d.wav", length, n))
			e = os.Rename(path, name)
			if e != nil {
				_ = os.Remove(path)
				return e
			}
		}
	}
	_, e = fmt.Fprintln(out, "Private mono WAV clips prepared; use only recordings you have rights to use.")
	return e
}
