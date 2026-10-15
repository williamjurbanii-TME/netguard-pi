package dhcpserver

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

func TestBuildUDPChecksums(t *testing.T) {
	src, dst := net.ParseIP("192.168.1.2"), net.ParseIP("192.168.1.125")
	payload := []byte("hello dhcp, odd length")
	p := buildUDP(src, dst, 67, 68, payload)
	if len(p) != 28+len(payload) || p[0] != 0x45 || p[9] != 17 {
		t.Fatalf("bad header: % x", p[:20])
	}
	if checksum(p[:20]) != 0 {
		t.Error("IPv4 header checksum does not verify")
	}
	u := p[20:]
	pseudo := append(append(append([]byte{}, src.To4()...), dst.To4()...), 0, 17, 0, 0)
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(u)))
	if checksum(append(pseudo, u...)) != 0 {
		t.Error("UDP checksum does not verify")
	}
	if binary.BigEndian.Uint16(u[0:]) != 67 || binary.BigEndian.Uint16(u[2:]) != 68 || string(u[8:]) != string(payload) {
		t.Error("UDP ports or payload wrong")
	}
}

func TestNeedsHWUnicast(t *testing.T) {
	hw, _ := net.ParseMAC("02:00:5e:00:00:7c")
	disc, _ := dhcpv4.NewDiscovery(hw) // NewDiscovery sets the broadcast flag
	disc.SetUnicast()                  // like a TV in standby: "reply to my MAC"
	offer, _ := dhcpv4.NewReplyFromRequest(disc, dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer),
		dhcpv4.WithYourIP(net.ParseIP("192.168.1.125")))
	if !needsHWUnicast(disc, offer) {
		t.Error("offer to a client without the broadcast flag should go to its MAC")
	}
	disc.SetBroadcast()
	if needsHWUnicast(disc, offer) {
		t.Error("client asked for broadcast")
	}
	disc.SetUnicast()
	disc.ClientIPAddr = net.ParseIP("192.168.1.125")
	if needsHWUnicast(disc, offer) {
		t.Error("renewing client (ciaddr set) gets normal UDP unicast")
	}
}
