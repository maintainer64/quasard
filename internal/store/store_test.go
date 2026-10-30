// SPDX-License-Identifier: GPL-3.0-only
package store

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/maintainer64/quasard/internal/config"
)

func testStore(t *testing.T) (*Store, config.Config) {
	t.Helper()
	c := config.Default()
	c.Root = t.TempDir()
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	return s, c
}
func TestPersistenceAndSecurity(t *testing.T) {
	s, c := testStore(t)
	if _, e := s.UpdateDevice("station/../A", map[string]any{"name": "Кухня", "platform": "yandexstation_2"}); e != nil {
		t.Fatal(e)
	}
	g, e := s.GlagolSecurity("station/../A")
	if e != nil {
		t.Fatal(e)
	}
	sec := g["security"].(map[string]any)
	cb, _ := pem.Decode([]byte(sec["server_certificate"].(string)))
	if cb == nil {
		t.Fatal("bad certificate PEM")
	}
	cert, e := x509.ParseCertificate(cb.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatal("certificate must use EC P256")
	}
	if cert.Subject.CommonName != "localhost" || cert.Subject.Country[0] != "RU" || cert.Subject.Organization[0] != "Yandex" || cert.SerialNumber.Int64() != 1 {
		t.Fatalf("certificate subject: %+v", cert.Subject)
	}
	kb, _ := pem.Decode([]byte(sec["server_private_key"].(string)))
	if kb == nil || kb.Type != "EC PRIVATE KEY" {
		t.Fatal("wrong private key format")
	}
	key, e := x509.ParseECPrivateKey(kb.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	if !key.PublicKey.Equal(pub) {
		t.Fatal("certificate/key mismatch")
	}
	reopened, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	again, e := reopened.GlagolSecurity("station/../A")
	if e != nil || !reflect.DeepEqual(g, again) {
		t.Fatalf("security changed on reload: %v", e)
	}
	info, e := os.Stat(s.deviceFile("station/../A"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("device mode: %v %v", info, e)
	}
	if filepath.Dir(s.deviceFile("../../")) != s.devices {
		t.Fatal("path traversal")
	}
	a, e := s.JWTKey()
	if e != nil {
		t.Fatal(e)
	}
	b, e := reopened.JWTKey()
	if e != nil || !reflect.DeepEqual(a, b) || len(a) != 32 {
		t.Fatal("JWT signing key unstable")
	}
	info, e = os.Stat(filepath.Join(c.DataPath(), "glagol_jwt.key"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("JWT key permissions")
	}
}
func TestCapturedSyncAndDeviceList(t *testing.T) {
	s, c := testStore(t)
	s.cfg.Server.PublicUniproxyURL = "wss://local/uni.ws"
	captured := map[string]any{"status": "old", "config": map[string]any{"sendVinsRequestProto": true, "useVinsRequestProto": true, "system_config": map[string]any{"voiceDialogSettings": map[string]any{"protoAliceApi": "on", "useProtoAliceApi": true}, "com.yandex.launcher.updaterapp": map[string]any{"settings": map[string]any{"allowFirmwareUpdate": true, "allowFirmwareUpdateV2": true}}}}}
	if e := s.SaveCaptured("get_sync_info", captured); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateDevice("A", map[string]any{"name": "Спальня", "platform": "yandexstation_2"}); e != nil {
		t.Fatal(e)
	}
	result, e := s.SyncInfo("A", "")
	if e != nil {
		t.Fatal(e)
	}
	cfg := result["config"].(map[string]any)
	sys := cfg["system_config"].(map[string]any)
	if sys["uniProxyUrl"] != "wss://local/uni.ws" || cfg["device_config"].(map[string]any)["name"] != "Спальня" || result["status"] != "ok" {
		t.Fatalf("sync: %+v", result)
	}
	if _, ok := cfg["sendVinsRequestProto"]; ok {
		t.Fatal("protobuf enabled")
	}
	vds := sys["voiceDialogSettings"].(map[string]any)
	if vds["useProtoAliceApi"] != false {
		t.Fatal("protobuf voice enabled")
	}
	if _, ok := vds["protoAliceApi"]; ok {
		t.Fatal("old proto option retained")
	}
	upd := sys["com.yandex.launcher.updaterapp"].(map[string]any)["settings"].(map[string]any)
	if upd["allowFirmwareUpdate"].(map[string]any)["ota"] != false {
		t.Fatal("OTA enabled")
	}
	unchanged, e := s.CapturedJSON("get_sync_info")
	if e != nil || unchanged["status"] != "old" {
		t.Fatal("captured data mutated")
	}
	if e := s.SaveCaptured("device_list", map[string]any{"devices": []any{map[string]any{"id": "captured", "glagol": map[string]any{}}}}); e != nil {
		t.Fatal(e)
	}
	list, e := s.DeviceList("B")
	if e != nil {
		t.Fatal(e)
	}
	if len(list["devices"].([]any)) != 3 {
		t.Fatalf("device merge: %+v", list)
	}
	if e := s.SaveCaptured("../escape", map[string]any{}); e == nil {
		t.Fatal("captured path traversal")
	}
	info, e := os.Stat(filepath.Join(c.DataPath(), "captured/get_sync_info.json"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("captured permissions")
	}
}
func TestConcurrentStableIdentity(t *testing.T) {
	s, c := testStore(t)
	other, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan map[string]any, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			current := s
			if i%2 == 0 {
				current = other
			}
			g, e := current.GlagolSecurity("same")
			if e != nil {
				t.Error(e)
				return
			}
			results <- g
		}(i)
	}
	wg.Wait()
	close(results)
	var first map[string]any
	for g := range results {
		if first == nil {
			first = g
		} else if !reflect.DeepEqual(first, g) {
			t.Fatal("concurrent identity rotated")
		}
	}
	if e := os.RemoveAll(s.devices); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateDevice("again", map[string]any{}); e != nil {
		t.Fatal("store did not recreate directory", e)
	}
}
