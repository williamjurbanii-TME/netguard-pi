package names

import (
	"encoding/binary"
	"testing"
)

func TestSlugCleanGeneric(t *testing.T) {
	for in, want := range map[string]string{
		"Emma's iPad": "emmas-ipad", "DESKTOP-AB12": "desktop-ab12", "  Living Room TV ": "living-room-tv",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Emmas-iPhone.local.": "Emmas-iPhone", "pc.lan": "pc", "192-168-1-5": "", "": "",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	for name, want := range map[string]bool{
		"iPhone": true, "iPad (2)": true, "iPhone-3": true, "android": true,
		"Emmas-iPhone": false, "DESKTOP-AB12CD": false, "android-1a2b3c4d5e6f7a8b": false,
	} {
		if got := IsGeneric(name); got != want {
			t.Errorf("IsGeneric(%q) = %v, want %v", name, got, want)
		}
	}
	if !IsRandomMAC("06:11:22:33:44:55") || !IsRandomMAC("da:a1:19:00:00:01") || IsRandomMAC("a4:83:e7:00:00:01") {
		t.Error("IsRandomMAC misclassified")
	}
}

func TestNetbios(t *testing.T) {
	req := netbiosStatusRequest(0x1234)
	if len(req) != 50 || string(req[13:15]) != "CK" {
		t.Fatalf("bad request encoding: % x", req)
	}
	// Build a node-status response with a group name then the workstation name.
	resp := make([]byte, 12)
	binary.BigEndian.PutUint16(resp[6:], 1) // one answer
	resp = append(resp, 0xc0, 0x0c, 0, 0x21, 0, 1, 0, 0, 0, 0, 0, 0)
	resp = append(resp, 2)
	entry := func(name string, suffix byte, flags uint16) []byte {
		b := []byte(name + "               ")[:15]
		b = append(b, suffix, byte(flags>>8), byte(flags))
		return b
	}
	resp = append(resp, entry("WORKGROUP", 0x00, 0x8400)...)
	resp = append(resp, entry("KIDS-LAPTOP", 0x00, 0x0400)...)
	if got := parseNetbiosStatus(resp); got != "KIDS-LAPTOP" {
		t.Errorf("parseNetbiosStatus = %q", got)
	}
}
