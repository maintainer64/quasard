// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DNS configures the optional exact-name DNS rewrite and blocking service.
type DNS struct {
	Enabled           bool     `toml:"enabled"`
	Listen            string   `toml:"listen"`
	Upstreams         []string `toml:"upstreams"`
	AnswerIPv4        string   `toml:"answer_ipv4"`
	AnswerIPv6        string   `toml:"answer_ipv6"`
	RewriteHosts      []string `toml:"rewrite_hosts"`
	BlockHosts        []string `toml:"block_hosts"`
	LogQueries        bool     `toml:"log_queries"`
	AllowCIDRs        []string `toml:"allow_cidrs"`
	TTL               int      `toml:"ttl"`
	TimeoutMS         int      `toml:"timeout_ms"`
	MaxConcurrent     int      `toml:"max_concurrent"`
	MaxTCPConnections int      `toml:"max_tcp_connections"`
	RateLimit         int      `toml:"rate_limit"`
}

func defaultDNS() DNS {
	return DNS{
		Listen: "127.0.0.1:5353", Upstreams: []string{"1.1.1.1:53", "9.9.9.9:53"},
		RewriteHosts: []string{}, BlockHosts: []string{}, AllowCIDRs: []string{}, TTL: 30, TimeoutMS: 2000,
		MaxConcurrent: 64, MaxTCPConnections: 32, RateLimit: 100,
	}
}

// ConfigPath resolves explicit path, then ALICE_CONFIG, then cwd/config.toml.
// It does not require the file to exist. Capture its result before changing cwd.
func ConfigPath(path string) (string, error) {
	if path == "" {
		path = os.Getenv("ALICE_CONFIG")
	}
	if path == "" {
		path = "config.toml"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return absolute, nil
}

// DNSLoader rereads DNS only from a fixed absolute path and startup environment.
// Each Load starts from fresh defaults; missing files and invalid edits fail.
// Construct once at startup, even if DNS is initially disabled.
type DNSLoader struct {
	path string
	env  map[string]string
}

func NewDNSLoader(path string) (*DNSLoader, error) {
	absolute, err := ConfigPath(path)
	if err != nil {
		return nil, err
	}
	return &DNSLoader{path: absolute, env: captureDNSEnv()}, nil
}

func (l *DNSLoader) Load() (DNS, error) {
	c := struct {
		DNS DNS `toml:"dns"`
	}{DNS: defaultDNS()}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return c.DNS, fmt.Errorf("read config %q: %w", l.path, err)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	// Unknown sections are skipped, never decoded into speech/server field types.
	if err := toml.Unmarshal(data, &c); err != nil {
		return c.DNS, fmt.Errorf("parse DNS config: %w", err)
	}
	if err := applyDNSEnv(&c.DNS, l.env); err != nil {
		return c.DNS, err
	}
	return c.DNS, ValidateDNS(c.DNS)
}

func captureDNSEnv() map[string]string {
	env := make(map[string]string)
	t := reflect.TypeOf(DNS{})
	for i := 0; i < t.NumField(); i++ {
		key := "ALICE_DNS_" + strings.ToUpper(t.Field(i).Tag.Get("toml"))
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

func applyDNSEnv(c *DNS, env map[string]string) error {
	fields := reflect.ValueOf(c).Elem()
	for i := 0; i < fields.NumField(); i++ {
		key := "ALICE_DNS_" + strings.ToUpper(fields.Type().Field(i).Tag.Get("toml"))
		raw, ok := env[key]
		if !ok {
			continue
		}
		field := fields.Field(i)
		switch field.Kind() {
		case reflect.Bool:
			// Unlike legacy env handling, typos must not silently disable DNS.
			if raw != "true" && raw != "false" {
				return fmt.Errorf("%s: expected true or false", key)
			}
			field.SetBool(raw == "true")
		case reflect.String:
			field.SetString(raw)
		case reflect.Int:
			n, err := strconv.ParseInt(raw, 10, field.Type().Bits())
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			field.SetInt(n)
		case reflect.Slice:
			// Require a JSON array of strings, not null or a mixed-type array.
			var values []json.RawMessage
			if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
				return fmt.Errorf("%s: expected JSON array of strings", key)
			}
			stringsOnly := make([]string, len(values))
			for j, value := range values {
				if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &stringsOnly[j]) != nil {
					return fmt.Errorf("%s: expected JSON array of strings", key)
				}
			}
			field.Set(reflect.ValueOf(stringsOnly))
		}
	}
	return nil
}

// ValidateDNS checks DNS policy without DNS lookups, I/O, or mutation.
// Numeric endpoints require ports 1..65535, except loopback bind port 0 for tests.
func ValidateDNS(c DNS) error {
	listen, err := dnsEndpoint(c.Listen, true)
	if err != nil {
		return fmt.Errorf("dns.listen: %w", err)
	}
	if listen.Addr().IsMulticast() {
		return fmt.Errorf("dns.listen: multicast listener is unsupported")
	}
	if !listen.Addr().IsLoopback() && len(c.AllowCIDRs) == 0 {
		return fmt.Errorf("dns.allow_cidrs: non-loopback listener requires explicit client ACL")
	}
	for _, cidr := range c.AllowCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return fmt.Errorf("dns.allow_cidrs: invalid or unrestricted CIDR %q", cidr)
		}
	}
	for _, upstream := range c.Upstreams {
		addr, err := dnsEndpoint(upstream, false)
		if err != nil {
			return fmt.Errorf("dns.upstreams: %w", err)
		}
		if addr.Addr().IsUnspecified() || addr.Addr().IsMulticast() {
			return fmt.Errorf("dns.upstreams: unusable destination %q", upstream)
		}
		if addr.Port() == listen.Port() && (addr.Addr().Unmap() == listen.Addr().Unmap() ||
			(listen.Addr().IsUnspecified() && addr.Addr().IsLoopback())) {
			return fmt.Errorf("dns.upstreams: %q loops back to dns.listen", upstream)
		}
	}
	if c.AnswerIPv4 != "" {
		addr, err := netip.ParseAddr(c.AnswerIPv4)
		if err != nil || !addr.Is4() {
			return fmt.Errorf("dns.answer_ipv4: expected numeric IPv4 address")
		}
	}
	if c.AnswerIPv6 != "" {
		addr, err := netip.ParseAddr(c.AnswerIPv6)
		if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
			return fmt.Errorf("dns.answer_ipv6: expected numeric IPv6 address without zone")
		}
	}
	seen := make(map[string]string, len(c.RewriteHosts)+len(c.BlockHosts))
	for _, list := range []struct {
		name  string
		hosts []string
	}{{"rewrite_hosts", c.RewriteHosts}, {"block_hosts", c.BlockHosts}} {
		for _, host := range list.hosts {
			name := strings.TrimSuffix(host, ".")
			if !validDNSName(name) {
				return fmt.Errorf("dns.%s: invalid exact domain %q", list.name, host)
			}
			name = strings.ToLower(name)
			if previous, ok := seen[name]; ok {
				if previous != list.name {
					return fmt.Errorf("dns.%s: exact domain %q overlaps dns.%s", list.name, host, previous)
				}
				return fmt.Errorf("dns.%s: duplicate exact domain %q", list.name, host)
			}
			seen[name] = list.name
		}
	}
	for _, limit := range []struct {
		name            string
		value, min, max int
	}{
		{"ttl", c.TTL, 1, 3600},
		{"timeout_ms", c.TimeoutMS, 100, 10000},
		{"max_concurrent", c.MaxConcurrent, 1, 1024},
		{"max_tcp_connections", c.MaxTCPConnections, 1, 256},
		{"rate_limit", c.RateLimit, 1, 10000},
	} {
		if limit.value < limit.min || limit.value > limit.max {
			return fmt.Errorf("dns.%s: must be %d..%d", limit.name, limit.min, limit.max)
		}
	}
	if c.Enabled {
		if len(c.RewriteHosts)+len(c.BlockHosts) == 0 || len(c.Upstreams) == 0 {
			return fmt.Errorf("dns: enabled requires rewrite_hosts or block_hosts, and upstreams")
		}
		if len(c.RewriteHosts) > 0 && c.AnswerIPv4 == "" {
			return fmt.Errorf("dns: enabled rewrite_hosts require answer_ipv4")
		}
	}
	return nil
}

func dnsEndpoint(value string, bind bool) (netip.AddrPort, error) {
	addr, err := netip.ParseAddrPort(value)
	if err != nil || addr.Addr().Zone() != "" || (addr.Port() == 0 && (!bind || !addr.Addr().IsLoopback())) {
		return netip.AddrPort{}, fmt.Errorf("invalid numeric IP:port %q", value)
	}
	return addr, nil
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}
