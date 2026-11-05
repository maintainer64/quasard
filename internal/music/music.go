// SPDX-License-Identifier: GPL-3.0-only
package music

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maintainer64/quasard/internal/protocol"
)

type TrackInfo struct {
	ID, Title, Artists, Cover, AlbumID string
	DurationMS                         int
}
type Session struct {
	Kind, Title, Station, BatchID string
	Queue                         []TrackInfo
	Pos                           int
	StartedAt                     time.Time
	FailStreak                    int
}

func (s *Session) Current() *TrackInfo {
	if s == nil || s.Pos < 0 || s.Pos >= len(s.Queue) {
		return nil
	}
	t := s.Queue[s.Pos]
	return &t
}
func snapshot(s *Session) *Session {
	if s == nil {
		return nil
	}
	c := *s
	c.Queue = append([]TrackInfo(nil), s.Queue...)
	return &c
}

// Session returns an isolated snapshot; callers cannot mutate playback state.
func (m *Music) Session(device string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return snapshot(m.sessions[device])
}
func (m *Music) TrackFailed(device string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[device]
	if s == nil {
		return 0
	}
	s.FailStreak++
	return s.FailStreak
}

func (m *Music) feedback(ctx context.Context, s *Session, kind, track string, played *float64) error {
	body := map[string]any{"type": kind, "timestamp": float64(time.Now().UnixMilli()) / 1000}
	if kind == "radioStarted" {
		body["from"] = "mobile-radio-user-onyourwave"
	}
	if track != "" {
		body["trackId"] = track
	}
	if played != nil {
		body["totalPlayedSeconds"] = *played
	}
	path := "/rotor/station/" + url.PathEscape(s.Station) + "/feedback"
	if s.BatchID != "" {
		path += "?" + url.Values{"batch-id": {s.BatchID}}.Encode()
	}
	return m.api(ctx, http.MethodPost, path, nil, body, nil)
}
func (m *Music) radioTracks(ctx context.Context, s *Session, previous string) error {
	path := "/rotor/station/" + url.PathEscape(s.Station) + "/tracks"
	if previous != "" {
		path += "?" + url.Values{"queue": {previous}}.Encode()
	}
	var r struct {
		BatchID  string `json:"batchId"`
		Sequence []struct {
			Track apiTrack `json:"track"`
		} `json:"sequence"`
	}
	if err := m.api(ctx, http.MethodGet, path, nil, nil, &r); err != nil {
		return err
	}
	s.BatchID = r.BatchID
	for _, x := range r.Sequence {
		if x.Track.Available == nil || *x.Track.Available {
			s.Queue = append(s.Queue, x.Track.info())
		}
	}
	return nil
}
func (m *Music) StartWave(ctx context.Context, device string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	s := &Session{Kind: "radio", Title: "Моя волна", Station: Wave, Pos: -1}
	_ = m.feedback(ctx, s, "radioStarted", "", nil)
	if err := m.radioTracks(ctx, s, ""); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m.sessions[device] = s
	return snapshot(s), nil
}

func (m *Music) StartSearch(ctx context.Context, device, query string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	var r struct {
		Best struct {
			Type   string    `json:"type"`
			Result jsonTrack `json:"result"`
		} `json:"best"`
		Tracks struct {
			Results []apiTrack `json:"results"`
		} `json:"tracks"`
	}
	path := "/search?" + url.Values{"text": {query}, "type": {"all"}, "page": {"0"}, "nocorrect": {"False"}}.Encode()
	if err := m.api(ctx, http.MethodGet, path, nil, nil, &r); err != nil {
		return nil, err
	}
	var tracks []apiTrack
	title := query
	switch r.Best.Type {
	case "track":
		t := r.Best.Result.Track
		tracks = []apiTrack{t}
		title = t.Title
	case "artist":
		b := r.Best.Result
		var a struct {
			Tracks []apiTrack `json:"tracks"`
		}
		if err := m.api(ctx, http.MethodGet, "/artists/"+url.PathEscape(string(b.Track.ID))+"/tracks?page=0&page-size=50", nil, nil, &a); err != nil {
			return nil, err
		}
		tracks = a.Tracks
		title = b.Name
	case "album":
		b := r.Best.Result
		var a struct {
			Volumes [][]apiTrack `json:"volumes"`
			Title   string       `json:"title"`
		}
		if err := m.api(ctx, http.MethodGet, "/albums/"+url.PathEscape(string(b.Track.ID))+"/with-tracks", nil, nil, &a); err != nil {
			return nil, err
		}
		// Source traverses API volumes in order. No shuffle, artist expansion, or new album semantics.
		for _, v := range a.Volumes {
			tracks = append(tracks, v...)
		}
		title = a.Title
	case "playlist":
		b := r.Best.Result
		if b.UID != "" && b.Kind != "" {
			var p struct {
				Tracks []apiTrack `json:"tracks"`
				Title  string     `json:"title"`
			}
			if err := m.api(ctx, http.MethodGet, "/users/"+url.PathEscape(string(b.UID))+"/playlists/"+url.PathEscape(string(b.Kind)), nil, nil, &p); err != nil {
				return nil, err
			}
			// fetch_tracks returns TrackShorts; fetch_track resolves id:albumId via /tracks.
			// Batch same ordered IDs rather than making one HTTP request per short track.
			ids := make([]string, 0, len(p.Tracks))
			for _, t := range p.Tracks {
				trackID := string(t.ID)
				if t.ShortAlbumID != "" {
					trackID += ":" + string(t.ShortAlbumID)
				}
				ids = append(ids, trackID)
			}
			if len(ids) > 0 {
				if err := m.api(ctx, http.MethodPost, "/tracks", url.Values{"track-ids": {strings.Join(ids, ",")}}, nil, &tracks); err != nil {
					return nil, err
				}
			}
			title = p.Title
		}
	}
	if len(tracks) == 0 {
		tracks = r.Tracks.Results
	}
	queue := infos(tracks)
	if len(queue) == 0 {
		return nil, fmt.Errorf("в Музыке ничего не нашлось по запросу «%s»", query)
	}
	s := &Session{Kind: "list", Title: title, Queue: queue, Pos: -1}
	m.sessions[device] = s
	return snapshot(s), nil
}

// jsonTrack contains fields shared by best-result object variants.
type jsonTrack struct {
	Track     apiTrack
	Name      string
	UID, Kind id
}

func (b *jsonTrack) UnmarshalJSON(data []byte) error {
	var v struct {
		apiTrack
		Name string `json:"name"`
		UID  id     `json:"uid"`
		Kind id     `json:"kind"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	b.Track = v.apiTrack
	b.Name = v.Name
	b.UID = v.UID
	b.Kind = v.Kind
	return nil
}

func (m *Music) Step(ctx context.Context, device string, delta int, skipped bool) (*TrackInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[device]
	if s == nil {
		return nil, nil
	}
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	previous := s.Current()
	if s.Kind == "radio" && previous != nil && delta > 0 {
		played := 0.0
		if !s.StartedAt.IsZero() {
			played = time.Since(s.StartedAt).Seconds()
			if played < 0 {
				played = 0
			}
		}
		kind := "trackFinished"
		if skipped {
			kind = "skip"
		}
		_ = m.feedback(ctx, s, kind, previous.ID, &played)
	}
	pos := s.Pos + delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(s.Queue) && s.Kind == "radio" {
		last := ""
		if previous != nil {
			last = previous.ID
		}
		if err := m.radioTracks(ctx, s, last); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if pos >= len(s.Queue) {
		return nil, nil
	}
	s.Pos = pos
	s.StartedAt = time.Now()
	return s.Current(), nil
}
func (m *Music) TrackStarted(ctx context.Context, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[device]
	if s == nil {
		return nil
	}
	t := s.Current()
	if t == nil {
		return nil
	}
	s.StartedAt = time.Now()
	s.FailStreak = 0
	if s.Kind == "radio" {
		if err := m.ready(ctx); err != nil {
			return err
		}
		return m.feedback(ctx, s, "trackStarted", t.ID, nil)
	}
	return nil
}
func (m *Music) Rate(ctx context.Context, device string, like bool) (*TrackInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[device]
	if s == nil || s.Current() == nil {
		return nil, nil
	}
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	t := s.Current()
	prefix := "/users/" + url.PathEscape(m.uid)
	if like {
		if err := m.api(ctx, http.MethodPost, prefix+"/likes/tracks/add-multiple", url.Values{"track-ids": {t.ID}}, nil, nil); err != nil {
			return nil, err
		}
	} else {
		_ = m.api(ctx, http.MethodPost, prefix+"/likes/tracks/remove", url.Values{"track-ids": {t.ID}}, nil, nil)
		if err := m.api(ctx, http.MethodPost, prefix+"/dislikes/tracks/add-multiple", url.Values{"track-ids": {t.ID}}, nil, nil); err != nil {
			return nil, err
		}
	}
	if s.Kind == "radio" {
		kind := "dislike"
		if like {
			kind = "like"
		}
		_ = m.feedback(ctx, s, kind, t.ID, nil)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return t, nil
}
func SayTrack(t TrackInfo) string {
	if t.Artists != "" {
		return t.Title + ", " + t.Artists
	}
	return t.Title
}

// PlayDirectives preserves music.py actions, callback state and metadata. The Python
// source references absent protocol helpers. Envelope and metadata below are
// conservative reconstructions, tested against fixtures, not verified on hardware.
func (m *Music) PlayDirectives(ctx context.Context, device, requestID string, t TrackInfo) ([]map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	direct, kbps, err := m.streamURL(ctx, t)
	if err != nil {
		return nil, err
	}
	entity := protocol.ID()
	playID := protocol.ID()
	title := "Музыка"
	if s := m.sessions[device]; s != nil && s.Title != "" {
		title = s.Title
	}
	action := map[string]any{"name": GetNext, "payload": map[string]any{"command": map[string]any{"type": "server_action", "name": GetNext}, "music_request_id": requestID, "entity_id": entity}}
	callback := func(kind string) map[string]any {
		return map[string]any{"command": map[string]any{"type": "server_action", "name": CallbackPrefix + kind, "payload": map[string]any{"device": device, "track_id": t.ID}}, "ignore_answer": true}
	}
	metadata := map[string]any{"track_id": t.ID, "album_id": t.AlbumID, "title": t.Title, "artists": t.Artists, "duration_ms": t.DurationMS, "cover": t.Cover}
	payload := map[string]any{"id": playID, "stream": map[string]any{"url": direct, "format": "mp3", "bitrate": kbps}, "metadata": metadata, "next": action, "playlist_title": title, "on_started": callback("started"), "on_finished": callback("finished"), "on_failed": callback("failed")}
	d := protocol.Action("audio_play", payload)
	return []map[string]any{d}, nil
}
