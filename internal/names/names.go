// Package names cleans up device names and discovers them on the network.
package names

import (
	"net"
	"regexp"
	"strings"
)

// Clean turns a raw hostname ("Emmas-iPhone.local.", "DESKTOP-AB12.lan") into
// a display name ("Emmas-iPhone", "DESKTOP-AB12"). Returns "" for junk.
func Clean(s string) string {
	s = strings.TrimSpace(strings.TrimSuffix(s, "."))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 63 {
		s = s[:63]
	}
	if s == "" || net.ParseIP(s) != nil || ipLike.MatchString(s) {
		return ""
	}
	return s
}

var ipLike = regexp.MustCompile(`^[0-9]{1,3}([-.][0-9]{1,3}){3}$`)

// Slug converts a name into a DNS label: "Emma's iPad" -> "emmas-ipad".
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’':
			// drop apostrophes so "Emma's" -> "emmas"
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// Factory-default names shared by many devices. These are never used to
// decide that two addresses belong to the same device.
var generic = regexp.MustCompile(`^(iphone|ipad|ipod|ipod-touch|imac|mac|macbook|macbook-air|macbook-pro|mac-mini|apple-tv|appletv|homepod|watch|apple-watch|android|galaxy|pixel|chromebook|desktop|laptop|localhost|unknown|amazon|echo|kindle|fire-tv|roku|xbox|playstation|ps4|ps5|switch|nintendo-switch|printer|espressif|esp|wlan0|raspberrypi|linux|none|android-device|host|client|device)([- ]?\(?[0-9]+\)?)?$`)

// IsGeneric reports whether a name is too common to identify one device.
func IsGeneric(name string) bool {
	return name == "" || generic.MatchString(Slug(name))
}

// IsRandomMAC reports whether mac is a locally administered ("private" /
// randomized) address, as used by iOS, Android and Windows for Wi-Fi privacy.
func IsRandomMAC(mac string) bool {
	hw, err := net.ParseMAC(mac)
	return err == nil && len(hw) > 0 && hw[0]&0x02 != 0
}
