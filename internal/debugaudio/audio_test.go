// SPDX-License-Identifier: GPL-3.0-only
package debugaudio

import (
	"encoding/binary"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func download(s *Store, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}
func clip() Clip {
	return Clip{DeviceID: "station", RequestID: "turn", Engine: "whisper", Language: "auto", Input: []int16{-32768, 0, 32767, 123}, Recognition: []int16{123}}
}
func mustCapture(t *testing.T, s *Store, c Clip) string {
	t.Helper()
	id, err := s.Capture(c)
	if err != nil || id == "" {
		t.Fatalf("capture: id=%q err=%v", id, err)
	}
	return id
}
func metadata(t *testing.T, s *Store, id string) Metadata {
	t.Helper()
	w := download(s, http.MethodGet, Prefix+id+"/metadata.json")
	var m Metadata
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
		t.Fatal("invalid metadata response")
	}
	return m
}
func TestWAVDownloadAndDebugGate(t *testing.T) {
	var enabled atomic.Bool
	s := New(enabled.Load)
	if id, err := s.Capture(clip()); id != "" || err != nil {
		t.Fatal("recorded outside DEBUG")
	}
	enabled.Store(true)
	c := clip()
	id := mustCapture(t, s, c)
	c.Input[0] = 99
	w := download(s, http.MethodGet, Prefix+id+"/input.wav")
	b := w.Body.Bytes()
	if w.Code != 200 || len(b) != 52 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatal("invalid WAV header")
	}
	if binary.LittleEndian.Uint32(b[4:8]) != uint32(len(b)-8) || binary.LittleEndian.Uint32(b[24:28]) != 16000 || binary.LittleEndian.Uint16(b[22:24]) != 1 || binary.LittleEndian.Uint16(b[34:36]) != 16 || binary.LittleEndian.Uint32(b[40:44]) != 8 {
		t.Fatal("invalid PCM format")
	}
	if int16(binary.LittleEndian.Uint16(b[44:46])) != -32768 {
		t.Fatal("input was not copied")
	}
	disposition, parameters, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || parameters["filename"] != "input.wav" {
		t.Fatal("invalid download filename")
	}
	if w.Header().Get("Content-Type") != "audio/wav" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("download headers missing")
	}
	if h := download(s, http.MethodHead, Prefix+id+"/input.wav"); h.Code != 200 || h.Body.Len() != 0 || h.Header().Get("Content-Length") != "52" {
		t.Fatal("HEAD not supported")
	}
	m := metadata(t, s, id)
	if m.Input.ClippedSamples != 2 || m.ASRStatus != "pending" || m.DeviceID != "station" {
		t.Fatal("missing signal metadata")
	}
	enabled.Store(false)
	if download(s, http.MethodGet, Prefix+id+"/input.wav").Code != 404 || s.last != nil {
		t.Fatal("DEBUG off did not clear and hide recording")
	}
	enabled.Store(true)
	if download(s, http.MethodGet, Prefix+id+"/input.wav").Code != 404 {
		t.Fatal("old recording returned after re-enabling DEBUG")
	}
}
func TestLatestOnlyAndLateASRResult(t *testing.T) {
	s := New(func() bool { return true })
	first := mustCapture(t, s, clip())
	second := mustCapture(t, s, clip())
	if first == second || download(s, http.MethodGet, Prefix+first+"/input.wav").Code != 404 {
		t.Fatal("previous capture still downloadable")
	}
	s.Finish(first, "old", "old", false, 999)
	if metadata(t, s, second).ASRStatus != "pending" {
		t.Fatal("late ASR modified new capture")
	}
	s.Finish(second, "Алиса, привет", "привет", false, 42)
	m := metadata(t, s, second)
	if m.RawText != "Алиса, привет" || m.Text != "привет" || m.ASRElapsedMS != 42 || m.ASRStatus != "done" {
		t.Fatal("ASR result missing")
	}
	if download(s, http.MethodGet, Prefix+second+"/asr.wav").Body.Len() != 46 {
		t.Fatal("ASR audio not separate from original")
	}
	for _, path := range []string{Prefix, Prefix + "latest.wav", Prefix + second + "/../input.wav", Prefix + second + "/unknown", Prefix + "bad/metadata.json"} {
		if download(s, http.MethodGet, path).Code != 404 {
			t.Fatal("unexpected public listing or path", path)
		}
	}
	if download(s, http.MethodPost, Prefix+second+"/input.wav").Code != 405 {
		t.Fatal("download endpoint accepted POST")
	}
	s.Clear()
	if download(s, http.MethodGet, Prefix+second+"/metadata.json").Code != 404 {
		t.Fatal("clear retained capture")
	}
}
func TestBoundedCaptureAndSilentMetadata(t *testing.T) {
	s := New(func() bool { return true })
	pcm := make([]int16, maxSamples+32)
	id := mustCapture(t, s, Clip{Input: pcm, Recognition: pcm})
	m := metadata(t, s, id)
	if !m.Input.Truncated || !m.Recognition.Truncated || m.Input.DurationMS != 30000 || len(s.last.input) != maxSamples || len(s.last.recognition) != maxSamples {
		t.Fatal("audio memory not bounded")
	}
	if m.Input.RMSDBFS != -240 || m.Input.PeakDBFS != -240 {
		t.Fatal("silence metadata not finite")
	}
	s.Finish(id, "", "", true, 1)
	if metadata(t, s, id).ASRStatus != "failed" {
		t.Fatal("recognition failure missing")
	}
}
func TestConcurrentCaptureDownloadAndClear(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	s := New(enabled.Load)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 25 {
				id, err := s.Capture(clip())
				if err != nil {
					t.Error(err)
					return
				}
				if id != "" {
					s.Finish(id, "test", "test", false, 1)
					w := download(s, http.MethodGet, Prefix+id+"/metadata.json")
					if w.Code != 200 && w.Code != 404 {
						t.Error("unexpected status", w.Code)
					}
				}
			}
		}()
	}
	for range 25 {
		enabled.Store(false)
		s.Clear()
		enabled.Store(true)
	}
	workers.Wait()
}
