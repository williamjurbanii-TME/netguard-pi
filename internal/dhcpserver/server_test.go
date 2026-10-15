package dhcpserver

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"netguard/internal/config"
	"netguard/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.DHCPConfig{
		Enabled: true, ServerIP: "10.0.0.2", RangeStart: "10.0.0.100", RangeEnd: "10.0.0.102",
		Netmask: "255.255.255.0", Router: "10.0.0.1", DNS: []string{"10.0.0.2"}, LeaseHours: 24, DomainName: "lan",
	}
	s, err := New(cfg, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// doLease runs DISCOVER -> OFFER -> REQUEST -> ACK and returns the ACK.
func doLease(t *testing.T, s *Server, mac string) *dhcpv4.DHCPv4 {
	t.Helper()
	hw, _ := net.ParseMAC(mac)
	disc, _ := dhcpv4.NewDiscovery(hw, dhcpv4.WithOption(dhcpv4.OptHostName("host-"+mac[len(mac)-2:])))
	offer := s.handle(disc)
	if offer == nil || offer.MessageType() != dhcpv4.MessageTypeOffer {
		t.Fatalf("%s: expected OFFER, got %v", mac, offer)
	}
	req, _ := dhcpv4.NewRequestFromOffer(offer)
	ack := s.handle(req)
	if ack == nil || ack.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("%s: expected ACK, got %v", mac, ack)
	}
	return ack
}

func TestLeaseFlow(t *testing.T) {
	s, _ := newTestServer(t)
	ack := doLease(t, s, "aa:00:00:00:00:01")
	if got := ack.YourIPAddr.String(); got != "10.0.0.100" {
		t.Errorf("first lease = %s, want 10.0.0.100", got)
	}
	if dns := ack.DNS(); len(dns) != 1 || !dns[0].Equal(net.ParseIP("10.0.0.2")) {
		t.Errorf("DNS option = %v, want [10.0.0.2]", dns)
	}
	if r := ack.Router(); len(r) != 1 || !r[0].Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("router option = %v", r)
	}
	// Same client gets the same address again.
	if got := doLease(t, s, "aa:00:00:00:00:01").YourIPAddr.String(); got != "10.0.0.100" {
		t.Errorf("renewed lease = %s, want 10.0.0.100", got)
	}
	if got := doLease(t, s, "aa:00:00:00:00:02").YourIPAddr.String(); got != "10.0.0.101" {
		t.Errorf("second client = %s, want 10.0.0.101", got)
	}
	if mac := s.MACForIP(net.ParseIP("10.0.0.101")); mac != "aa:00:00:00:00:02" {
		t.Errorf("MACForIP = %q", mac)
	}
}

func TestReservationAndExhaustion(t *testing.T) {
	s, st := newTestServer(t)
	// Reserve .102 for a device before it ever asks.
	// The device has two MACs (a rotated private address): both get the reservation.
	st.InsertDevice(&store.Device{ID: "aa:00:00:00:00:09", GroupID: 1, StaticIP: "10.0.0.102"}, "aa:00:00:00:00:09")
	st.SetAlias("06:00:00:00:00:0a", "aa:00:00:00:00:09", "", 0)
	if err := s.LoadReservations(); err != nil {
		t.Fatal(err)
	}
	doLease(t, s, "aa:00:00:00:00:01") // .100
	doLease(t, s, "aa:00:00:00:00:02") // .101
	// Pool is now full for everyone else (.102 is reserved).
	hw, _ := net.ParseMAC("aa:00:00:00:00:03")
	disc, _ := dhcpv4.NewDiscovery(hw)
	if offer := s.handle(disc); offer != nil {
		t.Errorf("expected no offer when pool is exhausted, got %s", offer.YourIPAddr)
	}
	if got := doLease(t, s, "aa:00:00:00:00:09").YourIPAddr.String(); got != "10.0.0.102" {
		t.Errorf("reserved device got %s, want 10.0.0.102", got)
	}
	// Same device, rotated MAC: takes over its reserved address.
	if got := doLease(t, s, "06:00:00:00:00:0a").YourIPAddr.String(); got != "10.0.0.102" {
		t.Errorf("reserved device's second MAC got %s, want 10.0.0.102", got)
	}
}

func TestRequestForForeignAddressIsNaked(t *testing.T) {
	s, _ := newTestServer(t)
	hw, _ := net.ParseMAC("aa:00:00:00:00:05")
	req, _ := dhcpv4.New(
		dhcpv4.WithHwAddr(hw),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.1.50"))),
	)
	req.OpCode = dhcpv4.OpcodeBootRequest
	resp := s.handle(req)
	if resp == nil || resp.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("expected NAK for address outside our subnet, got %v", resp)
	}
}
