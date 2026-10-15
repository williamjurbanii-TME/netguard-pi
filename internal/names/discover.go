package names

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	retryMissing = 15 * time.Minute // retry devices that didn't answer
	refreshFound = 12 * time.Hour   // re-check names we already have
)

// Sink receives discovered names (implemented by filter.Engine).
type Sink interface {
	SetHostname(deviceID, name, source string)
}

type job struct {
	deviceID string
	ip       net.IP
}

// Discoverer asks devices for their own names: Apple devices (and most
// Macs/Linux boxes) answer mDNS reverse lookups, Windows PCs answer NetBIOS.
type Discoverer struct {
	sink    Sink
	timeout time.Duration
	queue   chan job

	mu    sync.Mutex
	next  map[string]time.Time // deviceID -> earliest next attempt
	known map[string]bool      // deviceID -> already has a name
}

func NewDiscoverer(sink Sink) *Discoverer {
	return &Discoverer{
		sink:    sink,
		timeout: 1500 * time.Millisecond,
		queue:   make(chan job, 256),
		next:    map[string]time.Time{},
		known:   map[string]bool{},
	}
}

// Notify is called for every query; it is cheap and never blocks.
// hasName says whether the device already has a hostname (e.g. from DHCP).
func (d *Discoverer) Notify(deviceID string, ip net.IP, hasName bool) {
	ip = ip.To4()
	if ip == nil || !ip.IsPrivate() {
		return
	}
	now := time.Now()
	d.mu.Lock()
	if now.Before(d.next[deviceID]) {
		d.mu.Unlock()
		return
	}
	wait := retryMissing
	if hasName || d.known[deviceID] {
		wait = refreshFound
	}
	d.next[deviceID] = now.Add(wait)
	d.mu.Unlock()
	select {
	case d.queue <- job{deviceID, ip}:
	default:
	}
}

func (d *Discoverer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-d.queue:
					if name, src := d.Lookup(j.ip); name != "" {
						d.mu.Lock()
						d.known[j.deviceID] = true
						d.mu.Unlock()
						d.sink.SetHostname(j.deviceID, name, src)
					}
				}
			}
		}()
	}
	wg.Wait()
}

// Lookup asks the device at ip for its name.
func (d *Discoverer) Lookup(ip net.IP) (name, source string) {
	if n := Clean(d.mdnsName(ip)); n != "" {
		return n, "mdns"
	}
	if n := Clean(d.netbiosName(ip)); n != "" {
		return n, "netbios"
	}
	return "", ""
}

// mdnsName sends a unicast mDNS reverse (PTR) query straight to the device.
// Bonjour answers "legacy unicast" queries sent from a non-5353 port.
func (d *Discoverer) mdnsName(ip net.IP) string {
	rev, err := dns.ReverseAddr(ip.String())
	if err != nil {
		return ""
	}
	m := new(dns.Msg)
	m.SetQuestion(rev, dns.TypePTR)
	m.RecursionDesired = false
	c := &dns.Client{Net: "udp", Timeout: d.timeout}
	resp, _, err := c.Exchange(m, net.JoinHostPort(ip.String(), "5353"))
	if err != nil || resp == nil {
		return ""
	}
	for _, rr := range append(resp.Answer, resp.Extra...) {
		if ptr, ok := rr.(*dns.PTR); ok && strings.HasSuffix(strings.ToLower(ptr.Ptr), ".local.") {
			return ptr.Ptr
		}
	}
	return ""
}

// netbiosName sends a NetBIOS node status request (UDP 137), which Windows
// answers with its computer name.
func (d *Discoverer) netbiosName(ip net.IP) string {
	conn, err := net.DialTimeout("udp4", net.JoinHostPort(ip.String(), "137"), d.timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(d.timeout))
	if _, err := conn.Write(netbiosStatusRequest(uint16(rand.UintN(0xffff)))); err != nil {
		return ""
	}
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return ""
	}
	return parseNetbiosStatus(buf[:n])
}

func netbiosStatusRequest(id uint16) []byte {
	b := make([]byte, 12, 50)
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[4:], 1) // one question
	// Wildcard name "*" padded with NULs, in NetBIOS first-level encoding.
	b = append(b, 32)
	name := [16]byte{'*'}
	for _, c := range name {
		b = append(b, 'A'+c>>4, 'A'+c&0x0f)
	}
	b = append(b, 0, 0x00, 0x21, 0x00, 0x01) // end of name, type NBSTAT, class IN
	return b
}

// parseNetbiosStatus returns the first unique workstation name (suffix 0x00).
func parseNetbiosStatus(b []byte) string {
	if len(b) < 12 || binary.BigEndian.Uint16(b[6:]) == 0 {
		return ""
	}
	off := 12
	// Skip the answer name (labels or a compression pointer).
	for off < len(b) {
		l := int(b[off])
		if l&0xc0 == 0xc0 {
			off += 2
			break
		}
		off++
		if l == 0 {
			break
		}
		off += l
	}
	off += 10 // type, class, TTL, rdlength
	if off >= len(b) {
		return ""
	}
	count := int(b[off])
	off++
	for i := 0; i < count && off+18 <= len(b); i++ {
		entry := b[off : off+18]
		off += 18
		suffix, flags := entry[15], binary.BigEndian.Uint16(entry[16:])
		if suffix == 0x00 && flags&0x8000 == 0 { // workstation, not a group
			return strings.TrimRight(string(entry[:15]), " \x00")
		}
	}
	return ""
}
