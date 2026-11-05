// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/maintainer64/quasard/internal/aliceapi"
	"github.com/maintainer64/quasard/internal/protocol"
)

type wireMap = map[string]any

func oggPages(data []byte) ([][]byte, error) {
	var pages [][]byte
	for pos := 0; pos < len(data); {
		if len(data)-pos < 27 || !bytes.Equal(data[pos:pos+4], []byte("OggS")) {
			return nil, fmt.Errorf("invalid or truncated Ogg page")
		}
		segments := int(data[pos+26])
		if len(data)-pos < 27+segments {
			return nil, fmt.Errorf("truncated Ogg lacing")
		}
		total := 27 + segments
		for _, n := range data[pos+27 : pos+27+segments] {
			total += int(n)
		}
		if total > len(data)-pos {
			return nil, fmt.Errorf("truncated Ogg payload")
		}
		pages = append(pages, data[pos:pos+total])
		pos += total
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("empty Ogg input")
	}
	return pages, nil
}
func stationEvent(kind string, payload wireMap, stream uint32, protoMode bool) wireMap {
	id := protocol.ID()
	if protoMode {
		h := wireMap{"MessageId": id}
		if stream != 0 {
			h["StreamId"] = stream
		}
		return wireMap{"Event": wireMap{"Header": h, kind: payload}}
	}
	ns, name := "Vins", kind
	if kind == "SynchronizeStateRequest" {
		ns, name = "System", "SynchronizeState"
	}
	h := wireMap{"namespace": ns, "name": name, "messageId": id}
	if stream != 0 {
		h["streamId"] = stream
	}
	return wireMap{"event": wireMap{"header": h, "payload": payload}}
}
func stationControl(action int, protoMode bool) wireMap {
	if protoMode {
		kind := "Close"
		body := wireMap{"Reason": "OK"}
		if action == 2 {
			kind = "SpotterEnd"
			body = wireMap{}
		}
		return wireMap{"StreamControl": wireMap{"MessageId": protocol.ID(), "StreamId": 1, kind: body}}
	}
	return wireMap{"streamcontrol": wireMap{"messageId": protocol.ID(), "streamId": 1, "action": action, "reason": 0}}
}
func stationInput(input string, voice, protoMode bool) wireMap {
	typ := "text_input"
	if voice {
		typ = "voice_input"
	}
	if protoMode {
		event := wireMap{"Type": typ}
		if !voice {
			event["Text"] = input
		}
		return wireMap{"Header": wireMap{"RequestId": protocol.ID(), "SequenceNumber": 0}, "Request": wireMap{"Event": event}, "EnableSpotterValidation": voice}
	}
	event := wireMap{"type": typ}
	if !voice {
		event["text"] = input
	}
	payload := wireMap{"header": wireMap{"request_id": protocol.ID(), "sequence_number": 0}, "request": wireMap{"event": event}, "enable_spotter_validation": voice}
	if voice {
		payload["format"], payload["lang"] = "audio/opus", "ru-RU"
	}
	return payload
}
func station(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	input := f.String("text", "", "text query instead of Ogg audio")
	endpoint := f.String("url", "ws://127.0.0.1:80/uni.ws", "local simulator WebSocket endpoint")
	realtime := f.Bool("realtime", false, "pace audio pages at 120ms")
	tail := f.Float64("tail-silence", 2, "seconds of repeated final audio page")
	protoMode := f.Bool("proto", false, "use firmware 3.x AAPI dynamic protobuf")
	descriptor := f.String("descriptor", "alice_server/aliceapi.desc", "protobuf descriptor set")
	dir := f.String("out", "data", "private TTS response output directory")
	timeout := f.Duration("timeout", 60*time.Second, "overall simulation timeout")
	verify := f.Bool("verify", false, "verify WSS TLS certificates")
	if e := parseFlags(f, args); e != nil {
		return e
	}
	if f.NArg() > 1 || (*input == "" && f.NArg() != 1) || (*input != "" && f.NArg() != 0) {
		return fmt.Errorf("supply either one Ogg file or --text")
	}
	if *timeout <= 0 || *tail < 0 || *tail > 60 || math.IsNaN(*tail) || math.IsInf(*tail, 0) {
		return fmt.Errorf("invalid timeout or tail-silence (0..60 seconds)")
	}
	u, e := url.Parse(*endpoint)
	if e != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil {
		return fmt.Errorf("invalid WebSocket URL")
	}
	q := u.Query()
	q.Set("uuid", deviceID)
	q.Set("app", "quasar")
	q.Set("lang", "ru-RU")
	q.Set("format", "opus")
	q.Set("topic", "dialog")
	u.RawQuery = q.Encode()
	var pages [][]byte
	if f.NArg() == 1 {
		b, e := os.ReadFile(f.Arg(0))
		if e != nil {
			return fmt.Errorf("cannot read Ogg input")
		}
		pages, e = oggPages(b)
		if e != nil {
			return e
		}
	}
	var codec *aliceapi.Codec
	if *protoMode {
		codec, e = aliceapi.Load(*descriptor)
		if e != nil {
			return fmt.Errorf("cannot load AAPI descriptor")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !*verify}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	ws, _, e := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, HTTPHeader: http.Header{"User-Agent": {userAgent}, "X-UPRX-DEVICE-ID": {deviceID}, "X-UPRX-SSID": {protocol.ID()}, "X-UPRX-UUID": {protocol.ID()}, "X-UPRX-AUTH-TOKEN": {"sim"}}})
	if e != nil {
		return fmt.Errorf("WebSocket connection failed")
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(16 << 20)
	send := func(v wireMap) error {
		var b []byte
		var e error
		typ := websocket.MessageText
		if *protoMode {
			typ = websocket.MessageBinary
			b, e = codec.EncodeClient(v)
		} else {
			b, e = json.Marshal(v)
		}
		if e != nil {
			return e
		}
		return ws.Write(ctx, typ, b)
	}
	sync := wireMap{"uuid": deviceID, "auth_token": "sim-token", "is_tv": false, "vins": wireMap{"application": "quasar", "app_id": "ru.yandex.quasar.app", "lang": "ru-RU", "platform": "Linux", "platform_name": "yandexstation_2", "device_id": deviceID}}
	if *protoMode {
		sync = wireMap{}
	}
	if e = send(stationEvent("SynchronizeStateRequest", sync, 0, *protoMode)); e != nil {
		return fmt.Errorf("cannot send synchronization")
	}
	kind := "TextInput"
	stream := uint32(0)
	if len(pages) > 0 {
		kind = "VoiceInput"
		stream = 1
	}
	if e = send(stationEvent(kind, stationInput(*input, len(pages) > 0, *protoMode), stream, *protoMode)); e != nil {
		return fmt.Errorf("cannot send input: %w", e)
	}
	// Sending runs concurrently with receiving so long audio cannot block directives.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		var err error
		writePage := func(p []byte) error {
			b := make([]byte, len(p)+4)
			binary.BigEndian.PutUint32(b, 1)
			copy(b[4:], p)
			return ws.Write(ctx, websocket.MessageBinary, b)
		}
		spotterEnd, bos := 1, 0
		for i, p := range pages {
			if p[5]&2 != 0 {
				bos++
				if bos == 2 {
					spotterEnd = i
					break
				}
			}
		}
		for i, p := range pages {
			if i == spotterEnd {
				if err = send(stationControl(2, *protoMode)); err != nil {
					break
				}
			}
			if err = writePage(p); err != nil {
				break
			}
			if *realtime && i > spotterEnd {
				if err = wait(ctx, 120*time.Millisecond); err != nil {
					break
				}
			}
		}
		if err == nil && len(pages) > 0 {
			for n := 0; n < int(*tail/.12); n++ {
				if err = writePage(pages[len(pages)-1]); err != nil {
					break
				}
				if err = wait(ctx, 120*time.Millisecond); err != nil {
					break
				}
			}
			if err == nil {
				if err := send(stationControl(0, *protoMode)); err != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-sent }()
	streams := map[uint32]*bytes.Buffer{}
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return fmt.Errorf("station response ended before stream close")
		}
		var msg wireMap
		if typ == websocket.MessageBinary && bytes.HasPrefix(b, aliceapi.Magic) {
			msg, err = codecDecode(codec, b)
			if err != nil {
				return err
			}
		} else if typ == websocket.MessageBinary {
			if len(b) < 4 {
				return fmt.Errorf("short audio response frame")
			}
			id := binary.BigEndian.Uint32(b[:4])
			buf := streams[id]
			if buf == nil {
				buf = &bytes.Buffer{}
				streams[id] = buf
			}
			if buf.Len()+len(b)-4 > 64<<20 {
				return fmt.Errorf("audio response too large")
			}
			buf.Write(b[4:])
			continue
		} else if err = json.Unmarshal(b, &msg); err != nil {
			return fmt.Errorf("invalid JSON response")
		}
		control := object(msg["streamcontrol"])
		protoControl := object(msg["StreamControl"])
		if len(protoControl) > 0 {
			if _, closed := protoControl["Close"]; closed {
				control = wireMap{"streamId": protoControl["StreamId"], "action": float64(0)}
			}
		}
		if len(control) > 0 && fmt.Sprint(control["action"]) == "0" {
			id := uint32(number(control["streamId"]))
			if id%2 != 0 {
				continue
			} // Input close precedes the spoken response.
			if buf := streams[id]; buf != nil && buf.Len() > 0 {
				if err = privateWrite(filepath.Join(*dir, fmt.Sprintf("sim_answer_%d.ogg", id)), buf.Bytes()); err != nil {
					return err
				}
				if _, err = fmt.Fprintln(out, "Saved private audio response."); err != nil {
					return err
				}
			}
			return nil
		}
		// Do not print recognized text or directive bodies, which may contain secrets.
	}
}
func codecDecode(c *aliceapi.Codec, b []byte) (wireMap, error) {
	if c == nil {
		return nil, fmt.Errorf("unexpected AAPI response in JSON mode")
	}
	v, e := c.DecodeServer(b)
	if e != nil {
		return nil, fmt.Errorf("invalid AAPI response")
	}
	return v, nil
}
func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
