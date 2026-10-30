// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func clearDNSEnv(t *testing.T) {
	t.Helper()
	for key := range captureDNSEnv() {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func writeDNSConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDNSDefaultsAndExample(t *testing.T) {
	clearDNSEnv(t)
	want := DNS{
		Listen: "127.0.0.1:5353", Upstreams: []string{"1.1.1.1:53", "9.9.9.9:53"},
		RewriteHosts: []string{}, BlockHosts: []string{}, AllowCIDRs: []string{}, TTL: 30, TimeoutMS: 2000,
		MaxConcurrent: 64, MaxTCPConnections: 32, RateLimit: 100,
	}
	if got := Default().DNS; !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults: %+v", got)
	}
	if err := ValidateDNS(want); err != nil {
		t.Fatal(err)
	}
	// Default slices must not alias later configs.
	c := Default()
	c.DNS.Upstreams[0] = "127.0.0.2:53"
	if !reflect.DeepEqual(Default().DNS, want) {
		t.Fatal("defaults share slices")
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "config.toml"), "../../config.example.toml"} {
		if filepath.IsAbs(path) {
			writeDNSConfig(t, path, "[server]\nhttp_port=8080\n")
		}
		c, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c.DNS, want) {
			t.Fatalf("%s: %+v", path, c.DNS)
		}
		loader, err := NewDNSLoader(path)
		if err != nil {
			t.Fatal(err)
		}
		dns, err := loader.Load()
		if err != nil || !reflect.DeepEqual(dns, want) {
			t.Fatalf("DNS-only %s: %+v, %v", path, dns, err)
		}
	}
}

func enabledDNS() DNS {
	c := defaultDNS()
	c.Enabled = true
	c.AnswerIPv4 = "192.0.2.10"
	c.RewriteHosts = []string{"Example.COM.", "xn--e1afmkfd.xn--p1ai"}
	return c
}

func TestValidateDNSValid(t *testing.T) {
	for _, mutate := range []func(*DNS){
		func(*DNS) {},
		func(c *DNS) { c.BlockHosts = []string{"Blocked.Example.", "other.example"}; c.LogQueries = true },
		func(c *DNS) { c.RewriteHosts = nil; c.AnswerIPv4 = ""; c.BlockHosts = []string{"blocked.example"} },
		func(c *DNS) { c.Listen = "127.0.0.1:0" },
		func(c *DNS) { c.Listen = "[::1]:0"; c.AnswerIPv6 = "2001:db8::1" },
		func(c *DNS) { c.Listen = "0.0.0.0:53"; c.AllowCIDRs = []string{"192.168.1.0/24"} },
		func(c *DNS) { c.Listen = "[::]:53"; c.AllowCIDRs = []string{"fd00::/8"} },
		func(c *DNS) { c.Enabled = false; c.AnswerIPv4 = ""; c.Upstreams = nil; c.RewriteHosts = nil },
		func(c *DNS) {
			c.TTL, c.TimeoutMS, c.MaxConcurrent, c.MaxTCPConnections, c.RateLimit = 1, 100, 1, 1, 1
		},
		func(c *DNS) {
			c.TTL, c.TimeoutMS, c.MaxConcurrent, c.MaxTCPConnections, c.RateLimit = 3600, 10000, 1024, 256, 10000
		},
	} {
		c := enabledDNS()
		mutate(&c)
		before := c
		before.RewriteHosts = append([]string(nil), c.RewriteHosts...)
		if err := ValidateDNS(c); err != nil {
			t.Fatalf("valid %+v: %v", c, err)
		}
		if !reflect.DeepEqual(c, before) {
			t.Fatal("validation mutated input")
		}
	}
}

func TestValidateDNSInvalid(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DNS)
	}{
		{"bind hostname", func(c *DNS) { c.Listen = "localhost:53" }},
		{"bind no port", func(c *DNS) { c.Listen = "127.0.0.1" }},
		{"bind large port", func(c *DNS) { c.Listen = "127.0.0.1:65536" }},
		{"bind negative port", func(c *DNS) { c.Listen = "127.0.0.1:-1" }},
		{"bind zone", func(c *DNS) { c.Listen = "[fe80::1%en0]:53" }},
		{"bind wildcard ephemeral", func(c *DNS) { c.Listen = "0.0.0.0:0"; c.AllowCIDRs = []string{"10.0.0.0/8"} }},
		{"public no ACL", func(c *DNS) { c.Listen = "192.0.2.1:5353" }},
		{"ACL IPv4 any", func(c *DNS) { c.AllowCIDRs = []string{"0.0.0.0/0"} }},
		{"ACL IPv6 any", func(c *DNS) { c.AllowCIDRs = []string{"::/0"} }},
		{"ACL missing prefix", func(c *DNS) { c.AllowCIDRs = []string{"192.0.2.1"} }},
		{"ACL bad", func(c *DNS) { c.AllowCIDRs = []string{"bad/24"} }},
		{"ACL mapped", func(c *DNS) { c.AllowCIDRs = []string{"::ffff:192.0.2.0/120"} }},
		{"upstream hostname", func(c *DNS) { c.Upstreams = []string{"dns.example:53"} }},
		{"upstream no port", func(c *DNS) { c.Upstreams = []string{"1.1.1.1"} }},
		{"upstream port zero", func(c *DNS) { c.Upstreams = []string{"127.0.0.1:0"} }},
		{"upstream port overflow", func(c *DNS) { c.Upstreams = []string{"1.1.1.1:65536"} }},
		{"upstream unspecified", func(c *DNS) { c.Upstreams = []string{"0.0.0.0:53"} }},
		{"upstream multicast", func(c *DNS) { c.Upstreams = []string{"224.0.0.1:53"} }},
		{"upstream self", func(c *DNS) { c.Upstreams = []string{c.Listen} }},
		{"upstream mapped self", func(c *DNS) { c.Upstreams = []string{"[::ffff:127.0.0.1]:5353"} }},
		{"upstream wildcard self", func(c *DNS) {
			c.Listen = "0.0.0.0:53"
			c.AllowCIDRs = []string{"10.0.0.0/8"}
			c.Upstreams = []string{"127.0.0.1:53"}
		}},
		{"answer v4 hostname", func(c *DNS) { c.AnswerIPv4 = "example.com" }},
		{"answer v4 wrong family", func(c *DNS) { c.AnswerIPv4 = "::1" }},
		{"answer v6 wrong family", func(c *DNS) { c.AnswerIPv6 = "192.0.2.1" }},
		{"answer v6 mapped", func(c *DNS) { c.AnswerIPv6 = "::ffff:192.0.2.1" }},
		{"answer v6 zone", func(c *DNS) { c.AnswerIPv6 = "fe80::1%en0" }},
		{"enabled no answer", func(c *DNS) { c.AnswerIPv4 = "" }},
		{"block duplicate", func(c *DNS) { c.BlockHosts = []string{"Blocked.Example", "blocked.example."} }},
		{"block rewrite overlap", func(c *DNS) { c.BlockHosts = []string{"eXaMpLe.cOm"} }},
		{"disabled block rewrite overlap", func(c *DNS) { c.Enabled = false; c.BlockHosts = []string{"EXAMPLE.COM."} }},
		{"enabled no hosts", func(c *DNS) { c.RewriteHosts = nil }},
		{"enabled no upstream", func(c *DNS) { c.Upstreams = nil }},
		{"disabled invalid answer", func(c *DNS) { c.Enabled = false; c.AnswerIPv4 = "not-an-ip" }},
		{"ttl zero", func(c *DNS) { c.TTL = 0 }},
		{"ttl high", func(c *DNS) { c.TTL = 3601 }},
		{"timeout low", func(c *DNS) { c.TimeoutMS = 99 }},
		{"timeout high", func(c *DNS) { c.TimeoutMS = 10001 }},
		{"concurrency low", func(c *DNS) { c.MaxConcurrent = 0 }},
		{"concurrency high", func(c *DNS) { c.MaxConcurrent = 1025 }},
		{"TCP low", func(c *DNS) { c.MaxTCPConnections = 0 }},
		{"TCP high", func(c *DNS) { c.MaxTCPConnections = 257 }},
		{"rate low", func(c *DNS) { c.RateLimit = 0 }},
		{"rate high", func(c *DNS) { c.RateLimit = 10001 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := enabledDNS()
			tt.mutate(&c)
			if err := ValidateDNS(c); err == nil {
				t.Fatalf("accepted invalid %+v", c)
			}
		})
	}
	for _, hosts := range [][]string{
		{"example.com", "EXAMPLE.COM."}, {"*.example.com"}, {""}, {"."},
		{"example..com"}, {"example.com.."}, {"-bad.example"}, {"bad-.example"},
		{"under_score.example"}, {"bad name.example"}, {" пример.рф"}, {"K.example"},
		{strings.Repeat("a", 64) + ".example"},
		{strings.Repeat("a.", 127) + "aa"},
	} {
		for _, field := range []string{"rewrite_hosts", "block_hosts"} {
			c := enabledDNS()
			if field == "rewrite_hosts" {
				c.RewriteHosts = hosts
			} else {
				c.BlockHosts = hosts
			}
			if err := ValidateDNS(c); err == nil {
				t.Fatalf("accepted invalid %s: %q", field, hosts)
			}
		}
	}
}

func TestDNSLoadTOMLAndStrictEnv(t *testing.T) {
	clearDNSEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeDNSConfig(t, path, "\xef\xbb\xbf[dns]\nenabled=true\nlisten='127.0.0.1:5354'\nupstreams=['8.8.8.8:53']\nanswer_ipv4='192.0.2.1'\nanswer_ipv6='2001:db8::1'\nrewrite_hosts=['Example.COM.']\nblock_hosts=['Blocked.Example.']\nlog_queries=true\nallow_cidrs=['127.0.0.0/8']\nttl=60\ntimeout_ms=500\nmax_concurrent=10\nmax_tcp_connections=4\nrate_limit=55\n")
	t.Setenv("ALICE_DNS_REWRITE_HOSTS", `["env.example.", "second.example"]`)
	t.Setenv("ALICE_DNS_UPSTREAMS", `["9.9.9.9:5353"]`)
	t.Setenv("ALICE_DNS_ALLOW_CIDRS", `[]`)
	t.Setenv("ALICE_DNS_BLOCK_HOSTS", `["env-block.example"]`)
	t.Setenv("ALICE_DNS_LOG_QUERIES", "false")
	t.Setenv("ALICE_DNS_TTL", "120")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DNS{Enabled: true, Listen: "127.0.0.1:5354", Upstreams: []string{"9.9.9.9:5353"}, AnswerIPv4: "192.0.2.1", AnswerIPv6: "2001:db8::1", RewriteHosts: []string{"env.example.", "second.example"}, BlockHosts: []string{"env-block.example"}, AllowCIDRs: []string{}, TTL: 120, TimeoutMS: 500, MaxConcurrent: 10, MaxTCPConnections: 4, RateLimit: 55}
	if !reflect.DeepEqual(c.DNS, want) {
		t.Fatalf("got %+v, want %+v", c.DNS, want)
	}
	loader, err := NewDNSLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loader.Load()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS loader mismatch: %+v, %v", got, err)
	}
}

func TestDNSInvalidEnvironment(t *testing.T) {
	clearDNSEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeDNSConfig(t, path, "")
	for _, tt := range []struct{ key, value string }{
		{"ENABLED", "typo"}, {"ENABLED", "on"}, {"ENABLED", "1"}, {"ENABLED", ""},
		{"REWRITE_HOSTS", "example.com"}, {"REWRITE_HOSTS", `"example.com"`},
		{"REWRITE_HOSTS", `["example.com",3]`}, {"REWRITE_HOSTS", `[null]`},
		{"UPSTREAMS", "null"}, {"ALLOW_CIDRS", `{}`}, {"ALLOW_CIDRS", `[] trailing`},
		{"BLOCK_HOSTS", "blocked.example"}, {"BLOCK_HOSTS", `[null]`},
		{"LOG_QUERIES", "yes"}, {"LOG_QUERIES", ""},
		{"TTL", "abc"}, {"TIMEOUT_MS", "1.5"}, {"MAX_CONCURRENT", "99999999999999999999999"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			key := "ALICE_DNS_" + tt.key
			t.Setenv(key, tt.value)
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load: %v", err)
			}
			loader, err := NewDNSLoader(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("DNSLoader.Load: %v", err)
			}
		})
	}
	for _, value := range []string{"true", "false"} {
		t.Run("valid bool "+value, func(t *testing.T) {
			t.Setenv("ALICE_DNS_ENABLED", value)
			t.Setenv("ALICE_DNS_ANSWER_IPV4", "192.0.2.1")
			t.Setenv("ALICE_DNS_REWRITE_HOSTS", `["example.com"]`)
			c, err := Load(path)
			if err != nil || c.DNS.Enabled != (value == "true") {
				t.Fatalf("strict bool: %+v, %v", c.DNS, err)
			}
		})
	}
}

func TestDNSLoaderIgnoresUnrelatedTypedValues(t *testing.T) {
	clearDNSEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeDNSConfig(t, path, "\xef\xbb\xbf[asr]\nengine=17\nbase_url='legacy'\nvad_threshold='bad'\n[server]\nhttp_port='bad'\n[tts]\ncache=true\n[dns]\nttl=45\n")
	t.Setenv("ALICE_ASR_VAD_THRESHOLD", "bad")
	t.Setenv("ALICE_TTS_CACHE", "true")
	if _, err := Load(path); err == nil {
		t.Fatal("startup unexpectedly accepted invalid speech config")
	}
	loader, err := NewDNSLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loader.Load()
	if err != nil || c.TTL != 45 {
		t.Fatalf("unrelated config affected DNS reload: %+v, %v", c, err)
	}
	writeDNSConfig(t, path, "[dns]\nttl='bad'\n")
	if _, err := loader.Load(); err == nil {
		t.Fatal("accepted wrong DNS field type")
	}
}

func TestConfigPathAndDNSLoaderStableSnapshot(t *testing.T) {
	clearDNSEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ALICE_CONFIG", "env.toml")
	if got, err := ConfigPath("flag.toml"); err != nil || got != filepath.Join(dir, "flag.toml") {
		t.Fatalf("flag precedence: %q, %v", got, err)
	}
	if got, err := ConfigPath(""); err != nil || got != filepath.Join(dir, "env.toml") {
		t.Fatalf("env precedence: %q, %v", got, err)
	}
	t.Setenv("ALICE_CONFIG", "")
	if got, err := ConfigPath(""); err != nil || got != filepath.Join(dir, "config.toml") {
		t.Fatalf("default path: %q, %v", got, err)
	}
	// Absent implicit file is allowed at startup, never on reload.
	if _, err := Load(""); err != nil {
		t.Fatal(err)
	}
	loader, err := NewDNSLoader("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing implicit reload: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	writeDNSConfig(t, path, "[dns]\nttl=40\nrate_limit=33\n")
	t.Setenv("ALICE_DNS_TIMEOUT_MS", "500")
	loader, err = NewDNSLoader("")
	if err != nil {
		t.Fatal(err)
	}
	c, err := loader.Load()
	if err != nil || c.TTL != 40 || c.RateLimit != 33 || c.TimeoutMS != 500 {
		t.Fatalf("first read: %+v, %v", c, err)
	}
	// Changes to process cwd, path selection, and DNS env cannot redirect reload.
	t.Chdir(t.TempDir())
	t.Setenv("ALICE_CONFIG", "other.toml")
	t.Setenv("ALICE_DNS_TIMEOUT_MS", "bad")
	t.Setenv("ALICE_DNS_TTL", "bad")
	writeDNSConfig(t, path, "\xef\xbb\xbf[dns]\nttl=90\n")
	c, err = loader.Load()
	if err != nil || c.TTL != 90 || c.RateLimit != 100 || c.TimeoutMS != 500 {
		t.Fatalf("stable path/env or fresh defaults failed: %+v, %v", c, err)
	}
	writeDNSConfig(t, path, "[dns]\nttl=0\n")
	if _, err := loader.Load(); err == nil {
		t.Fatal("invalid reload accepted")
	}
	writeDNSConfig(t, path, "")
	c, err = loader.Load()
	if err != nil || c.TTL != 30 || c.TimeoutMS != 500 {
		t.Fatalf("stale state reused after error: %+v, %v", c, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing reload must fail: %v", err)
	}
}

func TestStartupValidatesDNS(t *testing.T) {
	clearDNSEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, content := range []string{
		"[dns]\nenabled=true\n",
		"[dns]\nlisten='localhost:5353'\n",
		"[dns]\nttl=0\n",
		"[dns]\nrewrite_hosts=['*.example.com']\n",
		"[dns]\nallow_cidrs=['0.0.0.0/0']\n",
	} {
		writeDNSConfig(t, path, content)
		if _, err := Load(path); err == nil {
			t.Fatalf("startup accepted %q", content)
		}
	}
}
