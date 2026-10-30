// SPDX-License-Identifier: GPL-3.0-only
package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/maintainer64/quasard/internal/config"
)

func testAPI(t *testing.T, hooks ...Hooks) (*Handler, config.Config) {
	t.Helper()
	c := config.Default()
	c.Root = t.TempDir()
	h, e := NewWithError(c, hooks...)
	if e != nil {
		t.Fatal(e)
	}
	return h, c
}
func request(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var v map[string]any
	if len(w.Body.Bytes()) > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &v)
	}
	return w, v
}
func TestEndpointsAndFallback(t *testing.T) {
	h, _ := testAPI(t)
	for _, tc := range []struct {
		method, path, contains string
		code                   int
	}{
		{"GET", "/generate_204", "", 204},
		{"GET", "/quasar.yandex.net/check_updates", `"hasUpdate":false`, 200},
		{"GET", "/allow_update", `"is_allowed":false`, 200},
		{"POST", "/tv/api/v4/updates", `"rom_update":null`, 200},
		{"POST", "/tv/api/v5/updates", `"apps":[]`, 200},
		{"GET", "/get_timezone", `"timezone":"Europe/Moscow"`, 200},
		{"GET", "/get_user_details", `"name":"Пользователь"`, 200},
		{"POST", "/quasar_metrics/write/batch", `[{"ResponseStatus":"OK"}]`, 200},
		{"POST", "/quasar_metrics/write/json/batch", `[{"ResponseStatus":"OK"}]`, 200},
		{"POST", "/report", `"status":"accepted"`, 200},
		{"GET", "/unknown.png", `"status":"ok"`, 200},
		{"PATCH", "/unknown", `"status":"ok"`, 200},
		{"PUT", "/unknown", `"status":"ok"`, 200},
		{"DELETE", "/unknown", `"status":"ok"`, 200},
		{"POST", "/check_updates", `"status":"ok"`, 200},
		{"HEAD", "/unknown", "", 405},
		{"OPTIONS", "/unknown", "", 405},
		{"GET", "/_ready", `"status":"ok"`, 200},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w, _ := request(t, h, tc.method, tc.path, "")
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
	w, _ := request(t, h, "POST", "/report", "")
	if w.Header().Get("Content-Type") != "application/json; encoding=utf-8" {
		t.Fatal("report content type")
	}
}
func TestOAuthAndDevices(t *testing.T) {
	h, _ := testAPI(t)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"abc"}, "device_id": {"D"}, "device_name": {"Кухня"}}
	w, v := request(t, h, "POST", "/oauth.yandex.ru/token", form.Encode())
	if w.Code != 200 || v["access_token"] != "y0_"+digest("accessabcD") || v["refresh_token"] != "1:"+digest("refreshabcD") {
		t.Fatalf("OAuth: %+v", v)
	}
	_, v = request(t, h, "POST", "/token", "grant_type=refresh_token&refresh_token=hello")
	if v["access_token"] != "y0_"+digest("accesshello") || v["uid"] != float64(100000001) {
		t.Fatalf("refresh: %+v", v)
	}
	_, v = request(t, h, "POST", "/token", "access_token=passed")
	if v["access_token"] != "passed" {
		t.Fatal("access token passthrough")
	}
	_, v = request(t, h, "POST", "/register?device_id=D&platform=yandexstation_2&firmware_version=1&activation_code=42", "")
	if v["hasCriticalUpdate"] != false {
		t.Fatal(v)
	}
	_, v = request(t, h, "POST", "/update_device_state?device_id=D&platform=yandexstation_2", `{"ip":"127.0.0.1"}`)
	if v["status"] != "ok" {
		t.Fatal(v)
	}
	d, e := h.Store().Device("D")
	if e != nil || d["name"] != "Кухня" || d["firmware"] != "1" || d["networkInfo"].(map[string]any)["ip"] != "127.0.0.1" {
		t.Fatalf("persisted: %+v %v", d, e)
	}
	_, v = request(t, h, "GET", "/get_sync_info?device_id=D", "")
	if v["config"].(map[string]any)["device_config"].(map[string]any)["name"] != "Кухня" {
		t.Fatalf("sync: %+v", v)
	}
	for _, path := range []string{"/glagol/device_list", "/glagol/family_device_list"} {
		_, v = request(t, h, "GET", path, "")
		if len(v["devices"].([]any)) != 1 {
			t.Fatal(v)
		}
	}
	request(t, h, "POST", "/update_device_state?device_id=D", `not json`)
	d, e = h.Store().Device("D")
	if e != nil || len(d["networkInfo"].(map[string]any)) != 0 {
		t.Fatal("invalid state must fallback empty")
	}
	_, v = request(t, h, "GET", "/analytics/startup?deviceid=test&api_key_128=key", "")
	if len(v["uuid"].(map[string]any)["value"].(string)) != 32 {
		t.Fatal(v)
	}
}
func TestJWTStableAndCheckRoutes(t *testing.T) {
	h, c := testAPI(t)
	_, v := request(t, h, "GET", "/glagol/token?device_id=A&platform=P", "")
	token := v["token"].(string)
	parts := strings.Split(token, ".")
	b, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		t.Fatal(e)
	}
	var claims map[string]any
	if e = json.Unmarshal(b, &claims); e != nil {
		t.Fatal(e)
	}
	if claims["iss"] != "quasar-backend" || claims["aud"] != "glagol" || claims["sub"] != "A" || claims["plt"] != "P" || claims["exp"].(float64)-claims["iat"].(float64) != 86400 || len(claims) != 6 {
		t.Fatalf("claims: %+v", claims)
	}
	h2, e := NewWithError(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/check_token", "/glagol/check_token", "/glagol/v2.0/check_token"} {
		_, v = request(t, h2, "POST", path, "  "+token+"\n")
		key := "valid"
		if strings.Contains(path, "v2.0") {
			key = "owner"
		}
		if v[key] != true {
			t.Fatalf("check %s: %+v", path, v)
		}
		_, v = request(t, h2, "POST", path, token+"bad")
		if v[key] != false {
			t.Fatalf("bad check: %+v", v)
		}
	}
	// Original verifier deliberately checks signature only, even expired claims.
	input := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`))
	mac := hmac.New(sha256.New, h.jwtKey)
	_, _ = mac.Write([]byte(input))
	expired := input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	_, v = request(t, h2, "POST", "/check_token", expired)
	if v["valid"] != true {
		t.Fatal("claim validation changed compatibility")
	}
}
func TestHooksAndReadOnlyStatus(t *testing.T) {
	var spoken, pushOp string
	ws := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, Host(r)+" "+r.URL.Path) })
	h, _ := testAPI(t, Hooks{WebSocket: ws, PushWebSocket: ws, Connections: func() map[string]int { return map[string]int{"A": 1} }, Say: func(_ context.Context, id, text string) error { spoken = id + " " + text; return nil }, Push: func(_ context.Context, id string, data map[string]any) error {
		pushOp = id + " " + data["operation"].(string)
		return nil
	}})
	w, _ := request(t, h, "GET", "/uniproxy.alice.yandex.net/uni.ws", "")
	if w.Body.String() != "uniproxy.alice.yandex.net /uni.ws" {
		t.Fatal(w.Body.String())
	}
	for _, p := range []string{"/push.yandex.ru/push", "/push.yandex.ru/ws"} {
		w, _ = request(t, h, "GET", p, "")
		if !strings.HasPrefix(w.Body.String(), "push.yandex.ru ") {
			t.Fatal(w.Body.String())
		}
	}
	_, v := request(t, h, "POST", "/_say/A", `{"text":"привет"}`)
	if v["status"] != "ok" || spoken != "A привет" {
		t.Fatal(v, spoken)
	}
	_, v = request(t, h, "POST", "/_say/absent", `{"text":"x"}`)
	if v["error"] != "станция не подключена" {
		t.Fatal(v)
	}
	_, v = request(t, h, "POST", "/_push/A", `{"data":{"volume":5}}`)
	if v["status"] != "ok" || pushOp != "A update_info" {
		t.Fatal(v, pushOp)
	}
	_, v = request(t, h, "GET", "/_status", "")
	if len(v["devices"].([]any)) != 0 || v["connections"].(map[string]any)["A"] != float64(1) {
		t.Fatal(v)
	}
	devices, e := h.Store().KnownDevices()
	if e != nil || len(devices) != 0 {
		t.Fatal("read-only status created devices")
	}
}
func TestHTTPServer(t *testing.T) {
	h, _ := testAPI(t)
	s := httptest.NewServer(h)
	defer s.Close()
	response, e := s.Client().Get(s.URL + "/quasar.yandex.net/get_timezone")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
}
