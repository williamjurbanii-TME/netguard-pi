package dnsserver

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/miekg/dns"

	"netguard/internal/clients"
	"netguard/internal/config"
	"netguard/internal/filter"
	"netguard/internal/querylog"
	"netguard/internal/store"
)

type fakeWriter struct {
	remote net.Addr
	msg    *dns.Msg
}

func (f *fakeWriter) LocalAddr() net.Addr       { return &net.UDPAddr{IP: net.IPv4zero, Port: 53} }
func (f *fakeWriter) RemoteAddr() net.Addr      { return f.remote }
func (f *fakeWriter) WriteMsg(m *dns.Msg) error { f.msg = m; return nil }
func (f *fakeWriter) Write([]byte) (int, error) { return 0, nil }
func (f *fakeWriter) Close() error              { return nil }
func (f *fakeWriter) TsigStatus() error         { return nil }
func (f *fakeWriter) TsigTimersOnly(bool)       {}
func (f *fakeWriter) Hijack()                   {}

func TestLocalNames(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	engine, _ := filter.NewEngine(st, filter.NewListManager(t.TempDir(), nil))
	// Upstream is unreachable on purpose: local answers must not need it.
	s := New(config.DNSConfig{Upstreams: []string{"127.0.0.1:1"}, CacheSize: 10}, "lan",
		engine, clients.NewResolver(nil), nil, querylog.New(st))

	id, _ := engine.EnsureDevice("aa:00:00:00:00:01", "192.168.1.20")
	engine.SetHostname(id, "Emmas-iPhone", "dhcp")
	engine.TouchIP(id, net.ParseIP("192.168.1.20"))

	ask := func(name string, qtype uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qtype)
		w := &fakeWriter{remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000}}
		s.ServeDNS(w, m)
		return w.msg
	}

	if r := ask("emmas-iphone.lan", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "192.168.1.20" {
		t.Errorf("A emmas-iphone.lan = %v", r)
	}
	if r := ask("Emmas-iPhone", dns.TypeA); len(r.Answer) != 1 {
		t.Errorf("single-label lookup failed: %v", r)
	}
	if r := ask("emmas-iphone.lan", dns.TypeAAAA); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 0 {
		t.Errorf("AAAA should be NODATA, got %v", r)
	}
	if r := ask("20.1.168.192.in-addr.arpa", dns.TypePTR); len(r.Answer) != 1 || r.Answer[0].(*dns.PTR).Ptr != "emmas-iphone.lan." {
		t.Errorf("PTR = %v", r)
	}
	for _, name := range []string{"nobody.lan", "wpad", "printer.local", "99.1.168.192.in-addr.arpa"} {
		qt := dns.TypeA
		if name == "99.1.168.192.in-addr.arpa" {
			qt = dns.TypePTR
		}
		if r := ask(name, qt); r.Rcode != dns.RcodeNameError {
			t.Errorf("%s should be NXDOMAIN locally, got %s", name, dns.RcodeToString[r.Rcode])
		}
	}
	if r := ask("netguard.lan", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "127.0.0.1" {
		t.Errorf("netguard.lan from loopback = %v", r)
	}
}

func TestReverseIP(t *testing.T) {
	if ip := reverseIP("4.3.2.1.in-addr.arpa"); ip.String() != "1.2.3.4" {
		t.Errorf("v4 = %v", ip)
	}
	v6 := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.e.f.ip6.arpa"
	if ip := reverseIP(v6); ip.String() != "fe80::1" {
		t.Errorf("v6 = %v", ip)
	}
	if reverseIP("168.192.in-addr.arpa") != nil {
		t.Error("partial reverse names should not parse")
	}
}
