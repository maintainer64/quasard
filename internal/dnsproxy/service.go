// SPDX-License-Identifier: GPL-3.0-only

// Package dnsproxy provides a bounded, opt-in DNS rewrite, blocking and forwarding service.
package dnsproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maintainer64/quasard/internal/config"
	"github.com/miekg/dns"
)

const drainTimeout = time.Second
const maxTCPQueries = 100

// Shared across transports and policy/listener replacements; never keyed by client.
const maxQueryLogsPerSecond = 100

type queryLogLimiter struct {
	mu    sync.Mutex
	start time.Time
	count int
}

func (l *queryLogLimiter) admit(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.start.IsZero() || now.Sub(l.start) >= time.Second {
		l.start, l.count = now, 0
	}
	if l.count >= maxQueryLogsPerSecond {
		return false
	}
	l.count++
	return true
}

// Service serializes configuration changes. New services do not listen.
// Errors reports unexpected failures of active listeners; it is never closed.
type Service struct {
	mu      sync.Mutex
	current *listener
	closed  bool
	errors  chan error
	logs    queryLogLimiter
}

func New() *Service                     { return &Service{errors: make(chan error, 1)} }
func (s *Service) Errors() <-chan error { return s.errors }
func (s *Service) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return ""
	}
	return s.current.udp.LocalAddr().String()
}

// Apply validates and binds both transports before replacing an active listener.
// A same-address update only publishes a new immutable policy snapshot.
func (s *Service) Apply(c config.DNS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("dnsproxy: service is closed")
	}
	if err := config.ValidateDNS(c); err != nil {
		return err
	}
	if !c.Enabled {
		old := s.current
		s.current = nil
		if old != nil {
			_ = old.stop(context.Background())
		}
		return nil
	}
	p := newPolicy(c)
	if s.current != nil && s.current.bind == c.Listen {
		s.current.policy.Store(p)
		return nil
	}
	udpAddr, err := net.ResolveUDPAddr("udp", c.Listen)
	if err != nil {
		return err
	}
	udp, tcp, err := bindPair(udpAddr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener{bind: c.Listen, udp: udp, tcp: tcp, ctx: ctx, cancel: cancel, errors: s.errors, logs: &s.logs, conns: make(map[net.Conn]struct{}), done: make(chan struct{})}
	l.policy.Store(p)
	l.active.Store(true)
	l.wg.Add(2)
	go l.serveUDP()
	go l.serveTCP()
	go func() { l.wg.Wait(); close(l.done) }()
	old := s.current
	s.current = l
	if old != nil {
		_ = old.stop(context.Background())
	}
	return nil
}

// Port zero chooses from UDP's ephemeral pool, which may overlap an occupied TCP
// port. Retry only ephemeral binds; explicit ports must fail without replacement.
func bindPair(addr *net.UDPAddr) (*net.UDPConn, net.Listener, error) {
	attempts := 1
	if addr.Port == 0 {
		attempts = 16
	}
	for attempt := 0; attempt < attempts; attempt++ {
		udp, err := net.ListenUDP("udp", addr)
		if err != nil {
			return nil, nil, fmt.Errorf("dnsproxy: bind UDP: %w", err)
		}
		tcp, err := net.Listen("tcp", udp.LocalAddr().String())
		if err == nil {
			return udp, tcp, nil
		}
		_ = udp.Close() // Candidate never became active.
		if attempt == attempts-1 {
			return nil, nil, fmt.Errorf("dnsproxy: bind TCP: %w", err)
		}
	}
	panic("unreachable")
}

// Close permanently disables this service, cancels upstream requests and forcibly
// closes client sockets. Drain is bounded even when ctx has no deadline.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	old := s.current
	s.current = nil
	if old != nil {
		return old.stop(ctx)
	}
	return nil
}

type policy struct {
	names                         map[string]struct{}
	blocked                       map[string]struct{}
	logQueries                    bool
	allow                         []netip.Prefix
	upstreams                     []string
	ipv4, ipv6                    net.IP
	ttl                           uint32
	timeout                       time.Duration
	concurrent, connections, rate int
}

func newPolicy(c config.DNS) *policy {
	p := &policy{names: make(map[string]struct{}, len(c.RewriteHosts)), blocked: make(map[string]struct{}, len(c.BlockHosts)), logQueries: c.LogQueries, upstreams: append([]string(nil), c.Upstreams...), ipv4: net.ParseIP(c.AnswerIPv4).To4(), ipv6: net.ParseIP(c.AnswerIPv6), ttl: uint32(c.TTL), timeout: time.Duration(c.TimeoutMS) * time.Millisecond, concurrent: c.MaxConcurrent, connections: c.MaxTCPConnections, rate: c.RateLimit}
	for _, name := range c.RewriteHosts {
		p.names[strings.ToLower(dns.Fqdn(name))] = struct{}{}
	}
	for _, name := range c.BlockHosts {
		p.blocked[strings.ToLower(dns.Fqdn(name))] = struct{}{}
	}
	for _, cidr := range c.AllowCIDRs {
		prefix, _ := netip.ParsePrefix(cidr)
		p.allow = append(p.allow, prefix)
	}
	return p
}
func (p *policy) allowed(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, prefix := range p.allow {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

type listener struct {
	bind    string
	udp     *net.UDPConn
	tcp     net.Listener
	ctx     context.Context
	cancel  context.CancelFunc
	policy  atomic.Pointer[policy]
	active  atomic.Bool
	queries atomic.Int64
	wg      sync.WaitGroup
	done    chan struct{}
	errors  chan error
	logs    *queryLogLimiter
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	tokens  float64
	last    time.Time
}

func (l *listener) stop(ctx context.Context) error {
	l.active.Store(false)
	l.cancel()
	_ = l.udp.Close()
	_ = l.tcp.Close()
	l.mu.Lock()
	for c := range l.conns {
		_ = c.Close()
	}
	l.mu.Unlock()
	timer := time.NewTimer(drainTimeout)
	defer timer.Stop()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("dnsproxy: drain timed out")
	}
}
func (l *listener) failed(transport string, err error) {
	if !l.active.Load() {
		return
	}
	select {
	case l.errors <- fmt.Errorf("dnsproxy: %s listener: %w", transport, err):
	default:
	}
}

// admit is global, not per-source: no attacker-controlled maps or queues.
func (l *listener) admit(p *policy) bool {
	l.mu.Lock()
	now := time.Now()
	if l.last.IsZero() {
		l.tokens = float64(p.rate)
	} else {
		l.tokens += now.Sub(l.last).Seconds() * float64(p.rate)
	}
	l.last = now
	if l.tokens > float64(p.rate) {
		l.tokens = float64(p.rate)
	}
	ok := l.tokens >= 1
	if ok {
		l.tokens--
	}
	l.mu.Unlock()
	if !ok {
		return false
	}
	for {
		n := l.queries.Load()
		if n >= int64(p.concurrent) {
			return false
		}
		if l.queries.CompareAndSwap(n, n+1) {
			return true
		}
	}
}
func (l *listener) serveUDP() {
	defer l.wg.Done()
	// Reject over-sized input instead of allocating attacker-selected buffers.
	buf := make([]byte, 1233)
	for {
		n, addr, err := l.udp.ReadFromUDP(buf)
		if err != nil {
			l.failed("UDP", err)
			return
		}
		p := l.policy.Load()
		if n > 1232 || !p.allowed(addr) || !l.admit(p) {
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.queries.Add(-1)
			req := new(dns.Msg)
			if req.Unpack(packet) != nil {
				return
			}
			reply := l.answer(p, req, "udp")
			if reply == nil {
				return
			}
			size := uint16(512)
			if opt := req.IsEdns0(); opt != nil {
				size = opt.UDPSize()
				if size < 512 {
					size = 512
				}
				if size > 1232 {
					size = 1232
				}
			}
			reply.Truncate(int(size))
			wire, err := reply.Pack()
			if err == nil && len(wire) > int(size) {
				// Truncate deliberately skips TSIG and can retain oversized OPT
				// records. Never let upstream metadata bypass our wire limit.
				minimal := new(dns.Msg)
				minimal.SetReply(req)
				minimal.Rcode = reply.Rcode
				minimal.RecursionAvailable = true
				minimal.Truncated = true
				wire, err = minimal.Pack()
			}
			if err == nil && len(wire) <= int(size) {
				_, _ = l.udp.WriteToUDP(wire, addr)
			}
		}()
	}
}
func (l *listener) serveTCP() {
	defer l.wg.Done()
	for {
		conn, err := l.tcp.Accept()
		if err != nil {
			l.failed("TCP", err)
			return
		}
		p := l.policy.Load()
		l.mu.Lock()
		if !l.active.Load() || !p.allowed(conn.RemoteAddr()) || len(l.conns) >= p.connections {
			l.mu.Unlock()
			_ = conn.Close()
			continue
		}
		l.conns[conn] = struct{}{}
		l.mu.Unlock()
		l.wg.Add(1)
		go l.serveConn(conn)
	}
}
func (l *listener) serveConn(conn net.Conn) {
	defer l.wg.Done()
	defer func() { _ = conn.Close(); l.mu.Lock(); delete(l.conns, conn); l.mu.Unlock() }()
	c := &dns.Conn{Conn: conn}
	for i := 0; i < maxTCPQueries; i++ {
		p := l.policy.Load()
		if !p.allowed(conn.RemoteAddr()) {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(p.timeout))
		req, err := c.ReadMsg()
		if err != nil {
			return
		}
		// Reloads apply to existing connections too, without retaining stale ACLs.
		p = l.policy.Load()
		if !p.allowed(conn.RemoteAddr()) || !l.admit(p) {
			return
		}
		reply := l.answer(p, req, "tcp")
		l.queries.Add(-1)
		if reply == nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(p.timeout))
		if c.WriteMsg(reply) != nil {
			return
		}
	}
}
func (l *listener) answer(p *policy, req *dns.Msg, transport string) (result *dns.Msg) {
	var started time.Time
	if p.logQueries {
		started = time.Now()
	}
	reply := new(dns.Msg)
	reply.SetReply(req)
	reply.RecursionAvailable = true
	if req.Response {
		return nil
	}
	if req.Opcode != dns.OpcodeQuery || len(req.Question) != 1 {
		reply.Rcode = dns.RcodeFormatError
		return reply
	}
	q := req.Question[0]
	if q.Qclass != dns.ClassINET {
		reply.Rcode = dns.RcodeRefused
		return reply
	}
	name := strings.ToLower(dns.Fqdn(q.Name))
	action := "FORWARD"
	if p.logQueries {
		defer func() {
			if action != "" && result != nil && l.logs.admit(time.Now()) {
				slog.Info("DNS query", "action", action, "qname", name,
					"qtype", dns.Type(q.Qtype).String(), "transport", transport,
					"rcode", dns.RcodeToString[result.Rcode], "duration", time.Since(started))
			}
		}()
	}
	if _, ok := p.blocked[name]; ok {
		action = "BLOCK"
		reply.Rcode = dns.RcodeNameError
		return reply
	}
	if q.Qtype == dns.TypeAXFR || q.Qtype == dns.TypeIXFR {
		// Unsupported operations are rejected locally, not forwarded/logged.
		action = ""
		reply.Rcode = dns.RcodeRefused
		return reply
	}
	if _, ok := p.names[name]; ok {
		action = "REWRITE"
		header := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: p.ttl}
		switch {
		case q.Qtype == dns.TypeA && p.ipv4 != nil:
			reply.Answer = []dns.RR{&dns.A{Hdr: header, A: p.ipv4}}
		case q.Qtype == dns.TypeAAAA && p.ipv6 != nil:
			reply.Answer = []dns.RR{&dns.AAAA{Hdr: header, AAAA: p.ipv6}}
		}
		reply.AuthenticatedData = false
		return reply
	}
	ctx, cancel := context.WithTimeout(l.ctx, p.timeout)
	defer cancel()
	for _, upstream := range p.upstreams {
		if ctx.Err() != nil {
			break
		}
		response, err := exchange(ctx, req, upstream, transport, p.timeout)
		if err == nil && validReply(req, response) && transport == "udp" && response.Truncated {
			response, err = exchange(ctx, req, upstream, "tcp", p.timeout)
		}
		if err == nil && validReply(req, response) {
			response.AuthenticatedData = false
			return response
		}
	}
	reply.Rcode = dns.RcodeServerFailure
	return reply
}
func validReply(req, reply *dns.Msg) bool {
	if reply == nil || !reply.Response || reply.Id != req.Id || reply.Opcode != req.Opcode || len(reply.Question) != 1 {
		return false
	}
	a, b := req.Question[0], reply.Question[0]
	return strings.EqualFold(a.Name, b.Name) && a.Qtype == b.Qtype && a.Qclass == b.Qclass
}
func exchange(ctx context.Context, req *dns.Msg, upstream, transport string, timeout time.Duration) (*dns.Msg, error) {
	client := &dns.Client{Net: transport, Timeout: timeout, UDPSize: 1232}
	conn, err := client.DialContext(ctx, upstream)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	reply, _, err := client.ExchangeWithConnContext(ctx, req, conn)
	return reply, err
}
