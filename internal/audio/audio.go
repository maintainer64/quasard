// SPDX-License-Identifier: GPL-3.0-only
// Package audio streams Ogg/Opus through ffmpeg; no Python runtime is used.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

const ASRRate = 16000
const OutputRate = 48000
const GapDurationMS = 120

type PCM struct {
	Samples    []int16
	SampleRate int
}

// Decoder accepts an incremental Ogg/Opus reader and yields 16 kHz mono PCM.
// Close cancels ffmpeg. Caller must also unblock its input reader if blocked.
type Decoder struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	cancel context.CancelFunc
	stderr bytes.Buffer
	once   sync.Once
	err    error
}

func NewDecoder(ctx context.Context, input io.Reader) (*Decoder, error) {
	ctx, cancel := context.WithCancel(ctx)
	d := &Decoder{cancel: cancel}
	d.cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-probesize", "32", "-analyzeduration", "0", "-f", "ogg", "-i", "pipe:0", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	d.cmd.Stdin = input
	d.cmd.Stderr = &d.stderr
	var err error
	d.out, err = d.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = d.cmd.Start(); err != nil {
		cancel()
		_ = d.out.Close()
		return nil, err
	}
	return d, nil
}
func (d *Decoder) wait() error {
	d.once.Do(func() {
		d.err = d.cmd.Wait()
		if d.err != nil {
			d.err = fmt.Errorf("decode Opus: %w: %s", d.err, d.stderr.String())
		}
	})
	return d.err
}
func (d *Decoder) Read(samples []int16) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	b := make([]byte, len(samples)*2)
	n, err := io.ReadFull(d.out, b)
	for i := 0; i < n/2; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	if err != nil {
		if n%2 != 0 {
			return n / 2, errors.New("truncated PCM sample")
		}
		if e := d.wait(); e != nil {
			return n / 2, e
		}
		err = io.EOF
	}
	return n / 2, err
}
func (d *Decoder) Close() error { d.cancel(); _ = d.out.Close(); return d.wait() }
func Decode(ctx context.Context, ogg []byte) (PCM, error) {
	d, err := NewDecoder(ctx, bytes.NewReader(ogg))
	if err != nil {
		return PCM{}, err
	}
	defer func() { _ = d.Close() }()
	p := PCM{SampleRate: ASRRate}
	buf := make([]int16, 4096)
	for {
		n, e := d.Read(buf)
		p.Samples = append(p.Samples, buf[:n]...)
		if e == io.EOF {
			return p, nil
		}
		if e != nil {
			return PCM{}, e
		}
	}
}

// Encoder emits ONE continuous 48 kHz mono Ogg/Opus logical stream. Add does
// not restart the codec. Gap inserts precisely 120 ms between sentences.
type Encoder struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	stderr bytes.Buffer
	closed bool
	err    error
}

func NewEncoder(ctx context.Context, output io.Writer) (*Encoder, error) {
	e := &Encoder{}
	e.cmd = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0", "-c:a", "libopus", "-b:a", "32k", "-application", "voip", "-frame_duration", "20", "-page_duration", "20000", "-flush_packets", "1", "-f", "ogg", "pipe:1")
	e.cmd.Stdout = output
	e.cmd.Stderr = &e.stderr
	var err error
	e.in, err = e.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err = e.cmd.Start(); err != nil {
		_ = e.in.Close()
		return nil, err
	}
	return e, nil
}
func (e *Encoder) Add(p PCM) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("audio encoder closed")
	}
	if p.SampleRate < 8000 || p.SampleRate > 192000 {
		return fmt.Errorf("invalid PCM sample rate %d", p.SampleRate)
	}
	if len(p.Samples) == 0 {
		return nil
	}
	n := int(int64(len(p.Samples)) * OutputRate / int64(p.SampleRate))
	buf := make([]byte, 8192)
	for start := 0; start < n; {
		count := min(n-start, len(buf)/2)
		for j := 0; j < count; j++ {
			pos := int64(start+j) * int64(p.SampleRate)
			i := int(pos / OutputRate)
			frac := pos % OutputRate
			a := int64(p.Samples[i])
			b := int64(p.Samples[min(i+1, len(p.Samples)-1)])
			v := a + (b-a)*frac/OutputRate
			binary.LittleEndian.PutUint16(buf[2*j:], uint16(int16(v)))
		}
		if _, err := e.in.Write(buf[:count*2]); err != nil {
			return err
		}
		start += count
	}
	return nil
}
func (e *Encoder) Gap() error {
	return e.Add(PCM{Samples: make([]int16, OutputRate*GapDurationMS/1000), SampleRate: OutputRate})
}
func (e *Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return e.err
	}
	e.closed = true
	_ = e.in.Close()
	e.err = e.cmd.Wait()
	if e.err != nil {
		e.err = fmt.Errorf("encode Opus: %w: %s", e.err, e.stderr.String())
	}
	return e.err
}
