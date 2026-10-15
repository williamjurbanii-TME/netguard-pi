package filter

import (
	"sort"
	"strings"
)

// Services maps an app/site to the domains it needs. Blocking a service blocks
// these domains and all of their subdomains. Note that YouTube Kids shares
// ytimg.com/googlevideo.com with YouTube, so blocking "youtube" breaks it too.
var Services = map[string][]string{
	"youtube":   {"youtube.com", "youtu.be", "ytimg.com", "googlevideo.com", "youtube-nocookie.com", "youtubei.googleapis.com", "youtube.googleapis.com", "yt.be"},
	"tiktok":    {"tiktok.com", "tiktokv.com", "tiktokv.us", "tiktokcdn.com", "tiktokcdn-us.com", "tiktokcdn-eu.com", "byteoversea.com", "ibytedtos.com", "ibyteimg.com", "muscdn.com", "musical.ly", "tiktokw.us"},
	"instagram": {"instagram.com", "cdninstagram.com", "ig.me", "instagr.am"},
	"facebook":  {"facebook.com", "facebook.net", "fbcdn.net", "fb.com", "fb.me", "fbsbx.com", "messenger.com"},
	"snapchat":  {"snapchat.com", "sc-cdn.net", "sc-static.net", "snap-dev.net", "snapkit.co", "snapads.com", "sc-prod.net"},
	"twitter":   {"twitter.com", "x.com", "twimg.com", "t.co"},
	"reddit":    {"reddit.com", "redd.it", "redditmedia.com", "redditstatic.com", "reddituploads.com"},
	"discord":   {"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discordcdn.com"},
	"twitch":    {"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "twitchsvc.net", "ext-twitch.tv"},
	"roblox":    {"roblox.com", "rbxcdn.com", "rbx.com", "robloxlabs.com"},
	"fortnite":  {"fortnite.com", "epicgames.com", "epicgames.dev", "epicgames.net"},
	"minecraft": {"minecraft.net", "minecraftservices.com", "mojang.com"},
	"steam":     {"steampowered.com", "steamcommunity.com", "steamstatic.com", "steamcontent.com", "steamserver.net", "valvesoftware.com"},
	"netflix":   {"netflix.com", "netflix.net", "nflxvideo.net", "nflximg.net", "nflxext.com", "nflxso.net"},
}

func ServiceNames() []string {
	out := make([]string, 0, len(Services))
	for k := range Services {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dohDomains are public DNS-over-HTTPS endpoints. Blocking them stops browsers
// and apps from bypassing NetGuard with their own encrypted DNS.
var dohDomains = []string{
	"dns.google", "dns.google.com", "cloudflare-dns.com", "mozilla.cloudflare-dns.com",
	"chrome.cloudflare-dns.com", "one.one.one.one", "1dot1dot1dot1.cloudflare-dns.com",
	"dns.quad9.net", "dns9.quad9.net", "dns10.quad9.net", "dns11.quad9.net",
	"doh.opendns.com", "dns.nextdns.io", "doh.cleanbrowsing.org", "dns.adguard.com",
	"dns.adguard-dns.com", "doh.mullvad.net", "dns.mullvad.net", "doh.dns.sb",
	"dns.controld.com", "freedns.controld.com", "doh.libredns.gr", "dns.alidns.com",
}

// firefoxCanary: answering NXDOMAIN tells Firefox to disable its automatic DoH.
const firefoxCanary = "use-application-dns.net"

// privateRelay: answering NXDOMAIN for these makes Apple devices turn off
// iCloud Private Relay on this network (Apple's documented network opt-out),
// so their DNS lookups come to NetGuard instead of being encrypted to Apple.
var privateRelay = map[string]bool{"mask.icloud.com": true, "mask-h2.icloud.com": true, "mask.apple-dns.net": true}

// safeSearchTarget returns the CNAME target that forces SafeSearch for a
// search engine / YouTube hostname, or "" if the domain is not affected.
func safeSearchTarget(d string) string {
	switch d {
	case "www.youtube.com", "m.youtube.com", "youtube.com", "youtubei.googleapis.com",
		"youtube.googleapis.com", "www.youtube-nocookie.com":
		return "restrict.youtube.com"
	case "www.bing.com", "bing.com":
		return "strict.bing.com"
	case "duckduckgo.com", "www.duckduckgo.com", "start.duckduckgo.com":
		return "safe.duckduckgo.com"
	}
	// Google search on any country domain: google.com, www.google.co.uk, ...
	host := strings.TrimPrefix(d, "www.")
	if tld, ok := strings.CutPrefix(host, "google."); ok && isCountryTLD(tld) {
		return "forcesafesearch.google.com"
	}
	return ""
}

func isCountryTLD(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if len(p) < 2 || len(p) > 3 {
			return false
		}
	}
	return true
}
