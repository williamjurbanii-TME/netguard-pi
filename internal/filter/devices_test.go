package filter

import (
	"net"
	"testing"
)

func TestRotatedPrivateMACMergesByName(t *testing.T) {
	e, st := newTestEngine(t)

	// Emma's iPhone joins, gets named via DHCP, and is put in the Kids group.
	oldID, isNew := e.EnsureDevice("aa:00:00:00:00:01", "192.168.1.20")
	if !isNew {
		t.Fatal("first sighting should create a device")
	}
	e.SetHostname(oldID, "Emmas-iPhone", "dhcp")
	d, _ := st.GetDevice(oldID)
	d.GroupID = 2
	st.UpdateDevice(d)
	e.Reload()

	// Later it reconnects with a new private (locally administered) MAC.
	newID, isNew := e.EnsureDevice("06:11:22:33:44:55", "192.168.1.21")
	if !isNew || newID == oldID {
		t.Fatal("unknown MAC should first appear as a new device")
	}
	if got := e.Check(newID, "www.youtube.com").Action; got != Allow {
		t.Fatalf("before merge the new MAC is in Default group, got %v", got)
	}
	// Its name arrives (DHCP option 12 or mDNS) -> merged into the known device.
	e.SetHostname(newID, "Emmas-iPhone.local.", "mdns")
	if id, _ := e.EnsureDevice("06:11:22:33:44:55", "192.168.1.21"); id != oldID {
		t.Fatalf("rotated MAC should now resolve to %s, got %s", oldID, id)
	}
	if got := e.Check(oldID, "www.youtube.com").Action; got != Block {
		t.Errorf("merged device should keep Kids rules, got %v", got)
	}
	if e.Device(newID) != nil {
		t.Error("the temporary device should be gone")
	}
}

func TestFactoryMACsWithSameNameDoNotMerge(t *testing.T) {
	e, _ := newTestEngine(t)
	// Two mesh Wi-Fi satellites: real (universally administered) MACs, same model name.
	a, _ := e.EnsureDevice("00:00:5e:00:53:27", "192.168.1.27")
	e.SetHostname(a, "Mesh-Node", "dhcp")
	b, _ := e.EnsureDevice("00:00:5e:00:53:25", "192.168.1.25")
	e.SetHostname(b, "Mesh-Node", "dhcp")
	if e.Device(a) == nil || e.Device(b) == nil {
		t.Fatal("devices with factory MACs must never be auto-merged")
	}
}

func TestSharedHostnameResolvesOnlyForItself(t *testing.T) {
	e, _ := newTestEngine(t)
	a, _ := e.EnsureDevice("00:00:5e:00:53:27", "192.168.1.27")
	b, _ := e.EnsureDevice("00:00:5e:00:53:25", "192.168.1.25")
	e.SetHostname(a, "Mesh-Node", "dhcp")
	e.SetHostname(b, "Mesh-Node", "dhcp")
	e.TouchIP(a, net.ParseIP("192.168.1.27"))
	e.TouchIP(b, net.ParseIP("192.168.1.25"))
	if ip := e.LookupName("mesh-node", a); ip.String() != "192.168.1.27" {
		t.Errorf("satellite A asking for its own name got %v", ip)
	}
	if ip := e.LookupName("mesh-node", b); ip.String() != "192.168.1.25" {
		t.Errorf("satellite B asking for its own name got %v", ip)
	}
	if ip := e.LookupName("mesh-node", "laptop"); ip != nil {
		t.Errorf("an ambiguous name must not resolve for other devices, got %v", ip)
	}
	// Echo devices report "linux": never a local name.
	c, _ := e.EnsureDevice("00:00:5e:00:53:31", "192.168.1.31")
	e.SetHostname(c, "linux", "dhcp")
	e.TouchIP(c, net.ParseIP("192.168.1.31"))
	if ip := e.LookupName("linux", "laptop"); ip != nil {
		t.Errorf("generic name 'linux' resolved to %v", ip)
	}
}

func TestHostnamePriority(t *testing.T) {
	e, _ := newTestEngine(t)
	id, _ := e.EnsureDevice("02:00:5e:00:00:7c", "192.168.1.125")
	e.SetHostname(id, "LivingRoom-RokuTV", "dhcp")
	e.SetHostname(id, "X0001234567", "mdns") // serial number via mDNS
	if h := e.Device(id).Hostname; h != "LivingRoom-RokuTV" {
		t.Errorf("mDNS overwrote the DHCP name: %q", h)
	}
	// A descriptive mDNS name is kept when DHCP only says "iPhone".
	p, _ := e.EnsureDevice("aa:00:00:00:00:44", "192.168.1.44")
	e.SetHostname(p, "Emmas-iPhone", "mdns")
	e.SetHostname(p, "iPhone", "dhcp")
	if h := e.Device(p).Hostname; h != "Emmas-iPhone" {
		t.Errorf("generic DHCP name replaced a descriptive one: %q", h)
	}
}

func TestGenericNamesDoNotMerge(t *testing.T) {
	e, _ := newTestEngine(t)
	a, _ := e.EnsureDevice("aa:00:00:00:00:01", "192.168.1.20")
	b, _ := e.EnsureDevice("aa:00:00:00:00:02", "192.168.1.21")
	e.SetHostname(a, "iPhone", "dhcp")
	e.SetHostname(b, "iPhone", "dhcp")
	if e.Device(a) == nil || e.Device(b) == nil {
		t.Fatal("two devices both called the default name 'iPhone' must stay separate")
	}
}

func TestPlaceholderAdoptedWhenMACAppears(t *testing.T) {
	e, _ := newTestEngine(t)
	pid, _ := e.EnsureDevice("ip:192.168.1.30", "192.168.1.30")
	id, isNew := e.EnsureDevice("aa:00:00:00:00:30", "192.168.1.30")
	if id != pid || isNew {
		t.Fatalf("MAC should adopt the ip: placeholder %s, got %s (new=%v)", pid, id, isNew)
	}
}

func TestManualMergeDetachAndLocalNames(t *testing.T) {
	e, st := newTestEngine(t)
	a, _ := e.EnsureDevice("aa:00:00:00:00:01", "192.168.1.20")
	b, _ := e.EnsureDevice("aa:00:00:00:00:02", "192.168.1.21")
	da, _ := st.GetDevice(a)
	da.Name = "Emma's iPad"
	st.UpdateDevice(da)
	e.Reload()
	e.TouchIP(a, net.ParseIP("192.168.1.20"))

	if ip := e.LookupName("emmas-ipad", "someone-else"); ip == nil || ip.String() != "192.168.1.20" {
		t.Errorf("LookupName = %v", ip)
	}
	if n := e.NameForIP(net.ParseIP("192.168.1.20")); n != "emmas-ipad" {
		t.Errorf("NameForIP = %q", n)
	}

	if err := e.Merge(b, a); err != nil {
		t.Fatal(err)
	}
	if id, _ := e.EnsureDevice("aa:00:00:00:00:02", ""); id != a {
		t.Errorf("after merge, second MAC -> %s, want %s", id, a)
	}
	if err := e.Detach("aa:00:00:00:00:02"); err != nil {
		t.Fatal(err)
	}
	if id, isNew := e.EnsureDevice("aa:00:00:00:00:02", ""); id == a || !isNew {
		t.Errorf("after detach, MAC should become a new device, got %s", id)
	}
	if err := e.Detach("aa:00:00:00:00:01"); err == nil {
		t.Error("detaching a device's only address should fail")
	}
}
