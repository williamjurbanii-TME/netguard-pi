package dhcpserver

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// unicaster sends a reply straight to a client's hardware address. A client
// that has no IP yet and did not set the broadcast flag expects this (RFC 2131
// section 4.1). Broadcasting instead mostly works on cables, but Wi-Fi delivers
// broadcasts at low rate without retries, and devices in power-save (TVs in
// standby, phones) often miss them, so they never finish getting an address.
type unicaster interface {
	Send(dstMAC net.HardwareAddr, dstIP net.IP, payload []byte) error
}

// needsHWUnicast reports whether resp must go to the client's MAC + yiaddr.
func needsHWUnicast(req, resp *dhcpv4.DHCPv4) bool {
	return isZero(req.GatewayIPAddr) && isZero(req.ClientIPAddr) && !req.IsBroadcast() &&
		resp.MessageType() != dhcpv4.MessageTypeNak && !isZero(resp.YourIPAddr)
}

// interfaceFor finds the network interface that owns ip.
func interfaceFor(ip net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		addrs, _ := ifaces[i].Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return &ifaces[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no interface has address %s", ip)
}

// buildUDP returns an IPv4+UDP packet (no link-layer header) with valid checksums.
func buildUDP(src, dst net.IP, sport, dport uint16, payload []byte) []byte {
	be := binary.BigEndian
	b := make([]byte, 28+len(payload))
	b[0] = 0x45 // IPv4, 20-byte header
	be.PutUint16(b[2:], uint16(len(b)))
	b[8] = 64 // TTL
	b[9] = 17 // UDP
	copy(b[12:16], src.To4())
	copy(b[16:20], dst.To4())
	be.PutUint16(b[10:], checksum(b[:20]))

	u := b[20:]
	be.PutUint16(u[0:], sport)
	be.PutUint16(u[2:], dport)
	be.PutUint16(u[4:], uint16(len(u)))
	copy(u[8:], payload)
	pseudo := make([]byte, 12+len(u))
	copy(pseudo[0:4], src.To4())
	copy(pseudo[4:8], dst.To4())
	pseudo[9] = 17
	be.PutUint16(pseudo[10:], uint16(len(u)))
	copy(pseudo[12:], u)
	cs := checksum(pseudo)
	if cs == 0 {
		cs = 0xffff // 0 means "no checksum" in UDP over IPv4
	}
	be.PutUint16(u[6:], cs)
	return b
}

// checksum is the Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}
