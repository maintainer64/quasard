// SPDX-License-Identifier: GPL-3.0-only
// Package music provides native Yandex Music HTTP access and per-device playback queues.
package music

import (
	"context"
	"crypto/md5" // Required by Yandex's MP3 download protocol, not used for security.
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Wave           = "user:onyourwave"
	GetNext        = "@@mm_stack_engine_get_next"
	CallbackPrefix = "aqw_music_"
)

// Config has no dependency on application configuration. Empty endpoints select production.
// TokenFile should normally be <data directory>/music_token.json. Token takes precedence.
// Quality defaults to "high"; "low" prefers 128 kbit/s. HTTPClient must support contexts.
type Config struct {
	Enabled                                      bool
	Token, Quality, TokenFile                    string
	APIBaseURL, OAuthBaseURL                     string
	ClientID, ClientSecret, DeviceID, DeviceName string
	HTTPClient                                   *http.Client
}

type Music struct {
	cfg  Config
	http *http.Client
	// Operations are serialized, including feedback, so callbacks cannot observe partial queues.
	mu         sync.Mutex
	token, uid string
	sessions   map[string]*Session
}

func New(cfg Config) *Music {
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.music.yandex.net"
	}
	if cfg.OAuthBaseURL == "" {
		cfg.OAuthBaseURL = "https://oauth.yandex.ru"
	}
	if cfg.Quality == "" {
		cfg.Quality = "high"
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Music{cfg: cfg, http: h, sessions: make(map[string]*Session)}
}

func (m *Music) loadToken() string {
	if t := strings.TrimSpace(m.cfg.Token); t != "" {
		return t
	}
	if m.token != "" {
		return m.token
	}
	if m.cfg.TokenFile == "" {
		return ""
	}
	b, err := os.ReadFile(m.cfg.TokenFile)
	if err != nil {
		return ""
	}
	var t OAuthToken
	if json.Unmarshal(b, &t) != nil {
		return ""
	}
	return strings.TrimSpace(t.AccessToken)
}

func (m *Music) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Enabled && m.loadToken() != ""
}

// APIError deliberately excludes response bodies and request URLs, which may contain credentials.
type APIError struct {
	Operation string
	Status    int
	Code      string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("music %s: HTTP %d (%s)", e.Operation, e.Status, e.Code)
}

func (m *Music) request(ctx context.Context, method, endpoint string, form url.Values, body any, token string, out any, envelope bool) error {
	var r io.Reader
	if form != nil {
		r = strings.NewReader(form.Encode())
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, r)
	if err != nil {
		return errors.New("music: invalid endpoint")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "OAuth "+token)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("music: HTTP request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return errors.New("music: reading HTTP response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Operation: "request", Status: resp.StatusCode, Code: "request_failed"}
	}
	if envelope {
		var e struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return fmt.Errorf("music: malformed API response: %w", err)
		}
		if len(e.Error) > 0 && string(e.Error) != "null" {
			return &APIError{Operation: "API", Status: resp.StatusCode, Code: "api_error"}
		}
		if len(e.Result) == 0 {
			return errors.New("music: missing API result")
		}
		b = e.Result
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("music: malformed JSON: %w", err)
	}
	return nil
}

func (m *Music) api(ctx context.Context, method, path string, form url.Values, body, out any) error {
	return m.request(ctx, method, strings.TrimRight(m.cfg.APIBaseURL, "/")+path, form, body, m.token, out, true)
}

func (m *Music) ready(ctx context.Context) error {
	if !m.cfg.Enabled {
		return errors.New("music disabled")
	}
	if m.token != "" && m.uid != "" {
		return nil
	}
	token := m.loadToken()
	if token == "" {
		return errors.New("нет токена Яндекс Музыки: запустите music-login")
	}
	var status struct {
		Account struct {
			UID id `json:"uid"`
		} `json:"account"`
	}
	old := m.token
	m.token = token
	if err := m.api(ctx, http.MethodGet, "/account/status", nil, nil, &status); err != nil {
		m.token = old
		return err
	}
	if status.Account.UID == "" {
		m.token = old
		return errors.New("music: account status has no UID")
	}
	m.uid = string(status.Account.UID)
	return nil
}

// id accepts API identifiers encoded as either JSON strings or numbers without float rounding.
type id string

func (i *id) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*i = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*i = id(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*i = id(n.String())
	return nil
}

type apiTrack struct {
	ID      id     `json:"id"`
	Title   string `json:"title"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	DurationMS int    `json:"durationMs"`
	CoverURI   string `json:"coverUri"`
	Albums     []struct {
		ID id `json:"id"`
	} `json:"albums"`
	Available    *bool     `json:"available"`
	Track        *apiTrack `json:"track"`
	ShortAlbumID id        `json:"albumId"`
}

func (t apiTrack) info() TrackInfo {
	var names []string
	for _, a := range t.Artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	ti := TrackInfo{ID: string(t.ID), Title: t.Title, Artists: strings.Join(names, ", "), DurationMS: t.DurationMS, Cover: t.CoverURI}
	if len(t.Albums) > 0 {
		ti.AlbumID = string(t.Albums[0].ID)
	}
	return ti
}
func infos(tracks []apiTrack) []TrackInfo {
	out := make([]TrackInfo, 0, len(tracks))
	for _, t := range tracks {
		if t.Track != nil {
			t = *t.Track
		}
		if t.Available != nil && !*t.Available {
			continue
		}
		if t.ID != "" {
			out = append(out, t.info())
		}
	}
	return out
}

// StreamURL resolves Yandex's legacy download-info XML to a signed direct MP3 URL.
// Returned bitrate is kbit/s. No audio is downloaded. Non-MP3 codecs are never mislabeled as MP3.
func (m *Music) StreamURL(ctx context.Context, t TrackInfo) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return "", 0, err
	}
	return m.streamURL(ctx, t)
}
func (m *Music) streamURL(ctx context.Context, t TrackInfo) (string, int, error) {
	var list []struct {
		Codec   string `json:"codec"`
		Bitrate int    `json:"bitrateInKbps"`
		URL     string `json:"downloadInfoUrl"`
	}
	if err := m.api(ctx, http.MethodGet, "/tracks/"+url.PathEscape(t.ID)+"/download-info", nil, nil, &list); err != nil {
		return "", 0, err
	}
	target := 320
	if m.cfg.Quality != "high" {
		target = 128
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if (a.Codec == "mp3") != (b.Codec == "mp3") {
			return a.Codec == "mp3"
		}
		return abs(a.Bitrate-target) < abs(b.Bitrate-target)
	})
	var last error
	for _, d := range list {
		if d.Codec != "mp3" {
			continue
		}
		direct, err := m.downloadURL(ctx, d.URL)
		if err == nil {
			return direct, d.Bitrate, nil
		}
		last = err
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
	}
	if last != nil {
		return "", 0, last
	}
	return "", 0, fmt.Errorf("music: no MP3 stream for track %s", t.ID)
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func (m *Music) downloadURL(ctx context.Context, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("music: invalid download-info URL")
	}
	// Download-info is a separate host; never send OAuth credentials to it.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("music: download-info request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Operation: "download-info", Status: resp.StatusCode, Code: "request_failed"}
	}
	var d struct {
		Host string `xml:"host"`
		Path string `xml:"path"`
		TS   string `xml:"ts"`
		S    string `xml:"s"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return "", fmt.Errorf("music: download-info XML: %w", err)
	}
	if d.Host == "" || strings.ContainsAny(d.Host, "/?#@\\ \t\r\n") || !strings.HasPrefix(d.Path, "/") || d.TS == "" || d.S == "" {
		return "", errors.New("music: incomplete download-info XML")
	}
	sum := md5.Sum([]byte("XGRlBW9FXlekgbPrRHuSiA" + strings.TrimPrefix(d.Path, "/") + d.S))
	return "https://" + d.Host + "/get-mp3/" + hex.EncodeToString(sum[:]) + "/" + d.TS + d.Path, nil
}
