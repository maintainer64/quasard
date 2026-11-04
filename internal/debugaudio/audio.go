// SPDX-License-Identifier: GPL-3.0-only

// Package debugaudio keeps only the latest ASR input in memory for DEBUG downloads.
package debugaudio

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

const Prefix = "/_debug/audio/"
const SampleRate = 16000
const maxSamples = 30 * SampleRate

// Clip is one utterance before and after the VAD filter, never an audio history.
type Clip struct {
	DeviceID, RequestID, Engine, Language string
	Input, Recognition                    []int16
}

type WaveInfo struct {
	SourceDurationMS int     `json:"source_duration_ms"`
	DurationMS       int     `json:"duration_ms"`
	Truncated        bool    `json:"truncated"`
	RMSDBFS          float64 `json:"rms_dbfs"`
	PeakDBFS         float64 `json:"peak_dbfs"`
	ClippedSamples   int     `json:"clipped_samples"`
}

type Metadata struct {
	ID           string    `json:"id"`
	CapturedAt   time.Time `json:"captured_at"`
	DeviceID     string    `json:"device_id"`
	RequestID    string    `json:"request_id"`
	Engine       string    `json:"engine"`
	Language     string    `json:"language"`
	SampleRate   int       `json:"sample_rate"`
	Input        WaveInfo  `json:"input"`
	Recognition  WaveInfo  `json:"recognition"`
	ASRStatus    string    `json:"asr_status"`
	ASRElapsedMS int64     `json:"asr_elapsed_ms"`
	RawText      string    `json:"raw_text"`
	Text         string    `json:"text"`
}

type capture struct {
	meta               Metadata
	input, recognition []int16
}

type Store struct {
	mu      sync.Mutex
	enabled func() bool
	last    *capture
}

// New does not create files, timers or background workers. The caller clears the
// store when toggling DEBUG off; each operation also checks the effective level.
func New(enabled func() bool) *Store { return &Store{enabled: enabled} }

func (s *Store) active() bool {
	if s.enabled == nil || !s.enabled() {
		s.last = nil
		return false
	}
	return true
}

func (s *Store) Clear() { s.mu.Lock(); s.last = nil; s.mu.Unlock() }

// Capture copies at most 30 seconds of each view and invalidates the old URL.
func (s *Store) Capture(c Clip) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active() || len(c.Input) == 0 {
		return "", nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	input := append([]int16(nil), c.Input[:min(len(c.Input), maxSamples)]...)
	recognition := append([]int16(nil), c.Recognition[:min(len(c.Recognition), maxSamples)]...)
	id := hex.EncodeToString(nonce[:])
	s.last = &capture{input: input, recognition: recognition, meta: Metadata{
		ID: id, CapturedAt: time.Now().UTC(), DeviceID: limited(c.DeviceID, 256), RequestID: limited(c.RequestID, 256),
		Engine: limited(c.Engine, 64), Language: limited(c.Language, 32), SampleRate: SampleRate,
		Input: waveInfo(input, len(c.Input)), Recognition: waveInfo(recognition, len(c.Recognition)), ASRStatus: "pending",
	}}
	return id, nil
}

// Finish cannot overwrite a newer utterance that arrived while ASR was running.
func (s *Store) Finish(id, raw, text string, failed bool, elapsedMS int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active() || s.last == nil || s.last.meta.ID != id {
		return
	}
	s.last.meta.RawText = limited(raw, 4096)
	s.last.meta.Text = limited(text, 4096)
	s.last.meta.ASRElapsedMS = elapsedMS
	s.last.meta.ASRStatus = "done"
	if failed {
		s.last.meta.ASRStatus = "failed"
	}
}

func limited(value string, limit int) string {
	count := 0
	for i := range value {
		if count == limit {
			return strings.Clone(value[:i])
		}
		count++
	}
	return strings.Clone(value)
}

func waveInfo(pcm []int16, sourceSamples int) WaveInfo {
	var sum, peak float64
	clipped := 0
	for _, sample := range pcm {
		v := float64(sample)
		sum += v * v
		peak = max(peak, math.Abs(v))
		if sample == 32767 || sample == -32768 {
			clipped++
		}
	}
	rms := 0.0
	if len(pcm) > 0 {
		rms = math.Sqrt(sum / float64(len(pcm)))
	}
	return WaveInfo{SourceDurationMS: sourceSamples * 1000 / SampleRate, DurationMS: len(pcm) * 1000 / SampleRate,
		Truncated: sourceSamples > len(pcm), RMSDBFS: 20 * math.Log10(max(rms/32768, 1e-12)),
		PeakDBFS: 20 * math.Log10(max(peak/32768, 1e-12)), ClippedSamples: clipped}
}

func wav(pcm []int16) []byte {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], SampleRate)
	binary.LittleEndian.PutUint32(b[28:32], SampleRate*2)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(2*len(pcm)))
	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(sample))
	}
	return b
}

// ServeHTTP requires the unguessable URL emitted in the private DEBUG log. There
// is deliberately no public "latest" or listing endpoint. Downloads never cache.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(r.URL.Path, Prefix) {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, Prefix), "/")
	s.mu.Lock()
	if !s.active() || s.last == nil || len(parts) != 2 || parts[0] != s.last.meta.ID {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	snapshot := *s.last
	s.mu.Unlock()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var data []byte
	switch parts[1] {
	case "input.wav":
		data = wav(snapshot.input)
		w.Header().Set("Content-Type", "audio/wav")
	case "asr.wav":
		data = wav(snapshot.recognition)
		w.Header().Set("Content-Type", "audio/wav")
	case "metadata.json":
		var err error
		data, err = json.MarshalIndent(snapshot.meta, "", "  ")
		if err != nil {
			http.Error(w, "cannot encode debug metadata", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+parts[1]+`"`)
	http.ServeContent(w, r, parts[1], snapshot.meta.CapturedAt, bytes.NewReader(data))
}
