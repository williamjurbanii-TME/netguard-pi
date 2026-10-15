package filter

import (
	"sort"
	"strings"
)

// Service is an app or site that can be recognised in the query log and
// blocked per group. Blocking it blocks Domains and all their subdomains.
type Service struct {
	Label    string
	Category string
	Domains  []string
}

// ServiceCategories is the display order of service categories.
var ServiceCategories = []string{"Video", "Social", "Messaging", "Games", "Music", "AI chatbots"}

// Services is keyed by the short name stored in a group's settings. Note
// that YouTube Kids shares ytimg.com/googlevideo.com with YouTube, so
// blocking "youtube" breaks YouTube Kids too.
var Services = map[string]Service{
	// Video
	"youtube":     {"YouTube", "Video", []string{"youtube.com", "youtu.be", "ytimg.com", "googlevideo.com", "youtube-nocookie.com", "youtubei.googleapis.com", "youtube.googleapis.com", "yt.be"}},
	"youtubekids": {"YouTube Kids", "Video", []string{"youtubekids.com"}},
	"netflix":     {"Netflix", "Video", []string{"netflix.com", "netflix.net", "nflxvideo.net", "nflximg.net", "nflxext.com", "nflxso.net"}},
	"disneyplus":  {"Disney+", "Video", []string{"disneyplus.com", "disney-plus.net", "dssott.com", "bamgrid.com"}},
	"hulu":        {"Hulu", "Video", []string{"hulu.com", "hulustream.com", "huluim.com"}},
	"primevideo":  {"Prime Video", "Video", []string{"primevideo.com", "aiv-cdn.net", "aiv-delivery.net", "amazonvideo.com"}},
	"max":         {"Max (HBO)", "Video", []string{"max.com", "hbomax.com", "hbo.com", "hbomaxcdn.com"}},
	"capcut":      {"CapCut", "Video", []string{"capcut.com", "capcutapi.com", "capcutapi.us", "capcutstatic.com"}},
	"twitch":      {"Twitch", "Video", []string{"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "twitchsvc.net", "ext-twitch.tv"}},
	// Social
	"tiktok":    {"TikTok", "Social", []string{"tiktok.com", "tiktokv.com", "tiktokv.us", "tiktokcdn.com", "tiktokcdn-us.com", "tiktokcdn-eu.com", "byteoversea.com", "ibytedtos.com", "ibyteimg.com", "muscdn.com", "musical.ly", "tiktokw.us"}},
	"instagram": {"Instagram", "Social", []string{"instagram.com", "cdninstagram.com", "ig.me", "instagr.am"}},
	"facebook":  {"Facebook", "Social", []string{"facebook.com", "facebook.net", "fbcdn.net", "fb.com", "fb.me", "fbsbx.com", "messenger.com"}},
	"snapchat":  {"Snapchat", "Social", []string{"snapchat.com", "sc-cdn.net", "sc-static.net", "snap-dev.net", "snapkit.co", "snapads.com", "sc-prod.net"}},
	"twitter":   {"X (Twitter)", "Social", []string{"twitter.com", "x.com", "twimg.com", "t.co"}},
	"reddit":    {"Reddit", "Social", []string{"reddit.com", "redd.it", "redditmedia.com", "redditstatic.com", "reddituploads.com"}},
	"pinterest": {"Pinterest", "Social", []string{"pinterest.com", "pinimg.com"}},
	// Locket Widget (friends' photos on the home screen). Its API host is
	// api.locketcamera.com; sign-in also uses Google Firebase, shared by many
	// apps, so blocking may not be complete.
	"locket": {"Locket", "Social", []string{"locketcamera.com", "locket.camera", "locket.cam"}},
	// Messaging
	"discord":  {"Discord", "Messaging", []string{"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discordcdn.com"}},
	"whatsapp": {"WhatsApp", "Messaging", []string{"whatsapp.com", "whatsapp.net", "wa.me"}},
	"telegram": {"Telegram", "Messaging", []string{"telegram.org", "t.me", "telegram.me"}},
	// Games
	"roblox":      {"Roblox", "Games", []string{"roblox.com", "rbxcdn.com", "rbx.com", "robloxlabs.com"}},
	"fortnite":    {"Fortnite / Epic", "Games", []string{"fortnite.com", "epicgames.com", "epicgames.dev", "epicgames.net"}},
	"minecraft":   {"Minecraft", "Games", []string{"minecraft.net", "minecraftservices.com", "mojang.com"}},
	"steam":       {"Steam", "Games", []string{"steampowered.com", "steamcommunity.com", "steamstatic.com", "steamcontent.com", "steamserver.net", "valvesoftware.com"}},
	"xbox":        {"Xbox Live", "Games", []string{"xboxlive.com", "xbox.com"}},
	"playstation": {"PlayStation", "Games", []string{"playstation.com", "playstation.net", "sonyentertainmentnetwork.com"}},
	"nintendo":    {"Nintendo", "Games", []string{"nintendo.com", "nintendo.net"}},
	// Music
	"spotify": {"Spotify", "Music", []string{"spotify.com", "scdn.co", "spotifycdn.com"}},
	// AI chatbots
	"chatgpt":     {"ChatGPT", "AI chatbots", []string{"chatgpt.com", "openai.com", "oaiusercontent.com"}},
	"characterai": {"Character.AI", "AI chatbots", []string{"character.ai"}},
	"claude":      {"Claude", "AI chatbots", []string{"claude.ai"}},
	"gemini":      {"Gemini", "AI chatbots", []string{"gemini.google.com"}},
	"copilot":     {"Copilot", "AI chatbots", []string{"copilot.microsoft.com"}},
}

// ServiceInfo is a service as listed in the dashboard.
type ServiceInfo struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
}

// ServiceList returns all services in category order, then by label.
func ServiceList() []ServiceInfo {
	rank := map[string]int{}
	for i, c := range ServiceCategories {
		rank[c] = i
	}
	out := make([]ServiceInfo, 0, len(Services))
	for k, s := range Services {
		out = append(out, ServiceInfo{k, s.Label, s.Category})
	}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Category] != rank[out[j].Category] {
			return rank[out[i].Category] < rank[out[j].Category]
		}
		return out[i].Label < out[j].Label
	})
	return out
}

var serviceByDomain = func() map[string]string {
	m := map[string]string{}
	for key, s := range Services {
		for _, d := range s.Domains {
			m[d] = key
		}
	}
	return m
}()

// ServiceFor returns the service a domain belongs to (matching subdomains),
// or "".
func ServiceFor(domain string) string {
	var key string
	walk(domain, func(d string) bool { key = serviceByDomain[d]; return key != "" })
	return key
}

// backgroundSites are system and infrastructure services that devices contact
// on their own (OS updates, cloud sync, content delivery networks, certificate
// checks). The device summary hides them so real activity stands out.
var backgroundSites = map[string]bool{
	// Apple
	"apple.com": true, "icloud.com": true, "icloud-content.com": true, "apple-dns.net": true, "aaplimg.com": true,
	"mzstatic.com": true, "cdn-apple.com": true, "apple-cloudkit.com": true, "apple-mapkit.com": true, "apple.news": true,
	// Google infrastructure (google.com itself stays visible: it's search)
	"gstatic.com": true, "googleapis.com": true, "googleusercontent.com": true, "gvt1.com": true, "gvt2.com": true,
	"gvt3.com": true, "1e100.net": true, "ggpht.com": true, "pki.goog": true, "googlezip.net": true,
	// Microsoft / Windows
	"microsoft.com": true, "windows.com": true, "windowsupdate.com": true, "msftncsi.com": true, "msftconnecttest.com": true,
	"live.com": true, "microsoftonline.com": true, "windows.net": true, "msedge.net": true, "bing.net": true, "office.net": true,
	// Content delivery networks and cloud hosting
	"akamai.net": true, "akamaihd.net": true, "akamaized.net": true, "akamaiedge.net": true, "edgekey.net": true,
	"edgesuite.net": true, "akadns.net": true, "cloudfront.net": true, "amazonaws.com": true, "fastly.net": true,
	"fastly-edge.com": true, "cloudflare.com": true, "cloudflare-dns.com": true, "azureedge.net": true, "trafficmanager.net": true,
	// Certificates and time
	"digicert.com": true, "lencr.org": true, "letsencrypt.org": true, "sectigo.com": true, "globalsign.com": true,
	"ntp.org": true, "usertrust.com": true,
	// Device platforms
	"amazonalexa.com": true, "a2z.com": true, "amazon.dev": true, "roku.com": true, "netgear.com": true,
}

// IsBackground reports whether a registrable domain is system/infrastructure
// traffic rather than something a person chose to visit.
func IsBackground(site string) bool {
	return backgroundSites[site] || strings.HasSuffix(site, ".arpa") || site == "arpa"
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
