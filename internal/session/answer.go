// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/maintainer64/quasard/internal/audio"
	"github.com/maintainer64/quasard/internal/intents"
	"github.com/maintainer64/quasard/internal/music"
	P "github.com/maintainer64/quasard/internal/protocol"
)

func (c *Session) response(ctx context.Context, gen uint64, ref, request string, seq int, t string, actions []Map) error {
	return c.send(ctx, P.Vins(ref, request, c.sid, seq, t, "", actions, false), gen)
}
func (c *Session) answer(ctx context.Context, gen uint64, ref, request string, seq int, t string, direct bool) error {
	if !direct {
		if in := intents.Match(t); in != nil {
			if in.ResetDialog {
				c.svc.Dialog.Reset(c.device)
			}
			actions := []Map{}
			for _, a := range in.Directives {
				actions = append(actions, Map{"name": a.Name, "payload": a.Payload, "type": a.Type})
			}
			if in.Text == "" {
				return c.response(ctx, gen, ref, request, seq, "", actions)
			}
			return c.speak(ctx, gen, ref, request, seq, func(emit func(string) error) error { return emit(in.Text) }, actions)
		}
		if m := intents.MatchMusic(t); m != nil {
			if handled, e := c.musicCommand(ctx, gen, ref, request, seq, m.Action, m.Query); handled || e != nil {
				return e
			}
		}
	}
	producer := func(emit func(string) error) error {
		if direct {
			return emit(t)
		}
		if c.svc.Dialog == nil {
			return errors.New("LLM unavailable")
		}
		return c.svc.Dialog.Stream(ctx, c.device, t, emit)
	}
	return c.speak(ctx, gen, ref, request, seq, producer, nil)
}

type sentence struct {
	text string
	pcm  audio.PCM
}

func (c *Session) speak(ctx context.Context, gen uint64, ref, request string, seq int, producer func(func(string) error) error, actions []Map) (err error) {
	if c.svc.Speech == nil {
		return errors.New("local speech engine not ready")
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	defer func() {
		cancel()
		workers.Wait()
	}()
	phrases := make(chan string, 4)
	ready := make(chan sentence, 2)
	errs := make(chan error, 2)
	var mu sync.Mutex
	all := []string{}
	finished := false
	go func() {
		defer workers.Done()
		err := producer(func(t string) error {
			if strings.TrimSpace(t) == "" {
				return nil
			}
			mu.Lock()
			all = append(all, t)
			mu.Unlock()
			select {
			case phrases <- t:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		mu.Lock()
		finished = true
		mu.Unlock()
		close(phrases)
		errs <- err
	}()
	go func() {
		defer workers.Done()
		defer close(ready)
		for {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case t, ok := <-phrases:
				if !ok {
					errs <- nil
					return
				}
				pcm, e := c.svc.Speech.Synthesize(ctx, t)
				if e != nil {
					errs <- e
					cancel()
					return
				}
				select {
				case ready <- sentence{t, audio.PCM(pcm)}:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			}
		}
	}()
	var first sentence
	select {
	case <-ctx.Done():
		return ctx.Err()
	case x, ok := <-ready:
		if !ok {
			for i := 0; i < 2; i++ {
				if e := <-errs; e != nil {
					return e
				}
			}
			return c.response(ctx, gen, ref, request, seq, "", actions)
		}
		first = x
	}
	hold := time.Duration(c.svc.Config.LLM.HoldResponseMS) * time.Millisecond
	if hold > 0 {
		timer := time.NewTimer(hold)
		defer timer.Stop()
		for {
			mu.Lock()
			done := finished
			mu.Unlock()
			if done {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				goto card
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
card:
	mu.Lock()
	full := strings.Join(all, " ")
	done := finished
	mu.Unlock()
	if full == "" {
		full = first.text
	}
	listen := c.svc.Config.LLM.ListenAfterQuestion && done && strings.HasSuffix(strings.TrimSpace(full), "?")
	if err := c.send(ctx, P.Vins(ref, request, c.sid, seq, full, full, actions, listen), gen); err != nil {
		return err
	}
	c.mu.Lock()
	id := c.stream
	c.stream += 2
	c.mu.Unlock()
	if err := c.send(ctx, P.Speak(ref, id), gen); err != nil {
		return err
	}
	writer := &streamWriter{session: c, ctx: ctx, id: id, gen: gen}
	encoder, e := audio.NewEncoder(ctx, writer)
	if e != nil {
		return e
	}
	closed := false
	defer func() {
		if !closed {
			_ = encoder.Close() // Best-effort cleanup after an earlier streaming failure.
		}
		err = errors.Join(err, c.send(ctx, P.Control(id, 0, 0), gen))
	}()
	if e = encoder.Add(first.pcm); e != nil {
		return e
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case next, ok := <-ready:
			if !ok {
				if e = encoder.Close(); e != nil {
					return e
				}
				closed = true
				for i := 0; i < 2; i++ {
					if e := <-errs; e != nil {
						return e
					}
				}
				return nil
			}
			if e = encoder.Gap(); e != nil {
				return e
			}
			if e = encoder.Add(next.pcm); e != nil {
				return e
			}
		}
	}
}

type streamWriter struct {
	session *Session
	ctx     context.Context
	id      uint32
	gen     uint64
}

func (w *streamWriter) Write(b []byte) (int, error) {
	if err := w.session.audio(w.ctx, w.id, b, w.gen); err != nil {
		return 0, err
	}
	return len(b), nil
}

var _ io.Writer = (*streamWriter)(nil)

func (c *Session) musicCommand(ctx context.Context, gen uint64, ref, request string, seq int, action, query string) (bool, error) {
	m := c.svc.Music
	if action == "pause" || action == "resume" {
		name := "player_pause"
		payload := Map{"smooth": true}
		if action == "resume" {
			name = "player_continue"
			payload = Map{}
		}
		return true, c.response(ctx, gen, ref, request, seq, "", []Map{P.Action(name, payload)})
	}
	if m == nil || !m.Enabled() {
		return false, nil
	}
	var track *music.TrackInfo
	var err error
	switch action {
	case "wave", "search":
		var s *music.Session
		if action == "wave" {
			s, err = m.StartWave(ctx, c.device)
		} else {
			s, err = m.StartSearch(ctx, c.device, query)
		}
		if s != nil {
			track = s.Current()
		}
		if track == nil && err == nil {
			return true, c.speak(ctx, gen, ref, request, seq, func(emit func(string) error) error {
				return emit("Ничего не нашлось. Попробуй другое название.")
			}, nil)
		}
	case "next", "prev":
		delta := 1
		if action == "prev" {
			delta = -1
		}
		track, err = m.Step(ctx, c.device, delta, action == "next")
	case "like", "dislike":
		s := m.Session(c.device)
		if s == nil || s.Current() == nil {
			return false, nil
		}
		track, err = m.Rate(ctx, c.device, action == "like")
		if action == "like" && err == nil {
			return true, c.response(ctx, gen, ref, request, seq, "", nil)
		}
	case "info":
		s := m.Session(c.device)
		if s != nil {
			track = s.Current()
		}
		if track == nil {
			return true, c.speak(ctx, gen, ref, request, seq, func(emit func(string) error) error { return emit("Сейчас ничего не играет.") }, nil)
		}
		return true, c.speak(ctx, gen, ref, request, seq, func(emit func(string) error) error { return emit(music.SayTrack(*track)) }, nil)
	default:
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if track == nil {
		return true, c.response(ctx, gen, ref, request, seq, "", nil)
	}
	dirs, e := m.PlayDirectives(ctx, c.device, request, *track)
	if e != nil {
		return true, e
	}
	return true, c.response(ctx, gen, ref, request, seq, "", dirs)
}
func (c *Session) serverAction(ref, request string, seq int, ev Map) {
	name := text(ev["name"])
	m := c.svc.Music
	if m == nil {
		return
	}
	if name == music.GetNext {
		ctx, gen := c.active()
		go func() {
			_, err := c.musicCommand(ctx, gen, ref, request, seq, "next", "")
			if err != nil {
				c.logError("music next failed", err)
				c.logError("music response failed", c.response(ctx, gen, ref, request, seq, "", nil))
			}
		}()
		return
	}
	if !strings.HasPrefix(name, music.CallbackPrefix) {
		return
	}
	payload := obj(ev["payload"])
	trackID := text(payload["track_id"])
	s := m.Session(c.device)
	if s != nil && s.Current() != nil && trackID != "" && trackID != s.Current().ID {
		c.logError("music response failed", c.response(c.ctx, 0, ref, request, seq, "", nil))
		return
	}
	if strings.HasSuffix(name, "started") {
		c.logError("music track-start feedback failed", m.TrackStarted(c.ctx, c.device))
		c.logError("music response failed", c.response(c.ctx, 0, ref, request, seq, "", nil))
		return
	}
	if strings.HasSuffix(name, "failed") {
		if m.TrackFailed(c.device) > 5 {
			c.logError("music response failed", c.response(c.ctx, 0, ref, request, seq, "", nil))
			return
		}
		ctx, gen := c.active()
		go func() {
			_, err := c.musicCommand(ctx, gen, ref, request, seq, "next", "")
			c.logError("music next failed", err)
		}()
		return
	}
	c.logError("music response failed", c.response(c.ctx, 0, ref, request, seq, "", nil))
}
