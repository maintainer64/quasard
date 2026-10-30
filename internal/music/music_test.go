// SPDX-License-Identifier: GPL-3.0-only
package music

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) (*Music, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{Enabled: true, Token: "fixture-token", APIBaseURL: server.URL, OAuthBaseURL: server.URL, HTTPClient: server.Client()}), server
}
func result(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(map[string]any{"result": v}) }
func account(w http.ResponseWriter) {
	result(w, map[string]any{"account": map[string]any{"uid": 1234567890123456789}})
}
func checkAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "OAuth fixture-token" {
		t.Errorf("missing OAuth on %s", r.URL.Path)
	}
}

func TestSearchVariantsAndQueues(t *testing.T) {
	for _, kind := range []string{"track", "artist", "album", "playlist", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				checkAuth(t, r)
				switch r.URL.Path {
				case "/account/status":
					account(w)
				case "/search":
					if r.URL.Query().Get("text") != "рок & jazz" || r.URL.Query().Get("type") != "all" {
						t.Errorf("search query: %v", r.URL.Query())
					}
					best := map[string]any{"id": 7, "title": "best title", "name": "artist", "uid": "42", "kind": 9}
					if kind == "track" {
						best["artists"] = []any{map[string]any{"name": "One"}, map[string]any{"name": "Two"}}
						best["durationMs"] = 123
						best["coverUri"] = "cover/%%"
						best["albums"] = []any{map[string]any{"id": "81"}}
					}
					result(w, map[string]any{"best": map[string]any{"type": kind, "result": best}, "tracks": map[string]any{"results": []any{map[string]any{"id": "99", "title": "fallback"}}}})
				case "/artists/7/tracks":
					if r.URL.Query().Get("page-size") != "50" {
						t.Errorf("artist page size")
					}
					result(w, map[string]any{"tracks": []any{map[string]any{"id": 11}, map[string]any{"id": 12, "available": false}, map[string]any{"id": 13}}})
				case "/albums/7/with-tracks":
					result(w, map[string]any{"title": "album title", "volumes": [][]any{{map[string]any{"id": 21}, map[string]any{"id": 22}}, {map[string]any{"id": 23}}}})
				case "/users/42/playlists/9":
					result(w, map[string]any{"title": "playlist title", "tracks": []any{map[string]any{"id": 31, "albumId": 301}, map[string]any{"id": 32, "track": map[string]any{"id": 32}}}})
				case "/tracks":
					if r.Method != "POST" {
						t.Errorf("track hydration method")
					}
					_ = r.ParseForm()
					if r.Form.Get("track-ids") != "31:301,32" {
						t.Errorf("track hydration: %v", r.Form)
					}
					result(w, []any{map[string]any{"id": 31, "title": "hydrated 1"}, map[string]any{"id": 32, "title": "hydrated 2"}})
				default:
					t.Errorf("unexpected endpoint %s", r.URL)
					w.WriteHeader(404)
				}
			})
			session, err := m.StartSearch(context.Background(), "speaker", "рок & jazz")
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			switch kind {
			case "track":
				want = []string{"7"}
				if session.Queue[0].Artists != "One, Two" || session.Queue[0].AlbumID != "81" || session.Queue[0].Cover != "cover/%%" || session.Queue[0].DurationMS != 123 {
					t.Fatalf("track info: %+v", session.Queue[0])
				}
			case "artist":
				want = []string{"11", "13"}
			case "album":
				want = []string{"21", "22", "23"}
				if session.Title != "album title" {
					t.Errorf("title %s", session.Title)
				}
			case "playlist":
				want = []string{"31", "32"}
			default:
				want = []string{"99"}
			}
			var got []string
			for _, track := range session.Queue {
				got = append(got, track.ID)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("queue %v want %v", got, want)
			}
			if session.Pos != -1 || session.Current() != nil {
				t.Fatal("search must not advance queue")
			}
			session.Queue[0].Title = "mutated"
			if m.Session("speaker").Queue[0].Title == "mutated" {
				t.Fatal("session snapshot aliases queue")
			}
			for _, id := range want {
				track, err := m.Step(context.Background(), "speaker", 1, false)
				if err != nil || track == nil || track.ID != id {
					t.Fatalf("step %v %v", track, err)
				}
			}
			track, err := m.Step(context.Background(), "speaker", 1, false)
			if err != nil || track != nil {
				t.Fatalf("end %v %v", track, err)
			}
			track, err = m.Step(context.Background(), "speaker", -1, false)
			if err != nil || track == nil {
				t.Fatalf("previous %v %v", track, err)
			}
			if m.Session("other") != nil {
				t.Fatal("device queues leaked")
			}
		})
	}
}

func TestMP3SigningAndQuality(t *testing.T) {
	for _, quality := range []string{"high", "low"} {
		t.Run(quality, func(t *testing.T) {
			var server *httptest.Server
			m, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/account/status":
					checkAuth(t, r)
					account(w)
				case "/tracks/123/download-info":
					checkAuth(t, r)
					result(w, []any{
						map[string]any{"codec": "aac", "bitrateInKbps": 320, "downloadInfoUrl": server.URL + "/wrong"},
						map[string]any{"codec": "mp3", "bitrateInKbps": 128, "downloadInfoUrl": server.URL + "/xml/128"},
						map[string]any{"codec": "mp3", "bitrateInKbps": 320, "downloadInfoUrl": server.URL + "/xml/320"},
					})
				case "/xml/128", "/xml/320":
					if r.Header.Get("Authorization") != "" {
						t.Error("OAuth leaked to download-info")
					}
					_, _ = fmt.Fprint(w, `<download-info><host>storage.example.test</host><path>/abc/song.mp3</path><ts>1700000000</ts><s>fixture-salt</s></download-info>`)
				default:
					t.Errorf("unexpected endpoint %s", r.URL)
					w.WriteHeader(404)
				}
			})
			server = s
			m.cfg.Quality = quality
			direct, bitrate, err := m.StreamURL(context.Background(), TrackInfo{ID: "123"})
			if err != nil {
				t.Fatal(err)
			}
			sum := md5.Sum([]byte("XGRlBW9FXlekgbPrRHuSiAabc/song.mp3fixture-salt"))
			want := "https://storage.example.test/get-mp3/" + hex.EncodeToString(sum[:]) + "/1700000000/abc/song.mp3"
			if direct != want {
				t.Fatalf("signed URL %s want %s", direct, want)
			}
			expected := 320
			if quality == "low" {
				expected = 128
			}
			if bitrate != expected {
				t.Errorf("bitrate %d want %d", bitrate, expected)
			}
		})
	}
}

func TestMP3FallbackAndRejectUnsupported(t *testing.T) {
	for _, mode := range []string{"broken-first", "aac-only", "bad-xml"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			m, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/account/status":
					account(w)
				case "/tracks/1/download-info":
					list := []any{map[string]any{"codec": "mp3", "bitrateInKbps": 320, "downloadInfoUrl": server.URL + "/bad"}, map[string]any{"codec": "mp3", "bitrateInKbps": 128, "downloadInfoUrl": server.URL + "/good"}}
					if mode == "aac-only" {
						list = []any{map[string]any{"codec": "aac", "bitrateInKbps": 320, "downloadInfoUrl": server.URL + "/good"}}
					}
					if mode == "bad-xml" {
						list = list[:1]
					}
					result(w, list)
				case "/bad":
					_, _ = fmt.Fprint(w, `<download-info><host>evil.test/path</host></download-info>`)
				case "/good":
					_, _ = fmt.Fprint(w, `<download-info><host>cdn.test</host><path>/a</path><ts>1</ts><s>s</s></download-info>`)
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			server = s
			_, bitrate, err := m.StreamURL(context.Background(), TrackInfo{ID: "1"})
			if mode == "broken-first" {
				if err != nil || bitrate != 128 {
					t.Fatalf("fallback: %d %v", bitrate, err)
				}
			} else if err == nil {
				t.Fatal("invalid MP3 data accepted")
			}
		})
	}
}

type feedbackFixture struct {
	Type      string   `json:"type"`
	TrackID   string   `json:"trackId"`
	From      string   `json:"from"`
	Timestamp float64  `json:"timestamp"`
	Played    *float64 `json:"totalPlayedSeconds"`
	Batch     string
}

func TestRotorFeedbackAndCallbacks(t *testing.T) {
	var mu sync.Mutex
	var events []feedbackFixture
	batches := 0
	var server *httptest.Server
	m, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xml" {
			checkAuth(t, r)
		}
		switch r.URL.Path {
		case "/account/status":
			account(w)
		case "/rotor/station/user:onyourwave/feedback":
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
				t.Error("feedback transport")
			}
			var event feedbackFixture
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Error(err)
			}
			event.Batch = r.URL.Query().Get("batch-id")
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			result(w, "ok")
		case "/rotor/station/user:onyourwave/tracks":
			batches++
			if batches == 1 {
				if r.URL.Query().Get("queue") != "" {
					t.Error("initial queue")
				}
				result(w, map[string]any{"batchId": "batch-1", "sequence": []any{map[string]any{"track": map[string]any{"id": 1, "title": "one"}}, map[string]any{"track": map[string]any{"id": 2, "available": false}}}})
			} else {
				expected := "1"
				if batches > 2 {
					expected = "3"
				}
				if r.URL.Query().Get("queue") != expected {
					t.Errorf("refill queue: %v", r.URL.Query())
				}
				result(w, map[string]any{"batchId": "batch-2", "sequence": []any{map[string]any{"track": map[string]any{"id": "3", "title": "three"}}}})
			}
		case "/users/1234567890123456789/likes/tracks/add-multiple", "/users/1234567890123456789/likes/tracks/remove", "/users/1234567890123456789/dislikes/tracks/add-multiple":
			_ = r.ParseForm()
			if r.Form.Get("track-ids") != "1" {
				t.Errorf("rating form: %v", r.Form)
			}
			result(w, map[string]any{"revision": 1})
		case "/tracks/1/download-info":
			result(w, []any{map[string]any{"codec": "mp3", "bitrateInKbps": 320, "downloadInfoUrl": server.URL + "/xml"}})
		case "/xml":
			_, _ = fmt.Fprint(w, `<download-info><host>cdn.test</host><path>/a.mp3</path><ts>1</ts><s>s</s></download-info>`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	server = s
	session, err := m.StartWave(context.Background(), "speaker")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Queue) != 1 || session.BatchID != "batch-1" {
		t.Fatalf("initial radio %+v", session)
	}
	first, err := m.Step(context.Background(), "speaker", 1, false)
	if err != nil || first == nil || first.ID != "1" {
		t.Fatalf("first: %v %v", first, err)
	}
	if m.TrackFailed("speaker") != 1 || m.TrackFailed("speaker") != 2 {
		t.Error("failure streak")
	}
	if err := m.TrackStarted(context.Background(), "speaker"); err != nil {
		t.Fatal(err)
	}
	if m.Session("speaker").FailStreak != 0 {
		t.Error("started must reset failures")
	}
	if _, err := m.Rate(context.Background(), "speaker", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rate(context.Background(), "speaker", false); err != nil {
		t.Fatal(err)
	}
	actions, err := m.PlayDirectives(context.Background(), "speaker", "request-1", *first)
	if err != nil {
		t.Fatal(err)
	}
	action := actions[0]
	if action["name"] != "audio_play" || action["type"] != "client_action" {
		t.Fatalf("action %v", action)
	}
	payload := action["payload"].(map[string]any)
	next := payload["next"].(map[string]any)
	if next["name"] != GetNext {
		t.Error("next action")
	}
	nextPayload := next["payload"].(map[string]any)
	if nextPayload["music_request_id"] != "request-1" || nextPayload["entity_id"] == "" {
		t.Error("next request state")
	}
	for _, kind := range []string{"started", "finished", "failed"} {
		cb := payload["on_"+kind].(map[string]any)
		if cb["ignore_answer"] != true {
			t.Error("callback must ignore answer")
		}
		cmd := cb["command"].(map[string]any)
		if cmd["name"] != CallbackPrefix+kind {
			t.Error("callback name")
		}
		p := cmd["payload"].(map[string]any)
		if p["device"] != "speaker" || p["track_id"] != "1" {
			t.Errorf("callback state %v", p)
		}
	}
	second, err := m.Step(context.Background(), "speaker", 1, true)
	if err != nil || second == nil || second.ID != "3" {
		t.Fatalf("refill: %v %v", second, err)
	}
	if m.Session("speaker").BatchID != "batch-2" {
		t.Error("new batch")
	}
	if _, err := m.Step(context.Background(), "speaker", 1, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
		if e.Timestamp <= 0 {
			t.Error("missing timestamp")
		}
	}
	if !reflect.DeepEqual(types, []string{"radioStarted", "trackStarted", "like", "dislike", "skip", "trackFinished"}) {
		t.Fatalf("feedback events %v", types)
	}
	if events[0].From != "mobile-radio-user-onyourwave" || events[0].Batch != "" {
		t.Error("radioStarted source")
	}
	if events[1].TrackID != "1" || events[1].Batch != "batch-1" {
		t.Error("started batch")
	}
	if events[4].Played == nil || *events[4].Played < 0 || events[4].Batch != "batch-1" {
		t.Errorf("skip feedback %+v", events[4])
	}
	if events[5].TrackID != "3" || events[5].Batch != "batch-2" || events[5].Played == nil {
		t.Errorf("finished feedback %+v", events[5])
	}
}

func TestDeviceAuthPrivateTokenAndReload(t *testing.T) {
	var calls int
	m, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("OAuth auth header on device flow")
		}
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/device/code":
			if r.Form.Get("client_id") != defaultClientID || r.Form.Get("device_id") == "" {
				t.Errorf("device form %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "device-code", UserCode: "ABCD", VerificationURL: "https://oauth.example.test/device", ExpiresIn: 60, Interval: 1})
		case "/token":
			if r.Form.Get("grant_type") != "device_code" || r.Form.Get("code") != "device-code" || r.Form.Get("client_secret") != defaultClientSecret {
				t.Errorf("token form %v", r.Form)
			}
			calls++
			if calls == 1 {
				w.WriteHeader(400)
				_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
			} else {
				_ = json.NewEncoder(w).Encode(OAuthToken{AccessToken: "fixture-new-token", RefreshToken: "fixture-refresh", ExpiresIn: 3600})
			}
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	m.cfg.Token = ""
	m.cfg.TokenFile = filepath.Join(t.TempDir(), "private", "music_token.json")
	// A separate already-created client must discover token without restart.
	other := New(m.cfg)
	if other.Enabled() {
		t.Fatal("enabled before login")
	}
	callback := false
	token, err := m.DeviceAuth(context.Background(), func(ctx context.Context, c DeviceCode) error {
		callback = true
		if c.UserCode != "ABCD" || c.VerificationURL != "https://oauth.example.test/device" {
			t.Errorf("code %+v", c)
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !callback || token.AccessToken != "fixture-new-token" || calls != 2 {
		t.Fatalf("login %v %v %d", callback, token, calls)
	}
	stat, err := os.Stat(m.cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Errorf("token permissions %o", stat.Mode().Perm())
	}
	data, err := os.ReadFile(m.cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	var disk OAuthToken
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.RefreshToken != "fixture-refresh" {
		t.Error("refresh token not saved")
	}
	if !other.Enabled() {
		t.Error("new token not detected")
	}
}
func TestDeviceAuthCancelAndDenial(t *testing.T) {
	for _, mode := range []string{"cancel", "denied", "slow", "callback"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/device/code":
					_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "d", UserCode: "u", VerificationURL: "https://example.test", ExpiresIn: 60, Interval: 1})
				case "/token":
					w.WriteHeader(400)
					code := "authorization_pending"
					if mode == "denied" {
						code = "access_denied"
					}
					if mode == "slow" {
						code = "slow_down"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "do not expose fixture-secret"})
				default:
					w.WriteHeader(404)
				}
			})
			path := filepath.Join(t.TempDir(), "token.json")
			m.cfg.TokenFile = path
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			callbackErr := errors.New("callback stopped")
			_, err := m.DeviceAuth(ctx, func(context.Context, DeviceCode) error {
				if mode == "callback" {
					return callbackErr
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("sensitive OAuth response exposed")
			}
			if mode == "callback" && !errors.Is(err, callbackErr) {
				t.Fatal(err)
			}
			if mode == "cancel" || mode == "slow" {
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
					t.Fatalf("cancellation %v", err)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("token saved after failed auth")
			}
		})
	}
}

func TestMissingTokenErrorsAndContext(t *testing.T) {
	m := New(Config{Enabled: true, TokenFile: filepath.Join(t.TempDir(), "missing.json")})
	if m.Enabled() {
		t.Fatal("enabled without token")
	}
	if _, err := m.StartSearch(context.Background(), "d", "q"); err == nil {
		t.Fatal("expected missing token error")
	}
	m, _ = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account/status" {
			account(w)
			return
		}
		result(w, map[string]any{"tracks": map[string]any{"results": []any{}}})
	})
	if _, err := m.StartSearch(context.Background(), "d", "nothing"); err == nil {
		t.Fatal("expected empty search error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.StartSearch(ctx, "d", "query"); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error %v", err)
	}
}
