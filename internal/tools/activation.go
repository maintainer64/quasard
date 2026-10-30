// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const deviceID = "SIMSTATION0001"
const firmware = "1.56.1.34.1278844160.20220415.22"
const userAgent = "yandexstation_2/" + firmware

func activation(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	base := f.String("base", "https://127.0.0.1:8443", "local server base URL (no vendor requests)")
	verify := f.Bool("verify", false, "verify TLS certificate using logical Host as SNI")
	timeout := f.Duration("timeout", 15*time.Second, "per-request timeout")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := noArgs(f); e != nil {
		return e
	}
	u, e := url.Parse(*base)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid base URL")
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	var failed bool
	call := func(method, host, path string, form url.Values) (map[string]any, error) {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !*verify, ServerName: host} // explicit simulator opt-in verification
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: *timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(*base, "/")+path, body)
		if e != nil {
			return nil, fmt.Errorf("invalid simulator request")
		}
		req.Host = host
		req.Header.Set("User-Agent", userAgent)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		start := time.Now()
		resp, e := client.Do(req)
		if e != nil {
			return nil, fmt.Errorf("%s %s: request failed", method, host)
		}
		defer func() { _ = resp.Body.Close() }()
		b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if e != nil {
			return nil, fmt.Errorf("response read failed")
		}
		good := resp.StatusCode == 200 || resp.StatusCode == 204
		status := "OK"
		if !good {
			failed = true
			status = "ERR"
		}
		// Never print response bodies, OAuth tokens, query strings or device credentials.
		if _, e := fmt.Fprintf(out, "%s %d %s %s%s %dms\n", status, resp.StatusCode, method, host, strings.Split(path, "?")[0], time.Since(start).Milliseconds()); e != nil {
			return nil, e
		}
		v := map[string]any{}
		_ = json.Unmarshal(b, &v)
		return v, nil
	}
	tok, e := call("POST", "oauth.yandex.ru", "/token", url.Values{"grant_type": {"authorization_code"}, "code": {"abcd1234"}, "client_id": {"x"}, "client_secret": {"y"}, "device_id": {deviceID}, "device_name": {"Яндекс.Станция 2"}})
	if e != nil {
		return e
	}
	tok, e = call("POST", "oauth.yandex.ru", "/token", url.Values{"grant_type": {"x-token"}, "access_token": {text(tok["access_token"])}, "device_id": {deviceID}})
	if e != nil {
		return e
	}
	if _, e = call("POST", "login.yandex.ru", "/info?format=json&oauth_token="+url.QueryEscape(text(tok["access_token"])), nil); e != nil {
		return e
	}
	q := url.Values{"device_id": {deviceID}, "platform": {"yandexstation_2"}, "quasar_platform": {"yandexstation_2"}, "firmware_version": {firmware}, "software_version": {firmware}, "user_id": {"1"}, "packaged": {"true"}}
	p := q.Encode()
	for _, step := range []struct {
		method, host, path string
		form               url.Values
	}{
		{"GET", "quasar.yandex.net", "/get_sync_info?" + p, nil},
		{"POST", "quasar.yandex.net", "/devices/register?" + p, url.Values{"device_id": {deviceID}, "name": {"Яндекс.Станция 2"}, "platform": {"yandexstation_2"}}},
		{"GET", "quasar.yandex.net", "/get_account_config?" + p, nil},
		{"POST", "quasar.yandex.net", "/set_account_config", url.Values{"config": {"{\"alice\":{\"loud\":true}}"}}},
		{"GET", "quasar.yandex.net", "/get_device_config?" + p, nil},
		{"GET", "quasar.yandex.net", "/glagol/device_list?" + p, nil},
		{"GET", "quasar.yandex.net", "/get_user_details", nil},
		{"GET", "quasar.yandex.net", "/check_updates?" + p, nil},
		{"GET", "startup.mobile.yandex.net", "/clids?app_id=ru.yandex.quasar.app&device_id=" + deviceID, nil},
		{"POST", "report.appmetrica.yandex.net", "/report", nil},
		{"GET", "uniproxy.alice.yandex.net", "/speechkit/wins?uuid=" + deviceID + "&format=opus&app=quasar", nil},
	} {
		if _, e = call(step.method, step.host, step.path, step.form); e != nil {
			return e
		}
	}
	if failed {
		return fmt.Errorf("activation simulation had unsuccessful responses")
	}
	return nil
}
