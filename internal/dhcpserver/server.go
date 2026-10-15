// Package dhcpserver is a small DHCPv4 server that hands out NetGuard as the DNS server.
package dhcpserver

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"netguard/internal/config"
	"netguard/internal/store"
)

const offerHold = 2 * time.Minute

type lease struct {
	mac      string
	ip       uint32
	hostname string
	expires  time.Time
	pending  bool // offered but not yet acknowledged
}

// ClientCallback is called synchronously for every DISCOVER/REQUEST, before an
// address is chosen, with the client's MAC and the hostname it sent (option
// 12). It registers the device and may merge a rotated private MAC into a
// known device, which in turn updates reservations via LoadReservations.
type ClientCallback func(mac, hostname string)

type Server struct {
	cfg       config.DHCPConfig
	store     *store.Store
	onClient  ClientCallback
	serverIP  net.IP
	start     uint32
	end       uint32
	mask      net.IPMask
	router    net.IP
	dns       []net.IP
	leaseTime time.Duration

	mu           sync.Mutex
	leases       map[string]*lease          // mac -> lease
	byIP         map[uint32]string          // ip -> mac
	reservations map[string]uint32          // mac -> ip
	reservedIPs  map[uint32]map[string]bool // ip -> macs of the device that owns it

	conn net.PacketConn
	raw  unicaster // nil: fall back to broadcasting replies
}

func ip2u(ip net.IP) uint32 {
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v4)
}

func u2ip(u uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, u)
	return ip
}

func New(cfg config.DHCPConfig, st *store.Store, onClient ClientCallback) (*Server, error) {
	s := &Server{
		cfg:          cfg,
		store:        st,
		onClient:     onClient,
		serverIP:     net.ParseIP(cfg.ServerIP).To4(),
		start:        ip2u(net.ParseIP(cfg.RangeStart)),
		end:          ip2u(net.ParseIP(cfg.RangeEnd)),
		mask:         net.IPMask(net.ParseIP(cfg.Netmask).To4()),
		leaseTime:    time.Duration(cfg.LeaseHours) * time.Hour,
		leases:       map[string]*lease{},
		byIP:         map[uint32]string{},
		reservations: map[string]uint32{},
		reservedIPs:  map[uint32]map[string]bool{},
	}
	if s.start > s.end {
		return nil, fmt.Errorf("dhcp: range_start is after range_end")
	}
	if cfg.Router != "" {
		s.router = net.ParseIP(cfg.Router).To4()
	}
	for _, d := range cfg.DNS {
		if ip := net.ParseIP(d).To4(); ip != nil {
			s.dns = append(s.dns, ip)
		}
	}
	saved, err := st.ListLeases()
	if err != nil {
		return nil, err
	}
	for _, l := range saved {
		s.setLease(l.MAC, ip2u(net.ParseIP(l.IP)), l.Hostname, time.Unix(l.Expires, 0), false)
	}
	if err := s.LoadReservations(); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadReservations reads static IP assignments from the device table. A
// reservation applies to every MAC address of the device.
func (s *Server) LoadReservations() error {
	devices, err := s.store.ListDevices()
	if err != nil {
		return err
	}
	aliases, err := s.store.ListAliases()
	if err != nil {
		return err
	}
	static := map[string]uint32{}
	for _, d := range devices {
		if ip := ip2u(net.ParseIP(d.StaticIP)); ip != 0 {
			static[d.ID] = ip
		}
	}
	res, rip := map[string]uint32{}, map[uint32]map[string]bool{}
	for _, a := range aliases {
		ip, ok := static[a.DeviceID]
		if !ok {
			continue
		}
		hw, err := net.ParseMAC(a.Identifier)
		if err != nil {
			continue
		}
		mac := hw.String()
		res[mac] = ip
		if rip[ip] == nil {
			rip[ip] = map[string]bool{}
		}
		rip[ip][mac] = true
	}
	s.mu.Lock()
	s.reservations, s.reservedIPs = res, rip
	s.mu.Unlock()
	return nil
}

func (s *Server) Start() error {
	// On Windows a socket bound to the interface address still receives
	// broadcasts and sends them out of the right NIC. On Linux, binding a
	// specific address would miss broadcasts, so bind the wildcard.
	addr := ":67"
	if runtime.GOOS == "windows" {
		addr = net.JoinHostPort(s.serverIP.String(), "67")
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("dhcp listen %s: %w", addr, err)
	}
	s.conn = conn
	if u, err := newUnicaster(s.serverIP); err == nil {
		s.raw = u
	} else {
		log.Printf("dhcp: replies to new clients will be broadcast (%v)", err)
	}
	log.Printf("dhcp: serving %s-%s on %s", s.cfg.RangeStart, s.cfg.RangeEnd, addr)
	go s.serve()
	return nil
}

func (s *Server) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Server) serve() {
	buf := make([]byte, 1500)
	for {
		n, _, err := s.conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return // closed
		}
		req, err := dhcpv4.FromBytes(buf[:n])
		if err != nil {
			continue
		}
		resp := s.handle(req)
		if resp == nil {
			continue
		}
		data := resp.ToBytes()
		if s.raw != nil && needsHWUnicast(req, resp) {
			err := s.raw.Send(req.ClientHWAddr, resp.YourIPAddr, data)
			if err == nil {
				continue
			}
			log.Printf("dhcp: direct reply to %s failed, broadcasting: %v", req.ClientHWAddr, err)
		}
		if _, err := s.conn.WriteTo(data, destination(req, resp)); err != nil {
			log.Printf("dhcp: send to %s: %v", req.ClientHWAddr, err)
		}
	}
}

func isZero(ip net.IP) bool { return ip == nil || ip.IsUnspecified() }

func destination(req, resp *dhcpv4.DHCPv4) net.Addr {
	if !isZero(req.GatewayIPAddr) {
		return &net.UDPAddr{IP: req.GatewayIPAddr, Port: dhcpv4.ServerPort}
	}
	if resp.MessageType() != dhcpv4.MessageTypeNak && !isZero(req.ClientIPAddr) {
		return &net.UDPAddr{IP: req.ClientIPAddr, Port: dhcpv4.ClientPort}
	}
	return &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
}

func (s *Server) handle(req *dhcpv4.DHCPv4) *dhcpv4.DHCPv4 {
	if req.OpCode != dhcpv4.OpcodeBootRequest || len(req.ClientHWAddr) != 6 {
		return nil
	}
	mac := req.ClientHWAddr.String()
	mt := req.MessageType()
	if s.onClient != nil && (mt == dhcpv4.MessageTypeDiscover || mt == dhcpv4.MessageTypeRequest) {
		s.onClient(mac, req.HostName()) // must not hold s.mu: it may call LoadReservations
	}
	switch mt {
	case dhcpv4.MessageTypeDiscover:
		ip := s.allocate(mac, ip2u(req.RequestedIPAddress()))
		if ip == 0 {
			log.Printf("dhcp: no free address for %s", mac)
			return nil
		}
		s.mu.Lock()
		s.setLease(mac, ip, req.HostName(), time.Now().Add(offerHold), true)
		s.mu.Unlock()
		return s.reply(req, dhcpv4.MessageTypeOffer, u2ip(ip))

	case dhcpv4.MessageTypeRequest:
		if sid := req.ServerIdentifier(); !isZero(sid) && !sid.Equal(s.serverIP) {
			s.dropPending(mac) // client picked another server
			return nil
		}
		ip := ip2u(req.RequestedIPAddress())
		if ip == 0 {
			ip = ip2u(req.ClientIPAddr) // renewing / rebinding
		}
		if ip == 0 || !s.canAssign(mac, ip) {
			return s.nak(req)
		}
		host := req.HostName()
		s.mu.Lock()
		if host == "" && s.leases[mac] != nil {
			host = s.leases[mac].hostname
		}
		s.setLease(mac, ip, host, time.Now().Add(s.leaseTime), false)
		s.mu.Unlock()
		ipStr := u2ip(ip).String()
		if err := s.store.UpsertLease(store.Lease{MAC: mac, IP: ipStr, Hostname: host, Expires: time.Now().Add(s.leaseTime).Unix()}); err != nil {
			log.Printf("dhcp: save lease: %v", err)
		}
		log.Printf("dhcp: %s -> %s (%s)", mac, ipStr, host)
		return s.reply(req, dhcpv4.MessageTypeAck, u2ip(ip))

	case dhcpv4.MessageTypeRelease:
		s.mu.Lock()
		if l := s.leases[mac]; l != nil {
			l.expires = time.Now()
		}
		s.mu.Unlock()
		return nil

	case dhcpv4.MessageTypeDecline:
		if ip := ip2u(req.RequestedIPAddress()); ip != 0 {
			// Someone else is using this address; park it for a while.
			s.mu.Lock()
			s.setLease("declined:"+u2ip(ip).String(), ip, "", time.Now().Add(10*time.Minute), false)
			s.mu.Unlock()
		}
		return nil

	case dhcpv4.MessageTypeInform:
		return s.reply(req, dhcpv4.MessageTypeAck, nil)
	}
	return nil
}

func (s *Server) reply(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, ip net.IP) *dhcpv4.DHCPv4 {
	mods := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithServerIP(s.serverIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.serverIP)),
		dhcpv4.WithNetmask(s.mask),
	}
	if len(s.dns) > 0 {
		mods = append(mods, dhcpv4.WithDNS(s.dns...))
	}
	if s.router != nil {
		mods = append(mods, dhcpv4.WithRouter(s.router))
	}
	if s.cfg.DomainName != "" {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptDomainName(s.cfg.DomainName)))
	}
	if ip != nil {
		mods = append(mods, dhcpv4.WithYourIP(ip), dhcpv4.WithLeaseTime(uint32(s.leaseTime.Seconds())))
	}
	resp, err := dhcpv4.NewReplyFromRequest(req, mods...)
	if err != nil {
		log.Printf("dhcp: build reply: %v", err)
		return nil
	}
	return resp
}

func (s *Server) nak(req *dhcpv4.DHCPv4) *dhcpv4.DHCPv4 {
	resp, err := dhcpv4.NewReplyFromRequest(req,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeNak),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.serverIP)))
	if err != nil {
		return nil
	}
	return resp
}

func (s *Server) dropPending(mac string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.leases[mac]; l != nil && l.pending {
		delete(s.byIP, l.ip)
		delete(s.leases, mac)
	}
}

// setLease records a lease, evicting any stale holder of the same IP. Caller holds mu
// (or is the constructor).
func (s *Server) setLease(mac string, ip uint32, host string, exp time.Time, pending bool) {
	if old := s.leases[mac]; old != nil && old.ip != ip {
		delete(s.byIP, old.ip)
	}
	if other, ok := s.byIP[ip]; ok && other != mac {
		delete(s.leases, other)
		_ = s.store.DeleteLease(other)
	}
	s.leases[mac] = &lease{mac: mac, ip: ip, hostname: host, expires: exp, pending: pending}
	s.byIP[ip] = mac
}

func (s *Server) canAssign(mac string, ip uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canAssignLocked(mac, ip, true)
}

// canAssignLocked reports whether ip may be given to mac. If allowExpired is
// false, IPs that have ever been leased to someone else are skipped.
func (s *Server) canAssignLocked(mac string, ip uint32, allowExpired bool) bool {
	if res, ok := s.reservations[mac]; ok {
		return ip == res
	}
	if ip < s.start || ip > s.end || ip == ip2u(s.serverIP) || (s.router != nil && ip == ip2u(s.router)) {
		return false
	}
	if owners, ok := s.reservedIPs[ip]; ok && !owners[mac] {
		return false
	}
	if owner, ok := s.byIP[ip]; ok && owner != mac {
		if !allowExpired {
			return false
		}
		return time.Now().After(s.leases[owner].expires)
	}
	return true
}

func (s *Server) allocate(mac string, requested uint32) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res, ok := s.reservations[mac]; ok {
		return res
	}
	if l := s.leases[mac]; l != nil && s.canAssignLocked(mac, l.ip, true) {
		return l.ip
	}
	if requested != 0 && s.canAssignLocked(mac, requested, false) {
		return requested
	}
	for _, allowExpired := range []bool{false, true} {
		for ip := s.start; ip <= s.end && ip >= s.start; ip++ {
			if s.canAssignLocked(mac, ip, allowExpired) {
				return ip
			}
		}
	}
	return 0
}

// MACForIP implements clients.LeaseLookup.
func (s *Server) MACForIP(ip net.IP) string {
	u := ip2u(ip)
	if u == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mac, ok := s.byIP[u]; ok && len(mac) == 17 {
		return mac
	}
	return ""
}

// Leases returns a snapshot of current leases for the dashboard.
func (s *Server) Leases() []store.Lease {
	s.mu.Lock()
	out := make([]store.Lease, 0, len(s.leases))
	for _, l := range s.leases {
		if l.pending || len(l.mac) != 17 {
			continue
		}
		out = append(out, store.Lease{MAC: l.mac, IP: u2ip(l.ip).String(), Hostname: l.hostname, Expires: l.expires.Unix()})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return ip2u(net.ParseIP(out[i].IP)) < ip2u(net.ParseIP(out[j].IP)) })
	return out
}
