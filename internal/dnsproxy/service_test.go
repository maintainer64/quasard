// SPDX-License-Identifier: GPL-3.0-only
package dnsproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maintainer64/quasard/internal/config"
	"github.com/miekg/dns"
)

func testConfig() config.DNS {
	c := config.Default().DNS
	c.Enabled = true
	c.Listen = "127.0.0.1:0"
	c.Upstreams = []string{"127.0.0.1:1"}
	c.RewriteHosts = []string{"Selected.Example"}
	c.AnswerIPv4 = "192.0.2.10"
	c.TimeoutMS = 200
	c.RateLimit = 10000
	return c
}
func startService(t *testing.T, c config.DNS) *Service {
	t.Helper()
	s := New()
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func query(t *testing.T, addr, transport, name string, typ uint16) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, typ)
	r, _, err := (&dns.Client{Net: transport, Timeout: time.Second}).Exchange(q, addr)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Both transports share one ephemeral loopback port; no public DNS is contacted.
func upstream(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", udp.LocalAddr().String())
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	us := &dns.Server{PacketConn: udp, Handler: handler, UDPSize: 1232}
	ts := &dns.Server{Listener: tcp, Handler: handler}
	ready := make(chan struct{}, 2)
	us.NotifyStartedFunc = func() { ready <- struct{}{} }
	ts.NotifyStartedFunc = func() { ready <- struct{}{} }
	go func() {
		if err := us.ActivateAndServe(); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		if err := ts.ActivateAndServe(); err != nil {
			t.Error(err)
		}
	}()
	<-ready
	<-ready
	t.Cleanup(func() { _ = us.Shutdown(); _ = ts.Shutdown() })
	return udp.LocalAddr().String()
}
func replyA(w dns.ResponseWriter, q *dns.Msg) {
	r := new(dns.Msg)
	r.SetReply(q)
	r.AuthenticatedData = true
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 42}, A: net.ParseIP("198.51.100.1")}}
	_ = w.WriteMsg(r)
}
func TestBlockExactNamesAllINTypes(t *testing.T) {
	var forwarded atomic.Int32
	c := testConfig()
	c.BlockHosts = []string{"Blocked.Example."}
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		forwarded.Add(1)
		replyA(w, q)
	})}
	s := startService(t, c)
	for _, transport := range []string{"udp", "tcp"} {
		for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeHTTPS, dns.TypeSVCB, dns.TypeTXT, dns.TypeMX, dns.TypeRRSIG, dns.TypeANY, dns.TypeAXFR, dns.TypeIXFR, 65280} {
			r := query(t, s.Addr(), transport, "bLoCkEd.eXaMpLe.", typ)
			if r.Rcode != dns.RcodeNameError || len(r.Answer) != 0 || len(r.Ns) != 0 || len(r.Extra) != 0 || r.AuthenticatedData {
				t.Fatalf("%s type %d not blocked: %v", transport, typ, r)
			}
		}
	}
	if forwarded.Load() != 0 {
		t.Fatalf("blocked names reached upstream: %d", forwarded.Load())
	}
	for _, transport := range []string{"udp", "tcp"} {
		for _, name := range []string{"child.blocked.example.", "notblocked.example.", "blocked.example.other."} {
			r := query(t, s.Addr(), transport, name, dns.TypeA)
			if len(r.Answer) != 1 || r.Rcode != dns.RcodeSuccess || r.Answer[0].(*dns.A).A.String() != "198.51.100.1" {
				t.Fatalf("exact block matched %s: %v", name, r)
			}
		}
		r := query(t, s.Addr(), transport, "selected.example.", dns.TypeA)
		if len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != c.AnswerIPv4 {
			t.Fatalf("block changed rewrite: %v", r)
		}
	}
	if forwarded.Load() != 6 {
		t.Fatalf("exactness forward count: %d", forwarded.Load())
	}
}

func TestBlockReloadAndLastGood(t *testing.T) {
	// Exercise same loader/Apply path used by SIGHUP without process-wide signals.
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(key, "ALICE_DNS_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	var forwarded atomic.Int32
	address := upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		forwarded.Add(1)
		replyA(w, q)
	})
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(section string) {
		t.Helper()
		contents := fmt.Sprintf("[dns]\nenabled=true\nlisten='127.0.0.1:0'\nupstreams=['%s']\n%s\n", address, section)
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("block_hosts=['Old.Example.']") // Block-only policy needs no rewrite IP.
	loader, err := config.NewDNSLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	s := startService(t, c)
	addr := s.Addr()
	for _, transport := range []string{"udp", "tcp"} {
		if r := query(t, addr, transport, "old.example.", dns.TypeA); r.Rcode != dns.RcodeNameError {
			t.Fatal(r)
		}
	}
	write("block_hosts=['New.Example.']\nlog_queries=true")
	c, err = loader.Load()
	if err != nil || !c.LogQueries {
		t.Fatalf("reload: %+v, %v", c, err)
	}
	// Keep network test quiet; logging reload has separate coverage below.
	c.LogQueries = false
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if s.Addr() != addr {
		t.Fatal("same-address reload replaced listener")
	}
	// Caller mutations cannot change published block map.
	c.BlockHosts[0] = "mutated.example"
	for _, invalid := range []string{
		"block_hosts=['new.example', 'NEW.EXAMPLE.']",
		"block_hosts=['new.example']\nrewrite_hosts=['NEW.EXAMPLE.']\nanswer_ipv4='192.0.2.10'",
		"block_hosts=['*.example']",
		"block_hosts=42",
	} {
		write(invalid)
		if _, err := loader.Load(); err == nil {
			t.Fatalf("accepted invalid reload: %s", invalid)
		}
		bad := testConfig()
		bad.BlockHosts = []string{"selected.example."}
		if s.Apply(bad) == nil {
			t.Fatal("Apply accepted overlapping rules")
		}
		for _, transport := range []string{"udp", "tcp"} {
			if r := query(t, addr, transport, "new.example.", dns.TypeAAAA); r.Rcode != dns.RcodeNameError {
				t.Fatalf("last-good policy lost: %v", r)
			}
		}
	}
	for _, transport := range []string{"udp", "tcp"} {
		if r := query(t, addr, transport, "old.example.", dns.TypeA); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
			t.Fatalf("old block survived reload: %v", r)
		}
	}
	if forwarded.Load() != 2 {
		t.Fatalf("unexpected upstream calls: %d", forwarded.Load())
	}
}

type queryLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*queryLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *queryLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *queryLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *queryLogCapture) WithGroup(string) slog.Handler      { return h }
func (h *queryLogCapture) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func TestQueryLogsOptInAndMetadata(t *testing.T) {
	h := new(queryLogCapture)
	previous := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(previous)
	c := testConfig()
	c.BlockHosts = []string{"blocked.example"}
	c.Upstreams = []string{upstream(t, replyA)}
	s := startService(t, c)
	checkQueries := func() {
		t.Helper()
		for _, transport := range []string{"udp", "tcp"} {
			for _, name := range []string{"selected.example.", "blocked.example.", "forward.example."} {
				query(t, s.Addr(), transport, name, dns.TypeA)
			}
		}
	}
	checkQueries()
	if got := len(h.snapshot()); got != 0 {
		t.Fatalf("query logs enabled by default: %d", got)
	}
	c.LogQueries = true
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	checkQueries()
	records := h.snapshot()
	if len(records) != 6 {
		t.Fatalf("expected six metadata records, got %d", len(records))
	}
	for i, record := range records {
		attrs := make(map[string]slog.Value)
		record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
		action := []string{"REWRITE", "BLOCK", "FORWARD"}[i%3]
		name := []string{"selected.example.", "blocked.example.", "forward.example."}[i%3]
		rcode := "NOERROR"
		if action == "BLOCK" {
			rcode = "NXDOMAIN"
		}
		transport := []string{"udp", "tcp"}[i/3]
		if record.Message != "DNS query" || record.Level != slog.LevelInfo || len(attrs) != 6 ||
			attrs["action"].String() != action || attrs["qname"].String() != name ||
			attrs["qtype"].String() != "A" || attrs["transport"].String() != transport ||
			attrs["rcode"].String() != rcode || attrs["duration"].Kind() != slog.KindDuration || attrs["duration"].Duration() < 0 {
			t.Fatalf("bad metadata (or unexpected raw data): %v", attrs)
		}
	}
	// Global cap survives same-address reload and listener replacement.
	logs := s.current.logs
	c.LogQueries = false
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	checkQueries()
	if len(h.snapshot()) != 6 || s.current.logs != logs {
		t.Fatal("logging opt-out or shared limiter lost on reload")
	}
	udp, tcp, err := bindPair(&net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	c.Listen = udp.LocalAddr().String()
	_ = udp.Close()
	_ = tcp.Close()
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if s.current.logs != logs {
		t.Fatal("log cap reset on listener replacement")
	}
}

func TestQueryLogGlobalCap(t *testing.T) {
	var l queryLogLimiter
	now := time.Unix(100, 0)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range maxQueryLogsPerSecond * 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.admit(now) {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != maxQueryLogsPerSecond {
		t.Fatalf("global log cap: admitted %d", got)
	}
	if l.admit(now.Add(time.Second - time.Nanosecond)) {
		t.Fatal("log cap refilled early")
	}
	if !l.admit(now.Add(time.Second)) {
		t.Fatal("log cap failed to refill")
	}
}

func TestRewriteAndForward(t *testing.T) {
	var forwarded atomic.Int32
	c := testConfig()
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		forwarded.Add(1)
		if q.Question[0].Name == "missing.example." {
			r := new(dns.Msg)
			r.SetRcode(q, dns.RcodeNameError)
			_ = w.WriteMsg(r)
			return
		}
		replyA(w, q)
	})}
	s := startService(t, c)
	for _, transport := range []string{"udp", "tcp"} {
		r := query(t, s.Addr(), transport, "sElEcTeD.eXaMpLe.", dns.TypeA)
		if len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != c.AnswerIPv4 || r.Answer[0].Header().Ttl != uint32(c.TTL) || r.AuthenticatedData {
			t.Fatalf("rewrite: %v", r)
		}
		for _, typ := range []uint16{dns.TypeAAAA, dns.TypeHTTPS, dns.TypeSVCB, dns.TypeTXT, dns.TypeRRSIG, dns.TypeANY} {
			r = query(t, s.Addr(), transport, "selected.example.", typ)
			if len(r.Answer) != 0 || r.Rcode != dns.RcodeSuccess || r.AuthenticatedData || len(r.Ns) != 0 {
				t.Fatalf("expected NODATA: %v", r)
			}
		}
		r = query(t, s.Addr(), transport, "child.selected.example.", dns.TypeA)
		if len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "198.51.100.1" || r.AuthenticatedData {
			t.Fatalf("forward: %v", r)
		}
		r = query(t, s.Addr(), transport, "missing.example.", dns.TypeA)
		if r.Rcode != dns.RcodeNameError {
			t.Fatalf("NXDOMAIN lost: %v", r)
		}
	}
	if forwarded.Load() != 4 {
		t.Fatalf("forward count %d", forwarded.Load())
	}
	addr := s.Addr()
	c.AnswerIPv6 = "2001:db8::10"
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if s.Addr() != addr {
		t.Fatal("same bind changed listener")
	}
	r := query(t, s.Addr(), "udp", "selected.example.", dns.TypeAAAA)
	if len(r.Answer) != 1 || r.Answer[0].(*dns.AAAA).AAAA.String() != c.AnswerIPv6 {
		t.Fatalf("AAAA: %v", r)
	}
}
func TestProtocolRestrictions(t *testing.T) {
	s := startService(t, testConfig())
	for _, transport := range []string{"udp", "tcp"} {
		for _, typ := range []uint16{dns.TypeAXFR, dns.TypeIXFR} {
			r := query(t, s.Addr(), transport, "selected.example.", typ)
			if r.Rcode != dns.RcodeRefused {
				t.Fatalf("transfer accepted: %v", r)
			}
		}
		cases := []*dns.Msg{{MsgHdr: dns.MsgHdr{Id: 123, Opcode: dns.OpcodeQuery}}, {MsgHdr: dns.MsgHdr{Id: 124, Opcode: dns.OpcodeStatus}}, {MsgHdr: dns.MsgHdr{Id: 125}, Question: []dns.Question{{Name: "selected.example.", Qtype: dns.TypeA, Qclass: dns.ClassCHAOS}}}, {MsgHdr: dns.MsgHdr{Id: 126}, Question: []dns.Question{{Name: "selected.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET}, {Name: "other.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}}}
		for _, q := range cases {
			r, _, err := (&dns.Client{Net: transport, Timeout: time.Second}).Exchange(q, s.Addr())
			if err != nil {
				t.Fatal(err)
			}
			if r.Rcode != dns.RcodeFormatError && r.Rcode != dns.RcodeRefused {
				t.Fatalf("bad request accepted: %v", r)
			}
		}
	}
}
func TestForwardTransportAndFallback(t *testing.T) {
	var udp, tcp atomic.Int32
	c := testConfig()
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		if strings.HasPrefix(w.RemoteAddr().Network(), "udp") {
			udp.Add(1)
			r := new(dns.Msg)
			r.SetReply(q)
			r.Truncated = true
			_ = w.WriteMsg(r)
			return
		}
		tcp.Add(1)
		replyA(w, q)
	})}
	s := startService(t, c)
	for _, transport := range []string{"udp", "tcp"} {
		r := query(t, s.Addr(), transport, "outside.example.", dns.TypeA)
		if r.Truncated || len(r.Answer) != 1 {
			t.Fatalf("fallback: %v", r)
		}
	}
	if udp.Load() != 1 || tcp.Load() != 2 {
		t.Fatalf("wrong transport: UDP%d TCP%d", udp.Load(), tcp.Load())
	}
}
func TestTimeoutAndFailover(t *testing.T) {
	c := testConfig()
	c.Upstreams = []string{"127.0.0.1:1", upstream(t, replyA)}
	s := startService(t, c)
	if r := query(t, s.Addr(), "udp", "outside.example.", dns.TypeA); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("failover: %v", r)
	}
	c.Upstreams = []string{upstream(t, func(dns.ResponseWriter, *dns.Msg) {}), upstream(t, replyA)}
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := query(t, s.Addr(), "udp", "outside.example.", dns.TypeA)
	elapsed := time.Since(start)
	if r.Rcode != dns.RcodeServerFailure || elapsed < 150*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Fatalf("total timeout: %v %v", elapsed, r)
	}
}
func TestUDPReplyBounds(t *testing.T) {
	c := testConfig()
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg)
		r.SetReply(q)
		if strings.HasPrefix(w.RemoteAddr().Network(), "udp") {
			r.Truncated = true
			_ = w.WriteMsg(r)
			return
		}
		for i := 0; i < 40; i++ {
			r.Answer = append(r.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{strings.Repeat("x", 60)}})
		}
		_ = w.WriteMsg(r)
	})}
	s := startService(t, c)
	for _, size := range []uint16{0, 512, 1232, 4096} {
		q := new(dns.Msg)
		q.SetQuestion("large.example.", dns.TypeTXT)
		if size != 0 {
			q.SetEdns0(size, true)
		}
		r, _, err := (&dns.Client{UDPSize: 4096, Timeout: time.Second}).Exchange(q, s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		// Unpack does not retain compression flag; repack as sent by Truncate.
		r.Compress = true
		b, err := r.Pack()
		if err != nil {
			t.Fatal(err)
		}
		max := 512
		if size > 512 {
			max = 1232
		}
		if len(b) > max || !r.Truncated {
			t.Fatalf("size %d: %d bytes TC%v", size, len(b), r.Truncated)
		}
	}
}
func TestReplyValidation(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.", dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	if !validReply(q, r) {
		t.Fatal("valid reply rejected")
	}
	r.Id++
	if validReply(q, r) {
		t.Fatal("wrong ID accepted")
	}
	r.Id = q.Id
	r.Question = []dns.Question{{Name: "other.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}
	if validReply(q, r) {
		t.Fatal("wrong question accepted")
	}
	r.Question = nil
	if validReply(q, r) {
		t.Fatal("empty question accepted")
	}
	c := testConfig()
	bad := upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg)
		r.SetReply(q)
		r.Question[0].Name = "wrong.example."
		_ = w.WriteMsg(r)
	})
	c.Upstreams = []string{bad, upstream(t, replyA)}
	s := startService(t, c)
	if r := query(t, s.Addr(), "tcp", "outside.example.", dns.TypeA); len(r.Answer) != 1 {
		t.Fatalf("invalid reply did not fail over: %v", r)
	}
}
func TestACL(t *testing.T) {
	p := newPolicy(testConfig())
	for _, host := range []string{"127.0.0.2", "::1"} {
		if !p.allowed(&net.TCPAddr{IP: net.ParseIP(host), Port: 123}) {
			t.Fatal("loopback denied")
		}
	}
	for _, host := range []string{"192.0.2.1", "2001:db8::1"} {
		if p.allowed(&net.UDPAddr{IP: net.ParseIP(host), Port: 123}) {
			t.Fatal("non-loopback allowed by default")
		}
	}
	c := testConfig()
	c.AllowCIDRs = []string{"192.0.2.0/24", "2001:db8::/64"}
	p = newPolicy(c)
	if !p.allowed(&net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 123}) || !p.allowed(&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 123}) || p.allowed(&net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 123}) {
		t.Fatal("explicit ACL failed")
	}
	// Exercise both network paths from another numeric loopback source.
	s := startService(t, testConfig())
	for _, transport := range []string{"udp", "tcp"} {
		var local net.Addr = &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}
		if transport == "tcp" {
			local = &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}
		}
		q := new(dns.Msg)
		q.SetQuestion("selected.example.", dns.TypeA)
		_, _, err := (&dns.Client{Net: transport, Timeout: time.Second, Dialer: &net.Dialer{LocalAddr: local}}).Exchange(q, s.Addr())
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestLifecycleTransactionalAndErrors(t *testing.T) {
	s := New()
	if s.Addr() != "" {
		t.Fatal("new service active")
	}
	c := testConfig()
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	old := s.Addr()
	// Occupying only TCP ensures candidate UDP is cleaned up on second bind failure.
	conflict, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.Listen = conflict.Addr().String()
	if s.Apply(c) == nil {
		t.Fatal("TCP conflict accepted")
	}
	if s.Addr() != old {
		t.Fatal("old listener lost")
	}
	probe, err := net.ListenPacket("udp", c.Listen)
	if err != nil {
		t.Fatalf("candidate UDP leaked: %v", err)
	}
	_ = probe.Close()
	_ = conflict.Close()
	// Occupying UDP also leaves old listener live.
	held, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.Listen = held.LocalAddr().String()
	if s.Apply(c) == nil {
		t.Fatal("UDP conflict accepted")
	}
	_ = held.Close()
	query(t, old, "udp", "selected.example.", dns.TypeA)
	next, nextTCP, err := bindPair(&net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	c.Listen = next.LocalAddr().String()
	_ = next.Close()
	_ = nextTCP.Close()
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if s.Addr() == old {
		t.Fatal("rebind unchanged")
	}
	select {
	case err := <-s.Errors():
		t.Fatalf("retired error reported: %v", err)
	default:
	}
	// Inject unexpected failure while listener is active.
	s.mu.Lock()
	_ = s.current.udp.Close()
	s.mu.Unlock()
	select {
	case err := <-s.Errors():
		if !strings.Contains(err.Error(), "UDP") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fatal error missing")
	}
	c.Enabled = false
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if s.Addr() != "" {
		t.Fatal("disable remained active")
	}
	c.Enabled = true
	if err := s.Apply(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Apply(c) == nil {
		t.Fatal("apply after close accepted")
	}
}
func TestBoundedAdmissionAndConnections(t *testing.T) {
	c := testConfig()
	c.RateLimit = 1
	c.MaxConcurrent = 1
	c.MaxTCPConnections = 1
	c.TimeoutMS = 100
	s := startService(t, c)
	query(t, s.Addr(), "udp", "selected.example.", dns.TypeA)
	q := new(dns.Msg)
	q.SetQuestion("selected.example.", dns.TypeA)
	_, _, err := (&dns.Client{Timeout: 50 * time.Millisecond}).Exchange(q, s.Addr())
	if err == nil {
		t.Fatal("UDP rate excess not dropped")
	}
	idle, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	// Wait for accept by trying lock-visible tracked state, with bounded deadline.
	waitUntil(t, func() bool {
		s.mu.Lock()
		l := s.current
		s.mu.Unlock()
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.conns) == 1
	})
	excess, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = excess.Close() }()
	_ = excess.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = excess.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess connection remained open")
	}
	_ = idle.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = idle.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle connection remained open")
	}
}
func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}
func TestConcurrentReloadAndClose(t *testing.T) {
	c := testConfig()
	s := startService(t, c)
	addr := s.Addr()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if i%2 == 0 {
					next := c
					next.AnswerIPv4 = fmt.Sprintf("192.0.2.%d", j+1)
					_ = s.Apply(next)
				} else {
					q := new(dns.Msg)
					q.SetQuestion("selected.example.", dns.TypeA)
					_, _, _ = (&dns.Client{Timeout: 20 * time.Millisecond}).Exchange(q, addr)
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Close(context.Background()) }()
	wg.Wait()
	if s.Addr() != "" {
		t.Fatal("closed service active")
	}
}
func TestCloseCancelsInflight(t *testing.T) {
	started := make(chan struct{}, 1)
	c := testConfig()
	c.TimeoutMS = 10000
	c.Upstreams = []string{upstream(t, func(dns.ResponseWriter, *dns.Msg) {
		select {
		case started <- struct{}{}:
		default:
		}
	})}
	s := startService(t, c)
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	q := new(dns.Msg)
	q.SetQuestion("outside.example.", dns.TypeA)
	if err := (&dns.Conn{Conn: conn}).WriteMsg(q); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream not reached")
	}
	start := time.Now()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("close did not cancel active upstream")
	}
}

func TestActiveQueryBound(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	c := testConfig()
	c.MaxConcurrent = 1
	c.TimeoutMS = 1000
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		started <- struct{}{}
		<-release
		replyA(w, q)
	})}
	s := startService(t, c)
	first, err := net.Dial("udp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	q := new(dns.Msg)
	q.SetQuestion("outside.example.", dns.TypeA)
	if err := (&dns.Conn{Conn: first}).WriteMsg(q); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first query not forwarded")
	}
	// Same global limit gates TCP and UDP, without queued handlers.
	for _, transport := range []string{"udp", "tcp"} {
		_, _, err := (&dns.Client{Net: transport, Timeout: 50 * time.Millisecond}).Exchange(q, s.Addr())
		if err == nil {
			t.Fatal("excess query accepted")
		}
	}
	s.mu.Lock()
	l := s.current
	s.mu.Unlock()
	if l.queries.Load() != 1 {
		t.Fatalf("active count %d", l.queries.Load())
	}
	select {
	case <-started:
		t.Fatal("excess query reached upstream")
	default:
	}
	close(release)
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := (&dns.Conn{Conn: first}).ReadMsg(); err != nil {
		t.Fatal(err)
	}
}

func TestTCPFiniteQueriesAndPolicySnapshot(t *testing.T) {
	c := testConfig()
	s := startService(t, c)
	// Caller-owned slices must not mutate published policy after Apply returns.
	c.RewriteHosts[0] = "mutated.example"
	c.Upstreams[0] = "127.0.0.1:2"
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	client := &dns.Conn{Conn: conn}
	for i := 0; i < maxTCPQueries; i++ {
		q := new(dns.Msg)
		q.SetQuestion("selected.example.", dns.TypeA)
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if err := client.WriteMsg(q); err != nil {
			t.Fatal(err)
		}
		r, err := client.ReadMsg()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Answer) != 1 {
			t.Fatalf("snapshot mutated: %v", r)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.ReadMsg(); err == nil {
		t.Fatal("connection query limit not enforced")
	}
}

func TestOversizedUpstreamMetadataBound(t *testing.T) {
	c := testConfig()
	c.Upstreams = []string{upstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg)
		r.SetReply(q)
		if strings.HasPrefix(w.RemoteAddr().Network(), "udp") {
			r.Truncated = true
		} else {
			r.Extra = []dns.RR{&dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT, Class: 4096}, Option: []dns.EDNS0{&dns.EDNS0_LOCAL{Code: 65001, Data: make([]byte, 2000)}}}}
		}
		_ = w.WriteMsg(r)
	})}
	s := startService(t, c)
	r := query(t, s.Addr(), "udp", "metadata.example.", dns.TypeA)
	r.Compress = true
	wire, err := r.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > 512 || !r.Truncated {
		t.Fatalf("oversized metadata bypass: %d TC%v", len(wire), r.Truncated)
	}
}
