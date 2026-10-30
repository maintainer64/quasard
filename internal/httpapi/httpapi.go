// SPDX-License-Identifier: GPL-3.0-only

// Package httpapi provides station-compatible HTTP endpoints. WebSocket sessions
// belong to caller and are attached through Hooks.
package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/md5" // Appmetrica protocol uses MD5 identifiers, not authentication.
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maintainer64/quasard/internal/config"
	"github.com/maintainer64/quasard/internal/store"
)

type Hooks struct {
	Store         *store.Store
	WebSocket     http.Handler
	PushWebSocket http.Handler
	Connections   func() map[string]int
	Say           func(context.Context, string, string) error
	Push          func(context.Context, string, map[string]any) error
	// Ready returns nil when model services are ready. No hook means HTTP ready.
	Ready func() error
}

var ErrNotConnected = errors.New("device is not connected")

type Handler struct {
	cfg    config.Config
	store  *store.Store
	hooks  Hooks
	jwtKey []byte
}

// New returns a host-prefix-aware handler. Startup failures produce HTTP 500;
// use NewWithError when startup failure must abort server launch.
func New(cfg config.Config, hooks ...Hooks) http.Handler {
	h, e := NewWithError(cfg, hooks...)
	if e != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 500, map[string]any{"status": "error", "error": "HTTP store initialization failed"})
		})
	}
	return h
}
func NewWithError(cfg config.Config, hooks ...Hooks) (*Handler, error) {
	var hk Hooks
	if len(hooks) > 0 {
		hk = hooks[0]
	}
	s := hk.Store
	if s == nil {
		var e error
		s, e = store.New(cfg)
		if e != nil {
			return nil, e
		}
	}
	key, e := s.JWTKey()
	if e != nil {
		return nil, e
	}
	return &Handler{cfg: cfg, store: s, hooks: hk, jwtKey: key}, nil
}
func (h *Handler) Store() *store.Store { return h.store }

type hostKey struct{}

func Host(r *http.Request) string {
	if host, ok := r.Context().Value(hostKey{}).(string); ok {
		return host
	}
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		return h
	}
	return strings.Split(host, ":")[0]
}

// HostPrefix matches Python proxy behavior, including unlisted .net/.ru/.com hosts.
func HostPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, normalizeHost(r)) })
}
func normalizeHost(r *http.Request) *http.Request {
	host := Host(r)
	parts := strings.SplitN(r.URL.Path, "/", 3)
	if len(parts) > 1 && strings.Contains(parts[1], ".") && (strings.HasSuffix(parts[1], ".net") || strings.HasSuffix(parts[1], ".ru") || strings.HasSuffix(parts[1], ".com")) {
		host = parts[1]
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Path = "/"
		if len(parts) == 3 {
			u.Path += parts[2]
		}
		u.RawPath = ""
		cloned.URL = &u
		r = cloned
	}
	return r.WithContext(context.WithValue(r.Context(), hostKey{}, host))
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = normalizeHost(r)
	p, m := r.URL.Path, r.Method
	if p == "/uni.ws" && h.hooks.WebSocket != nil {
		h.hooks.WebSocket.ServeHTTP(w, r)
		return
	}
	if (p == "/push" || p == "/ws") && h.hooks.PushWebSocket != nil {
		h.hooks.PushWebSocket.ServeHTTP(w, r)
		return
	}
	q := r.URL.Query()
	var body any
	var err error
	status := http.StatusOK
	switch {
	case m == "GET" && p == "/generate_204":
		w.WriteHeader(204)
		return
	case m == "POST" && p == "/token":
		body, err = h.oauthToken(r)
	case m == "GET" && p == "/analytics/startup":
		body = startup(q)
	case m == "POST" && p == "/report":
		w.Header().Set("Content-Type", "application/json; encoding=utf-8")
		body = map[string]any{"status": "accepted"}
	case m == "POST" && (p == "/quasar_metrics/write/json/batch" || p == "/quasar_metrics/write/batch"):
		body = []any{map[string]any{"ResponseStatus": "OK"}}
	case m == "POST" && p == "/register":
		_, err = h.store.UpdateDevice(q.Get("device_id"), map[string]any{"platform": queryOptional(q, "platform"), "firmware": queryOptional(q, "firmware_version"), "activation_code": queryOptional(q, "activation_code"), "last_register": time.Now().Unix()})
		body = map[string]any{"hasCriticalUpdate": false, "status": "ok"}
	case m == "GET" && p == "/check_updates":
		body = map[string]any{"hasUpdate": false}
	case m == "POST" && p == "/push_subscribe":
		var sign string
		sign, err = randomHex(16)
		body = map[string]any{"sign": sign, "status": "ok", "ts": strconv.FormatInt(time.Now().Unix()+28800, 10)}
	case m == "GET" && (p == "/glagol/device_list" || p == "/glagol/family_device_list"):
		body, err = h.store.DeviceList(q.Get("device_id"))
	case m == "POST" && (p == "/tv/api/v4/updates" || p == "/tv/api/v5/updates"):
		body = map[string]any{"apps": []any{}, "rom_update": nil, "ab_update": nil, "sys_recovery_update": nil, "on_boot_update": nil, "batches": []any{}}
	case m == "GET" && p == "/allow_update":
		body = map[string]any{"status": "ok", "is_allowed": false, "allowed": false}
	case m == "GET" && p == "/get_sync_info":
		platform := q.Get("platform")
		if _, ok := q["platform"]; !ok {
			platform = "yandexstation_2"
		}
		body, err = h.store.SyncInfo(q.Get("device_id"), platform)
	case m == "GET" && p == "/get_timezone":
		body = map[string]any{"timezone": h.cfg.Device.Timezone, "status": "ok", "offset_sec": h.cfg.Device.TimezoneOffsetSec}
	case m == "GET" && p == "/get_user_details":
		var captured map[string]any
		captured, err = h.store.CapturedJSON("user_details")
		body = captured
		if len(captured) == 0 {
			body = map[string]any{"status": "ok", "user_details": map[string]any{"display_name": map[string]any{"avatar": map[string]any{"default": "0/0-0", "empty": true}, "name": h.cfg.Device.DisplayName}}}
		}
	case m == "POST" && p == "/update_device_state":
		var raw []byte
		raw, err = io.ReadAll(r.Body)
		if err == nil {
			var state any = map[string]any{}
			if len(raw) > 0 {
				if json.Unmarshal(raw, &state) != nil {
					state = map[string]any{}
				}
			}
			_, err = h.store.UpdateDevice(q.Get("device_id"), map[string]any{"platform": queryOptional(q, "platform"), "networkInfo": state})
		}
		body = map[string]any{"status": "ok"}
	case m == "GET" && p == "/glagol/token":
		body, err = h.glagolToken(q)
	case m == "POST" && (p == "/glagol/check_token" || p == "/check_token" || p == "/glagol/v2.0/check_token"):
		var raw []byte
		raw, err = io.ReadAll(r.Body)
		valid := h.checkJWT(string(raw))
		if p == "/glagol/v2.0/check_token" {
			body = map[string]any{"guest": false, "owner": valid, "status": "ok"}
		} else {
			body = map[string]any{"status": "ok", "valid": valid}
		}
	case m == "GET" && p == "/_status":
		var devices []map[string]any
		devices, err = h.store.KnownDevices()
		body = map[string]any{"devices": devices, "connections": h.connections()}
	case m == "GET" && p == "/_ready":
		body = map[string]any{"status": "ok"}
		if h.hooks.Ready != nil {
			if e := h.hooks.Ready(); e != nil {
				status = 503
				body = map[string]any{"status": "not_ready"}
			}
		}
	case m == "POST" && debugDevice(p, "/_say/") != "":
		body, status, err = h.say(r, debugDevice(p, "/_say/"))
	case m == "POST" && debugDevice(p, "/_push/") != "":
		body, status, err = h.push(r, debugDevice(p, "/_push/"))
	default:
		if !fallbackMethod(m) {
			w.Header().Set("Allow", "GET, POST, PUT, DELETE, PATCH")
			writeJSON(w, 405, map[string]any{"detail": "Method Not Allowed"})
			return
		}
		body = map[string]any{"status": "ok"}
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"status": "error", "error": "internal server error"})
		return
	}
	writeJSON(w, status, body)
}
func fallbackMethod(m string) bool {
	switch m {
	case "GET", "POST", "PUT", "DELETE", "PATCH":
		return true
	}
	return false
}
func queryOptional(q url.Values, k string) any {
	if v, ok := q[k]; ok && len(v) > 0 {
		return v[0]
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func digest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func (h *Handler) oauthToken(r *http.Request) (any, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, e
	}
	values, _ := url.ParseQuery(string(b))
	first := func(k string) string {
		for _, v := range values[k] {
			if v != "" {
				return v
			}
		}
		return ""
	}
	id := first("device_id")
	if name := first("device_name"); name != "" {
		if _, e = h.store.UpdateDevice(id, map[string]any{"name": name}); e != nil {
			return nil, e
		}
	}
	grant := first("grant_type")
	if grant == "authorization_code" {
		seed := first("code") + id
		return map[string]any{"access_token": "y0_" + digest("access"+seed), "expires_in": 31536000, "refresh_token": "1:" + digest("refresh"+seed), "token_type": "bearer"}, nil
	}
	token := first("access_token")
	if grant == "refresh_token" {
		token = "y0_" + digest("access"+first("refresh_token"))
	} else if token == "" {
		token, e = randomHex(24)
		if e != nil {
			return nil, e
		}
		token = "y0_" + token
	}
	return map[string]any{"access_token": token, "expires_in": 31536000, "token_type": "bearer", "uid": h.cfg.Device.UID}, nil
}
func startup(q url.Values) any {
	uid := md5.Sum([]byte(q.Get("deviceid") + q.Get("api_key_128")))
	hash := hex.EncodeToString(uid[:])
	number, _ := strconv.ParseUint(hash[:15], 16, 64)
	hosts := map[string]any{}
	for k, u := range map[string]string{"check_updates": "https://analytics.mobile.yandex.net", "location": "https://rosenberg.appmetrica.yandex.net", "startup": "https://startup.mobile.yandex.net", "report": "https://report.appmetrica.yandex.net", "diagnostic": "https://report.appmetrica.yandex.net"} {
		hosts[k] = map[string]any{"urls": []string{u}}
	}
	return map[string]any{"device_id": map[string]any{"hash": strconv.FormatUint(number, 10)}, "query_hosts": map[string]any{"list": hosts}, "uuid": map[string]any{"value": hash}}
}
func (h *Handler) glagolToken(q url.Values) (any, error) {
	now := time.Now().Unix()
	claims := map[string]any{"plt": q.Get("platform"), "sub": q.Get("device_id"), "iss": "quasar-backend", "aud": "glagol", "iat": now, "exp": now + 86400}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	b, e := json.Marshal(claims)
	if e != nil {
		return nil, e
	}
	input := header + "." + base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, h.jwtKey)
	_, _ = mac.Write([]byte(input))
	return map[string]any{"status": "ok", "token": input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}, nil
}

// checkJWT mirrors station endpoint: verify signature only, not claim expiry.
func (h *Handler) checkJWT(token string) bool {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return false
	}
	mac := hmac.New(sha256.New, h.jwtKey)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	good := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(good), []byte(parts[2]))
}
func debugDevice(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}
func (h *Handler) connections() map[string]int {
	if h.hooks.Connections != nil {
		if c := h.hooks.Connections(); c != nil {
			return c
		}
	}
	return map[string]int{}
}
func debugBody(r *http.Request) (map[string]any, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, e
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		return nil, e
	}
	if v == nil {
		return nil, errors.New("expected JSON object")
	}
	return v, nil
}
func (h *Handler) say(r *http.Request, id string) (any, int, error) {
	body, e := debugBody(r)
	if e != nil {
		return map[string]any{"detail": "expected JSON object"}, 422, nil
	}
	if h.hooks.Say == nil || (h.hooks.Connections != nil && h.connections()[id] == 0) {
		return map[string]any{"status": "error", "error": "станция не подключена"}, 200, nil
	}
	text := ""
	if v, ok := body["text"]; ok {
		text = fmt.Sprint(v)
	}
	e = h.hooks.Say(r.Context(), id, text)
	if errors.Is(e, ErrNotConnected) {
		return map[string]any{"status": "error", "error": "станция не подключена"}, 200, nil
	}
	return map[string]any{"status": "ok"}, 200, e
}
func (h *Handler) push(r *http.Request, id string) (any, int, error) {
	body, e := debugBody(r)
	if e != nil {
		return map[string]any{"detail": "expected JSON object"}, 422, nil
	}
	data := body
	if v, ok := body["data"]; ok {
		var valid bool
		data, valid = v.(map[string]any)
		if !valid {
			return map[string]any{"detail": "data must be an object"}, 422, nil
		}
	}
	envelope := make(map[string]any, len(data))
	for k, v := range data {
		envelope[k] = v
	}
	if _, ok := envelope["operation"]; !ok {
		envelope["operation"] = "update_info"
	}
	if h.hooks.Push == nil {
		return map[string]any{"status": "error", "error": "no push connection (station is not subscribed)"}, 404, nil
	}
	e = h.hooks.Push(r.Context(), id, envelope)
	if errors.Is(e, ErrNotConnected) {
		return map[string]any{"status": "error", "error": "no push connection (station is not subscribed)"}, 404, nil
	}
	return map[string]any{"status": "ok"}, 200, e
}
