// Package dnsserver is the filtering, caching DNS forwarder.
package dnsserver

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"netguard/internal/clients"
	"netguard/internal/config"
	"netguard/internal/filter"
	"netguard/internal/names"
	"netguard/internal/querylog"
	"netguard/internal/store"
)

type Server struct {
	cfg      config.DNSConfig
	domain   string // local domain, e.g. "lan"; "" disables <name>.<domain>
	engine   *filter.Engine
	resolver *clients.Resolver
	names    *names.Discoverer // nil when name discovery is off
	qlog     *querylog.Logger
	cache    *Cache
	udp, tcp *dns.Client
	servers  []*dns.Server
}

func New(cfg config.DNSConfig, localDomain string, engine *filter.Engine, resolver *clients.Resolver,
	disc *names.Discoverer, qlog *querylog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		domain:   localDomain,
		engine:   engine,
		resolver: resolver,
		names:    disc,
		qlog:     qlog,
		cache:    NewCache(cfg.CacheSize),
		udp:      &dns.Client{Net: "udp", Timeout: 3 * time.Second, UDPSize: dns.DefaultMsgSize},
		tcp:      &dns.Client{Net: "tcp", Timeout: 5 * time.Second},
	}
}

// Start binds UDP and TCP listeners and returns once both are up (or one failed).
func (s *Server) Start() error {
	for _, network := range []string{"udp", "tcp"} {
		srv := &dns.Server{Addr: s.cfg.Listen, Net: network, Handler: s}
		started := make(chan struct{})
		errc := make(chan error, 1)
		srv.NotifyStartedFunc = func() { close(started) }
		go func() { errc <- srv.ListenAndServe() }()
		select {
		case <-started:
		case err := <-errc:
			s.Shutdown()
			return fmt.Errorf("dns %s listen on %s: %w", network, s.cfg.Listen, err)
		}
		s.servers = append(s.servers, srv)
	}
	log.Printf("dns: listening on %s (udp+tcp), upstreams %v", s.cfg.Listen, s.cfg.Upstreams)
	return nil
}

func (s *Server) Shutdown() {
	for _, srv := range s.servers {
		_ = srv.Shutdown()
	}
}

func clientIP(addr net.Addr) net.IP {
	var ip net.IP
	switch a := addr.(type) {
	case *net.UDPAddr:
		ip = a.IP
	case *net.TCPAddr:
		ip = a.IP
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	start := time.Now()
	if len(r.Question) != 1 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}
	q := r.Question[0]
	domain := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	ip := clientIP(w.RemoteAddr())
	ident := s.resolver.Identify(ip)
	devID, _ := s.engine.EnsureDevice(ident, ip.String())
	s.engine.TouchIP(devID, ip)
	if s.names != nil {
		if v4 := s.engine.IPv4For(devID); v4 != nil {
			hasName := false
			if d := s.engine.Device(devID); d != nil {
				hasName = d.Hostname != ""
			}
			s.names.Notify(devID, v4, hasName)
		}
	}

	entry := store.QueryEntry{
		Identifier: ident, TS: start.Unix(), DeviceID: devID, ClientIP: ip.String(), Domain: domain,
		QType: dns.TypeToString[q.Qtype],
	}
	if resp, ok := s.handleLocal(r, ip, devID, domain); ok {
		entry.Status = "local"
		s.finish(w, r, resp, entry, start)
		return
	}

	dec := s.engine.Check(devID, domain)
	entry.Reason = dec.Reason
	var resp *dns.Msg
	var err error
	switch dec.Action {
	case filter.Block:
		resp, entry.Status = s.blockedReply(r, false), "blocked"
	case filter.NXDomain:
		resp, entry.Status = s.blockedReply(r, true), "blocked"
	case filter.Rewrite:
		resp, err = s.rewrite(r, dec.Target)
		entry.Status = "safesearch"
	default:
		var cached bool
		resp, cached, err = s.resolve(r)
		entry.Status = "allowed"
		if cached {
			entry.Status = "cached"
		}
	}
	if err != nil || resp == nil {
		resp = new(dns.Msg)
		resp.SetRcode(r, dns.RcodeServerFailure)
		entry.Status = "error"
		if err != nil {
			entry.Reason = err.Error()
		}
	}
	s.finish(w, r, resp, entry, start)
}

func (s *Server) finish(w dns.ResponseWriter, r, resp *dns.Msg, entry store.QueryEntry, start time.Time) {
	resp.Id = r.Id
	if w.LocalAddr().Network() == "udp" {
		size := dns.MinMsgSize
		if opt := r.IsEdns0(); opt != nil {
			size = int(opt.UDPSize())
		}
		resp.Truncate(size)
	}
	_ = w.WriteMsg(resp)
	entry.Ms = time.Since(start).Milliseconds()
	s.qlog.Log(entry)
}

// blockedReply answers 0.0.0.0 / :: for A/AAAA (like Pi-hole), NODATA for
// other types, or NXDOMAIN when nx is set.
func (s *Server) blockedReply(r *dns.Msg, nx bool) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	if nx {
		m.Rcode = dns.RcodeNameError
		return m
	}
	q := r.Question[0]
	hdr := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: s.cfg.BlockTTL}
	switch q.Qtype {
	case dns.TypeA:
		m.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.IPv4zero.To4()}}
	case dns.TypeAAAA:
		m.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: net.IPv6zero}}
	}
	return m
}

// rewrite answers with a CNAME to target plus target's records (SafeSearch).
func (s *Server) rewrite(r *dns.Msg, target string) (*dns.Msg, error) {
	q := r.Question[0]
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	m.Answer = []dns.RR{&dns.CNAME{
		Hdr:    dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300},
		Target: dns.Fqdn(target),
	}}
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
		return m, nil
	}
	sub := new(dns.Msg)
	sub.SetQuestion(dns.Fqdn(target), q.Qtype)
	up, _, err := s.resolve(sub)
	if err != nil {
		return nil, err
	}
	m.Answer = append(m.Answer, up.Answer...)
	return m, nil
}

// resolve answers from cache or forwards to the upstreams in order.
func (s *Server) resolve(r *dns.Msg) (*dns.Msg, bool, error) {
	q := r.Question[0]
	if m := s.cache.Get(q); m != nil {
		return m, true, nil
	}
	req := r.Copy()
	req.RecursionDesired = true
	var lastErr error
	for _, up := range s.cfg.Upstreams {
		resp, _, err := s.udp.Exchange(req, up)
		if err == nil && resp.Truncated {
			resp, _, err = s.tcp.Exchange(req, up)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Rcode == dns.RcodeServerFailure || resp.Rcode == dns.RcodeRefused {
			lastErr = fmt.Errorf("%s from %s", dns.RcodeToString[resp.Rcode], up)
			continue
		}
		s.cache.Set(q, resp)
		return resp, false, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstreams configured")
	}
	return nil, false, lastErr
}
