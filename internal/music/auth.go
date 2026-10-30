// SPDX-License-Identifier: GPL-3.0-only
package music

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Public OAuth client identity used by the upstream SDK's Android device flow.
// These identify the app, not user credentials. Override when using another OAuth app.
const defaultClientID = "23cabbbdc6cd418abb4b39c32c41195d"
const defaultClientSecret = "53bc75238f0c4d08a118e51fe9203300"

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type,omitempty"`
}

// DeviceAuth is CLI-ready device login: show UserCode/VerificationURL in onCode.
// Callback and polling share ctx; no browser is opened and no login runs automatically.
// Token is atomically saved with mode 0600 before becoming available to this client.
func (m *Music) DeviceAuth(ctx context.Context, onCode func(context.Context, DeviceCode) error) (*OAuthToken, error) {
	if onCode == nil {
		return nil, errors.New("music: device auth requires code callback")
	}
	cid, secret := m.cfg.ClientID, m.cfg.ClientSecret
	if cid == "" {
		cid = defaultClientID
	}
	if secret == "" {
		secret = defaultClientSecret
	}
	device, name := m.cfg.DeviceID, m.cfg.DeviceName
	if device == "" {
		var b [10]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		device = hex.EncodeToString(b[:])
	}
	if name == "" {
		name = "quasard"
	}
	var code DeviceCode
	if err := m.request(ctx, http.MethodPost, strings.TrimRight(m.cfg.OAuthBaseURL, "/")+"/device/code", url.Values{"client_id": {cid}, "device_id": {device}, "device_name": {name}}, nil, "", &code, false); err != nil {
		return nil, err
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURL == "" || code.ExpiresIn <= 0 {
		return nil, errors.New("music: incomplete device code response")
	}
	authCtx, cancel := context.WithTimeout(ctx, time.Duration(code.ExpiresIn)*time.Second)
	defer cancel()
	if err := onCode(authCtx, code); err != nil {
		return nil, err
	}
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		var token OAuthToken
		codeName, err := m.pollToken(authCtx, url.Values{"grant_type": {"device_code"}, "code": {code.DeviceCode}, "client_id": {cid}, "client_secret": {secret}}, &token)
		if err != nil {
			return nil, err
		}
		if codeName == "" {
			if token.AccessToken == "" {
				return nil, errors.New("music: OAuth response has no access token")
			}
			if m.cfg.TokenFile != "" {
				if err := saveToken(m.cfg.TokenFile, token); err != nil {
					return nil, err
				}
			}
			m.mu.Lock()
			m.token = token.AccessToken
			m.uid = ""
			m.mu.Unlock()
			return &token, nil
		}
		switch codeName {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		default:
			return nil, &APIError{Operation: "device auth", Code: codeName, Status: http.StatusBadRequest}
		}
		timer := time.NewTimer(interval)
		select {
		case <-authCtx.Done():
			timer.Stop()
			return nil, authCtx.Err()
		case <-timer.C:
		}
	}
}
func (m *Music) pollToken(ctx context.Context, form url.Values, out *OAuthToken) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.cfg.OAuthBaseURL, "/")+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("music: invalid OAuth endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("music: OAuth request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errors.New("music: reading OAuth response failed")
	}
	var result struct {
		OAuthToken
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return "", errors.New("music: malformed OAuth response")
	}
	if result.Error != "" {
		switch result.Error {
		case "authorization_pending", "slow_down", "access_denied", "expired_token", "invalid_grant", "invalid_client":
			return result.Error, nil
		default:
			return "", errors.New("music: OAuth authorization failed")
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{Operation: "OAuth token", Status: resp.StatusCode, Code: "request_failed"}
	}
	*out = result.OAuthToken
	return "", nil
}
func saveToken(path string, token OAuthToken) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(token)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".music-token-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
