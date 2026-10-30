// SPDX-License-Identifier: GPL-3.0-only
package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/maintainer64/quasard/internal/aliceapi"
	"github.com/maintainer64/quasard/internal/audio"
	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/llm"
	"github.com/maintainer64/quasard/internal/music"
	P "github.com/maintainer64/quasard/internal/protocol"
	"github.com/maintainer64/quasard/internal/speech"
)

type Map = map[string]any

var ErrNotConnected = errors.New("station not connected")

type Speaker interface {
	Transcribe(context.Context, []int16) (string, error)
	Synthesize(context.Context, string) (speech.PCM, error)
}
type Services struct {
	Config   config.Config
	Dialog   *llm.Dialog
	Music    *music.Music
	Speech   Speaker
	Codec    *aliceapi.Codec
	mu       sync.Mutex
	conns    map[string]map[*Session]bool
	closing  bool
	handlers sync.WaitGroup
}

func New(cfg config.Config, d *llm.Dialog, m *music.Music, sp Speaker, c *aliceapi.Codec) *Services {
	return &Services{Config: cfg, Dialog: d, Music: m, Speech: sp, Codec: c, conns: map[string]map[*Session]bool{}}
}
func (s *Services) Connections() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for id, cs := range s.conns {
		out[id] = len(cs)
	}
	return out
}
func (s *Services) find(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns[id] {
		return c
	}
	return nil
}
func (s *Services) Say(ctx context.Context, id, text string) error {
	c := s.find(id)
	if c == nil {
		return ErrNotConnected
	}
	c.startAnswer("", P.ID(), 0, text, true)
	return nil
}
func (s *Services) Push(ctx context.Context, id string, payload Map) error {
	c := s.find(id)
	if c == nil {
		return ErrNotConnected
	}
	return c.send(ctx, P.Directive("System", "Push", payload, "", 0), 0)
}

// Close stops accepting sessions and waits for all speech workers before models close.
func (s *Services) Close() {
	s.mu.Lock()
	s.closing = true
	connections := []*Session{}
	for _, sessions := range s.conns {
		for c := range sessions {
			connections = append(connections, c)
		}
	}
	s.mu.Unlock()
	for _, c := range connections {
		c.stop()
		c.cancelActive()
		_ = c.conn.CloseNow() // Best-effort shutdown; connection may already be closed.
	}
	s.handlers.Wait()
}

func (s *Services) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	s.handlers.Add(1)
	s.mu.Unlock()
	defer s.handlers.Done()
	conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if e != nil {
		return
	}
	conn.SetReadLimit(16 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() { _ = conn.CloseNow() }() // Best-effort connection cleanup.
	id := r.Header.Get("x-uprx-device-id")
	if id == "" {
		id = "unknown-" + P.ID()
	}
	sid := r.Header.Get("x-uprx-ssid")
	if sid == "" {
		sid = P.ID()
	}
	c := &Session{svc: s, conn: conn, ctx: ctx, stop: cancel, device: id, sid: sid, stream: 2}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	if s.conns[id] == nil {
		s.conns[id] = map[*Session]bool{}
	}
	s.conns[id][c] = true
	s.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.stop()
		c.cancelActive()
		c.workers.Wait()
		s.mu.Lock()
		delete(s.conns[id], c)
		if len(s.conns[id]) == 0 {
			delete(s.conns, id)
		}
		s.mu.Unlock()
	}()
	for {
		kind, b, e := conn.Read(ctx)
		if e != nil {
			return
		}
		if kind == websocket.MessageBinary {
			if bytes.HasPrefix(b, aliceapi.Magic) {
				if s.Codec == nil {
					continue
				}
				c.mu.Lock()
				c.proto = true
				c.mu.Unlock()
				raw, e := s.Codec.DecodeClient(b)
				if e != nil {
					slog.Debug("bad AAPI", "error", e)
					continue
				}
				if msg := aliceapi.ClientToJSON(raw, ""); msg != nil {
					c.onMessage(msg)
				}
			} else {
				c.onAudio(b)
			}
		} else {
			var msg Map
			if json.Unmarshal(b, &msg) == nil {
				c.onMessage(msg)
			}
		}
	}
}

type Session struct {
	svc         *Services
	conn        *websocket.Conn
	ctx         context.Context
	stop        context.CancelFunc
	closed      bool
	device, sid string
	mu          sync.Mutex
	write       sync.Mutex
	proto       bool
	generation  uint64
	cancel      context.CancelFunc
	voice       *voice
	stream      uint32
	workers     sync.WaitGroup
}
type voice struct {
	ref, request string
	seq          int
	id           uint32
	validate     bool
	ctx          context.Context
	cancel       context.CancelFunc
	pipe         *io.PipeWriter
	once         sync.Once
	mu           sync.Mutex
	pcm          []int16
	spoken       bool
	lastSpeech   int
	spotterEnd   bool
	flags        []bool
	finalized    bool
	started      time.Time
	gen          uint64
}

func obj(v any) Map {
	m, _ := v.(map[string]any)
	if m == nil {
		return Map{}
	}
	return m
}
func text(v any) string { s, _ := v.(string); return s }
func number(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case uint32:
		return int(n)
	case string:
		var v int
		if _, err := fmt.Sscan(n, &v); err != nil {
			return 0
		}
		return v
	}
	return 0
}
func (c *Session) cancelActive() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	if c.voice != nil {
		c.voice.cancel()
		_ = c.voice.pipe.CloseWithError(context.Canceled) // Best-effort cancellation cleanup.
		c.voice = nil
	}
	c.generation++
	c.mu.Unlock()
}
func (c *Session) active() (context.Context, uint64) {
	c.cancelActive()
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithCancel(c.ctx)
	c.cancel = cancel
	return ctx, c.generation
}
func (c *Session) current(gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return gen == 0 || gen == c.generation
}
func (c *Session) send(ctx context.Context, msg Map, gen uint64) error {
	c.write.Lock()
	defer c.write.Unlock()
	if !c.current(gen) {
		return context.Canceled
	}
	c.mu.Lock()
	useProto := c.proto
	c.mu.Unlock()
	var b []byte
	var err error
	kind := websocket.MessageText
	if useProto {
		msg = aliceapi.ServerMessage(msg)
		if msg == nil {
			return nil
		}
		kind = websocket.MessageBinary
		b, err = c.svc.Codec.EncodeServer(msg)
	} else {
		b, err = json.Marshal(msg)
	}
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.conn.Write(writeCtx, kind, b)
}
func (c *Session) audio(ctx context.Context, id uint32, b []byte, gen uint64) error {
	c.write.Lock()
	defer c.write.Unlock()
	if !c.current(gen) {
		return context.Canceled
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, id)
	copy(buf[4:], b)
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.conn.Write(writeCtx, websocket.MessageBinary, buf)
}
func (c *Session) logError(message string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn(message, "device", c.device, "error", err)
	}
}

func (c *Session) onMessage(msg Map) {
	if sc, ok := msg["streamcontrol"]; ok {
		c.control(obj(sc))
		return
	}
	event := obj(msg["event"])
	h, p := obj(event["header"]), obj(event["payload"])
	ns, name, ref := text(h["namespace"]), text(h["name"]), text(h["messageId"])
	if ref == "" {
		ref = P.ID()
	}
	switch ns + "." + name {
	case "System.SynchronizeState":
		c.logError("synchronize-state response failed", c.send(c.ctx, P.Sync(ref, c.sid), 0))
		c.logError("push response failed", c.send(c.ctx, P.Push(ref), 0))
	case "System.Ack", "System.Ping":
		return
	case "System.EchoRequest":
		c.logError("echo response failed", c.send(c.ctx, P.Directive("System", "EchoResponse", p, ref, 0), 0))
	case "Vins.CancelRequest":
		c.cancelActive()
	case "Log.Spotter":
		c.logError("spotter acknowledgement failed", c.send(c.ctx, P.Directive("Log", "Ack", Map{}, ref, 0), 0))
	case "Vins.VoiceInput", "ASR.Recognize":
		rh := obj(p["header"])
		request := text(rh["request_id"])
		if request == "" {
			request = P.ID()
		}
		c.startVoice(ref, request, number(rh["sequence_number"]), uint32(number(h["streamId"])), p["enable_spotter_validation"] == true)
	case "Vins.TextInput", "Vins.VinsRequest", "Vins.CustomInput":
		rh := obj(p["header"])
		request := text(rh["request_id"])
		if request == "" {
			request = P.ID()
		}
		ev := obj(obj(p["request"])["event"])
		if ev["type"] == "server_action" {
			c.serverAction(ref, request, number(rh["sequence_number"]), ev)
			return
		}
		t := text(ev["text"])
		if t == "" {
			t = text(p["text"])
		}
		if t == "" {
			t = text(p["query"])
		}
		if t != "" {
			c.startAnswer(ref, request, number(rh["sequence_number"]), t, false)
		}
	case "TTS.Generate":
		t := text(p["text"])
		if t == "" {
			t = text(p["utterance"])
		}
		c.startAnswer(ref, P.ID(), 0, t, true)
	}
}
func (c *Session) startAnswer(ref, request string, seq int, t string, direct bool) {
	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.workers.Add(1)
	c.mu.Unlock()
	ctx, gen := c.active()
	go func() {
		defer c.workers.Done()
		if err := c.answer(ctx, gen, ref, request, seq, t, direct); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("answer failed", "error", err)
		}
	}()
}
func (c *Session) startVoice(ref, request string, seq int, id uint32, validate bool) {
	if id == 0 {
		id = 1
	}
	ctx, gen := c.active()
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	v := &voice{ref: ref, request: request, seq: seq, id: id, validate: validate, ctx: ctx, cancel: cancel, pipe: writer, started: time.Now(), gen: gen}
	c.mu.Lock()
	c.voice = v
	c.mu.Unlock()
	c.logError("voice input acknowledgement failed", c.send(ctx, P.InputAck(ref), gen))
	c.workers.Add(2)
	go func() {
		defer c.workers.Done()
		defer func() { _ = reader.Close() }() // Best-effort pipe cleanup.
		decoder, e := audio.NewDecoder(ctx, reader)
		if e != nil {
			slog.Warn("audio decoder", "error", e)
			c.finalize(v, 2)
			return
		}
		defer func() { _ = decoder.Close() }() // Best-effort decoder cleanup.
		var vad *speech.VAD
		if engine, ok := c.svc.Speech.(interface{ NewVAD() (*speech.VAD, error) }); ok {
			var err error
			vad, err = engine.NewVAD()
			c.logError("Silero initialization failed; using energy fallback", err)
			if vad != nil {
				defer func(v *speech.VAD) { _ = v.Close() }(vad) // Best-effort VAD cleanup.
			}
		}
		buf := make([]int16, 512)
		for {
			n, e := decoder.Read(buf)
			if n > 0 {
				samples := buf[:n]
				voiced := false
				if vad != nil {
					probs, err := vad.Feed(samples)
					if err != nil {
						slog.Warn("Silero inference failed; using energy fallback", "error", err)
						_ = vad.Close() // Best-effort cleanup before energy fallback.
						vad = nil
					}
					if err == nil {
						for _, p := range probs {
							if p >= float32(c.svc.Config.ASR.VADThreshold) {
								voiced = true
							}
						}
					}
				}
				if vad == nil {
					sum := 0.
					for _, x := range samples {
						sum += float64(x) * float64(x)
					}
					rms := math.Sqrt(sum/float64(n)) / 32768
					voiced = 20*math.Log10(rms+1e-12) >= c.svc.Config.ASR.VADEnergyDB
				}
				v.mu.Lock()
				if v.finalized {
					v.mu.Unlock()
					return
				}
				v.pcm = append(v.pcm, samples...)
				v.flags = append(v.flags, voiced)
				duration := len(v.pcm) * 1000 / 16000
				if voiced && (!v.spotterEnd || duration > 300) {
					v.spoken = true
					v.lastSpeech = duration
				}
				end := duration >= c.svc.Config.ASR.MaxUtteranceMS || (!v.spoken && duration >= c.svc.Config.ASR.NoSpeechTimeoutMS) || (v.spoken && duration-v.lastSpeech >= c.svc.Config.ASR.VADSilenceMS)
				v.mu.Unlock()
				if end {
					c.finalize(v, 0)
					return
				}
			}
			if e != nil {
				if !errors.Is(e, context.Canceled) && v.ctx.Err() == nil {
					c.finalize(v, 0)
				}
				return
			}
		}
	}()
	go func() {
		defer c.workers.Done()
		interval := time.Duration(c.svc.Config.ASR.PartialIntervalMS) * time.Millisecond
		if interval <= 0 {
			interval = 300 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		deadline := time.NewTimer(time.Duration(c.svc.Config.ASR.MaxUtteranceMS+3000) * time.Millisecond)
		defer deadline.Stop()
		partial := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				c.finalize(v, 0)
				return
			case <-ticker.C:
				v.mu.Lock()
				duration := len(v.pcm) * 1000 / 16000
				finalized := v.finalized
				v.mu.Unlock()
				if finalized {
					return
				}
				c.logError("partial ASR response failed", c.send(ctx, P.ASRResult(ref, "", partial, false, duration), gen))
				partial++
			}
		}
	}()
}
func (c *Session) onAudio(b []byte) {
	if len(b) < 4 {
		return
	}
	id := binary.BigEndian.Uint32(b)
	c.mu.Lock()
	v := c.voice
	c.mu.Unlock()
	if v != nil && v.id == id {
		if _, err := v.pipe.Write(b[4:]); err != nil && v.ctx.Err() == nil {
			slog.Debug("audio stream write", "error", err)
		}
	}
}
func (c *Session) control(sc Map) {
	c.mu.Lock()
	v := c.voice
	c.mu.Unlock()
	if v == nil || uint32(number(sc["streamId"])) != v.id {
		return
	}
	if number(sc["action"]) == 2 {
		v.mu.Lock()
		v.spotterEnd = true
		v.spoken = false
		v.lastSpeech = 0
		v.mu.Unlock()
		return
	}
	if number(sc["action"]) != 0 {
		return
	}
	reason := number(sc["reason"])
	if reason == 7 {
		c.cancelActive()
		return
	}
	_ = v.pipe.Close() // Best-effort input stream cleanup.
	if reason == 8 {
		v.mu.Lock()
		v.spoken = false
		v.mu.Unlock()
	}
	if reason == 8 {
		go c.finalize(v, reason)
	}
}

var wake = regexp.MustCompile(`(?i)^(?:алиса[,.!?: ]*)+`)

func (c *Session) finalize(v *voice, reason int) {
	v.once.Do(func() {
		_ = v.pipe.Close() // Best-effort input stream cleanup.
		v.mu.Lock()
		v.finalized = true
		pcm := append([]int16(nil), v.pcm...)
		flags := append([]bool(nil), v.flags...)
		spoken := v.spoken
		v.mu.Unlock()
		if v.ctx.Err() != nil || !c.current(v.gen) {
			return
		}
		t := ""
		if spoken && reason != 8 && c.svc.Speech != nil {
			var e error
			t, e = c.svc.Speech.Transcribe(v.ctx, speechOnly(pcm, flags))
			if e != nil {
				slog.Warn("ASR failed", "error", e)
			}
		}
		t = strings.TrimSpace(wake.ReplaceAllString(strings.TrimSpace(t), ""))
		c.logError("final ASR response failed", c.send(v.ctx, P.ASRResult(v.ref, t, 1, true, len(pcm)*1000/16000), v.gen))
		c.logError("voice stream close response failed", c.send(v.ctx, P.Control(v.id, 0, 0), v.gen))
		if v.validate {
			c.logError("voice validation response failed", c.send(v.ctx, P.Validation(v.ref, c.device, t != ""), v.gen))
		}
		v.cancel()
		if t != "" && c.current(v.gen) {
			c.mu.Lock()
			if c.voice == v {
				c.voice = nil
			}
			c.mu.Unlock()
			c.mu.Lock()
			if c.generation != v.gen {
				c.mu.Unlock()
				return
			}
			answerCtx, cancel := context.WithCancel(c.ctx)
			c.cancel = cancel
			c.workers.Add(1)
			c.mu.Unlock()
			go func() {
				defer c.workers.Done()
				if err := c.answer(answerCtx, v.gen, v.ref, v.request, v.seq, t, false); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("voice answer failed", "error", err)
				}
			}()
		} else {
			c.logError("empty recognition response failed", c.send(c.ctx, P.Vins(v.ref, v.request, c.sid, v.seq, "", "", nil, false), v.gen))
		}
	})
}
