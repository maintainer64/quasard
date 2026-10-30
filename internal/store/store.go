// SPDX-License-Identifier: GPL-3.0-only

// Package store persists captured responses and local device state.
package store

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/maintainer64/quasard/internal/config"
)

// Serialize read-modify-write operations, including distinct Store instances.
var diskMu sync.Mutex

type Store struct {
	cfg               config.Config
	captured, devices string
}

func New(cfg config.Config) (*Store, error) {
	s := &Store{cfg: cfg, captured: filepath.Join(cfg.DataPath(), "captured"), devices: filepath.Join(cfg.DataPath(), "devices")}
	if err := os.MkdirAll(s.devices, 0700); err != nil {
		return nil, err
	}
	return s, nil
}
func NewStore(cfg config.Config) (*Store, error) { return New(cfg) }
func (s *Store) deviceFile(id string) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' {
			return r
		}
		return -1
	}, id)
	if safe == "" {
		safe = "unknown"
	}
	return filepath.Join(s.devices, safe+".json")
}
func readJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d map[string]any
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}
func (s *Store) CapturedJSON(name string) (map[string]any, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") {
		return nil, errors.New("invalid captured response name")
	}
	diskMu.Lock()
	defer diskMu.Unlock()
	return readJSON(filepath.Join(s.captured, name+".json"))
}
func (s *Store) SaveCaptured(name string, data map[string]any) error {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid captured response name")
	}
	diskMu.Lock()
	defer diskMu.Unlock()
	return writeJSON(filepath.Join(s.captured, name+".json"), data)
}
func (s *Store) Device(id string) (map[string]any, error) {
	diskMu.Lock()
	defer diskMu.Unlock()
	d, e := readJSON(s.deviceFile(id))
	if d == nil {
		d = map[string]any{}
	}
	return d, e
}
func (s *Store) SaveDevice(id string, data map[string]any) error {
	diskMu.Lock()
	defer diskMu.Unlock()
	return writeJSON(s.deviceFile(id), data)
}
func (s *Store) UpdateDevice(id string, fields map[string]any) (map[string]any, error) {
	diskMu.Lock()
	defer diskMu.Unlock()
	d, e := readJSON(s.deviceFile(id))
	if e != nil {
		return nil, e
	}
	if d == nil {
		d = map[string]any{}
	}
	for k, v := range fields {
		d[k] = v
	}
	if _, ok := d["id"]; !ok {
		d["id"] = id
	}
	if e = writeJSON(s.deviceFile(id), d); e != nil {
		return nil, e
	}
	return d, nil
}
func (s *Store) KnownDevices() ([]map[string]any, error) {
	diskMu.Lock()
	defer diskMu.Unlock()
	paths, err := filepath.Glob(filepath.Join(s.devices, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		d, e := readJSON(p)
		if e != nil {
			return nil, e
		}
		if d != nil {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *Store) GlagolSecurity(id string) (map[string]any, error) {
	diskMu.Lock()
	defer diskMu.Unlock()
	d, e := readJSON(s.deviceFile(id))
	if e != nil {
		return nil, e
	}
	if d == nil {
		d = map[string]any{}
	}
	if g, ok := d["glagol"].(map[string]any); ok {
		return g, nil
	}
	security, e := selfSignedLocalhost()
	if e != nil {
		return nil, e
	}
	g := map[string]any{"security": security}
	d["glagol"] = g
	if _, ok := d["id"]; !ok {
		d["id"] = id
	}
	if e = writeJSON(s.deviceFile(id), d); e != nil {
		return nil, e
	}
	return g, nil
}

// JWTKey returns stable 32-byte local Glagol signing key.
func (s *Store) JWTKey() ([]byte, error) {
	diskMu.Lock()
	defer diskMu.Unlock()
	path := filepath.Join(s.cfg.DataPath(), "glagol_jwt.key")
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) != 32 {
			return nil, errors.New("invalid glagol_jwt.key length")
		}
		if e = os.Chmod(path, 0600); e != nil {
			return nil, e
		}
		return b, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return nil, e
	}
	if e = atomicWrite(path, b); e != nil {
		return nil, e
	}
	return b, nil
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", " ")
	if e != nil {
		return e
	}
	return atomicWrite(path, b)
}
func atomicWrite(path string, b []byte) error {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".quasard-*")
	if e != nil {
		return e
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if e = f.Chmod(0600); e != nil {
		_ = f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func selfSignedLocalhost() (map[string]any, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, e
	}
	now := time.Now().UTC()
	name := pkix.Name{Country: []string{"RU"}, CommonName: "localhost", Organization: []string{"Yandex"}}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: name, Issuer: name, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(3650 * 24 * time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		return nil, e
	}
	private, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		return nil, e
	}
	return map[string]any{"server_certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "server_private_key": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))}, nil
}
func object(parent map[string]any, key string) map[string]any {
	if v, ok := parent[key].(map[string]any); ok {
		return v
	}
	v := map[string]any{}
	parent[key] = v
	return v
}
func (s *Store) SyncInfo(id, platform string) (map[string]any, error) {
	info, e := s.CapturedJSON("get_sync_info")
	if e != nil {
		return nil, e
	}
	if info == nil {
		info = s.defaultSyncInfo()
	}
	g, e := s.GlagolSecurity(id)
	if e != nil {
		return nil, e
	}
	info["glagol"] = g
	info["status"] = "ok"
	cfg := object(info, "config")
	sys := object(cfg, "system_config")
	if s.cfg.Server.PublicUniproxyURL != "" {
		sys["uniProxyUrl"] = s.cfg.Server.PublicUniproxyURL
	}
	vds := object(sys, "voiceDialogSettings")
	vds["useProtoAliceApi"] = false
	delete(vds, "protoAliceApi")
	delete(cfg, "sendVinsRequestProto")
	delete(cfg, "useVinsRequestProto")
	if updater, ok := sys["com.yandex.launcher.updaterapp"].(map[string]any); ok {
		if settings, ok := updater["settings"].(map[string]any); ok {
			for _, k := range []string{"allowFirmwareUpdate", "allowFirmwareUpdateV2"} {
				if _, ok := settings[k]; ok {
					settings[k] = map[string]any{"ota": false}
				}
			}
		}
	}
	dev, e := s.Device(id)
	if e != nil {
		return nil, e
	}
	if name, ok := dev["name"].(string); ok && name != "" {
		object(cfg, "device_config")["name"] = name
	}
	return info, nil
}
func (s *Store) defaultDeviceConfig(name string) map[string]any {
	d := s.cfg.Device
	return map[string]any{"locale": "ru-RU", "name": name, "location": map[string]any{"latitude": d.Latitude, "longitude": d.Longitude}, "dndMode": map[string]any{"enabled": false, "features": map[string]any{"allowIncomingCalls": false}}, "standby": map[string]any{"deepStandbyEnabled": true, "deepStandbyTimeoutMinutes": 240}}
}
func (s *Store) defaultSyncInfo() map[string]any {
	dc := s.defaultDeviceConfig(s.cfg.Device.Name)
	dc["beta"] = false
	dc["tv_beta"] = false
	return map[string]any{
		"alice_pro_subscription": map[string]any{"enabled": false},
		"config": map[string]any{
			"account_config": map[string]any{"spotter": "alisa", "jingle": false, "smartActivation": true, "contentAccess": "without", "childContentAccess": "children", "aliceProactivity": false, "saveHistoryUsage": false},
			"device_config":  dc,
			"system_config":  map[string]any{"uniProxyUrl": "wss://uniproxy.alice.yandex.net/uni.ws", "voiceDialogSettings": map[string]any{"useProtoAliceApi": false}, "onlineSpotterEnabled": false}},
		"subscription": map[string]any{"mode": "transaction"}, "tags": []any{}, "status": "ok"}
}
func (s *Store) deviceEntry(id string, dev map[string]any) (map[string]any, error) {
	name := s.cfg.Device.Name
	if n, ok := dev["name"].(string); ok && n != "" {
		name = n
	}
	g, e := s.GlagolSecurity(id)
	if e != nil {
		return nil, e
	}
	network := dev["networkInfo"]
	if network == nil {
		network = map[string]any{}
	}
	platform, ok := dev["platform"]
	if !ok {
		platform = "yandexstation_2"
	}
	return map[string]any{"activation_code": 0, "activation_region": "RU", "config": s.defaultDeviceConfig(name), "glagol": g, "id": id, "name": name, "networkInfo": network, "platform": platform, "promocode_activated": false, "tags": []any{}}, nil
}
func (s *Store) DeviceList(id string) (map[string]any, error) {
	base, e := s.CapturedJSON("device_list")
	if e != nil {
		return nil, e
	}
	devices := []any{}
	if base != nil {
		if ds, ok := base["devices"].([]any); ok {
			devices = ds
		}
	}
	ids := map[string]bool{}
	for _, v := range devices {
		if d, ok := v.(map[string]any); ok {
			if id, ok := d["id"].(string); ok {
				ids[id] = true
			}
		}
	}
	known, e := s.KnownDevices()
	if e != nil {
		return nil, e
	}
	for _, dev := range known {
		did, _ := dev["id"].(string)
		if did != "" && !ids[did] && dev["platform"] != nil && dev["platform"] != "" {
			entry, e := s.deviceEntry(did, dev)
			if e != nil {
				return nil, e
			}
			devices = append(devices, entry)
			ids[did] = true
		}
	}
	if id != "" && !ids[id] {
		dev, e := s.Device(id)
		if e != nil {
			return nil, e
		}
		entry, e := s.deviceEntry(id, dev)
		if e != nil {
			return nil, e
		}
		devices = append(devices, entry)
	}
	for _, v := range devices {
		if d, ok := v.(map[string]any); ok {
			did, _ := d["id"].(string)
			if _, ok := d["glagol"]; did != "" && ok {
				g, e := s.GlagolSecurity(did)
				if e != nil {
					return nil, e
				}
				d["glagol"] = g
			}
		}
	}
	return map[string]any{"devices": devices, "status": "ok"}, nil
}
