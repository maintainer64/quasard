//go:build !native || !cgo || !linux

// SPDX-License-Identifier: GPL-3.0-only
package speech

import "context"

type backend struct{}
type vadBackend struct{}

func newBackend(Config) (*backend, error)                            { return nil, ErrUnavailable }
func (*backend) transcribe(context.Context, []int16) (string, error) { return "", ErrUnavailable }
func (*backend) synthesize(context.Context, string) (PCM, error)     { return PCM{}, ErrUnavailable }
func (*backend) close()                                              {}
func (*backend) vad(string) (*vadBackend, error)                     { return nil, ErrUnavailable }
func (*vadBackend) feed([]int16) (float32, error)                    { return 0, ErrUnavailable }
func (*vadBackend) close()                                           {}
