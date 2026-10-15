package dnsserver

import (
	"net"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

const localTTL = 60

// handleLocal answers names that belong to the home network and must never
// be forwarded to the internet: <device>.<domain>, single-label names, *.local
// (mDNS) and reverse lookups for private addresses. ok is false for anything
// else.
func (s *Server) handleLocal(r *dns.Msg, client net.IP, deviceID, domain string) (resp *dns.Msg, ok bool) {
	q := r.Question[0]
	switch {
	case domain == "":
		return nil, false // root queries go upstream
	case domain == "local" || strings.HasSuffix(domain, ".local"):
		return nxdomain(r), true
	case q.Qtype == dns.TypePTR && strings.HasSuffix(domain, ".arpa"):
		ip := reverseIP(domain)
		if ip == nil || !isLocalIP(ip) {
			return nil, false
		}
		name := s.engine.NameForIP(ip)
		if ip.Equal(ownIP(client)) {
			name = "netguard"
		}
		if name == "" || s.domain == "" {
			return nxdomain(r), true
		}
		m := reply(r)
		m.Answer = []dns.RR{&dns.PTR{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: localTTL},
			Ptr: dns.Fqdn(name + "." + s.domain),
		}}
		return m, true
	}

	label := ""
	switch {
	case !strings.Contains(domain, "."):
		label = domain // single label, e.g. "wpad" or "emmas-iphone"
	case s.domain != "" && strings.HasSuffix(domain, "."+s.domain):
		label = strings.TrimSuffix(domain, "."+s.domain)
	case s.domain != "" && domain == s.domain:
		return nxdomain(r), true
	default:
		return nil, false
	}
	var ip net.IP
	if label == "netguard" {
		ip = ownIP(client)
	} else if !strings.Contains(label, ".") {
		ip = s.engine.LookupName(label, deviceID)
	}
	if ip == nil {
		return nxdomain(r), true
	}
	m := reply(r)
	if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
		m.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: localTTL},
			A:   ip.To4(),
		}}
	}
	return m, true // other types: NODATA, so clients fall back to IPv4
}

func reply(r *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = true
	return m
}

func nxdomain(r *dns.Msg) *dns.Msg {
	m := reply(r)
	m.Rcode = dns.RcodeNameError
	return m
}

func isLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}

// reverseIP parses a full reverse-lookup name ("4.3.2.1.in-addr.arpa" or a
// 32-nibble ip6.arpa name). Partial names return nil.
func reverseIP(name string) net.IP {
	if rest, ok := strings.CutSuffix(name, ".in-addr.arpa"); ok {
		parts := strings.Split(rest, ".")
		if len(parts) != 4 {
			return nil
		}
		for i, j := 0, 3; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
		return net.ParseIP(strings.Join(parts, ".")).To4()
	}
	if rest, ok := strings.CutSuffix(name, ".ip6.arpa"); ok {
		nibbles := strings.Split(rest, ".")
		if len(nibbles) != 32 {
			return nil
		}
		ip := make(net.IP, 16)
		for i := 0; i < 32; i++ {
			v, err := strconv.ParseUint(nibbles[31-i], 16, 8)
			if err != nil || len(nibbles[31-i]) != 1 {
				return nil
			}
			if i%2 == 0 {
				ip[i/2] = byte(v << 4)
			} else {
				ip[i/2] |= byte(v)
			}
		}
		return ip
	}
	return nil
}

// ownIP returns this machine's IPv4 address on the interface that reaches
// client (no packets are sent; a UDP "dial" just picks the route).
func ownIP(client net.IP) net.IP {
	if client.IsLoopback() {
		return net.IPv4(127, 0, 0, 1).To4()
	}
	target := "192.0.2.1:9" // any off-link IPv4: picks the default interface
	if v4 := client.To4(); v4 != nil {
		target = net.JoinHostPort(v4.String(), "9")
	}
	conn, err := net.Dial("udp4", target)
	if err != nil {
		return nil
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.To4()
}
