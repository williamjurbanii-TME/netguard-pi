//go:build linux

package dhcpserver

import (
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"golang.org/x/sys/unix"
)

// rawSender writes IPv4 packets addressed to a specific MAC via an AF_PACKET
// socket (needs CAP_NET_RAW). The kernel adds the Ethernet header.
type rawSender struct {
	fd      int
	ifindex int
	src     net.IP
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func newUnicaster(src net.IP) (unicaster, error) {
	ifi, err := interfaceFor(src)
	if err != nil {
		return nil, err
	}
	// Protocol 0: this socket only sends, it never receives (and so never
	// queues copies of incoming traffic).
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &rawSender{fd: fd, ifindex: ifi.Index, src: src.To4()}, nil
}

func (r *rawSender) Send(dstMAC net.HardwareAddr, dstIP net.IP, payload []byte) error {
	sa := &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_IP), Ifindex: r.ifindex, Halen: uint8(len(dstMAC))}
	copy(sa.Addr[:], dstMAC)
	return unix.Sendto(r.fd, buildUDP(r.src, dstIP, dhcpv4.ServerPort, dhcpv4.ClientPort, payload), 0, sa)
}
