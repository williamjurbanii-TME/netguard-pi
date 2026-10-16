// demodata creates a NetGuard database for a fictional household, for
// screenshots and demos. Every device, name and address is made up.
//
//	go run ./tools/demodata -out demo/data/netguard.db
//	netguard -config demo/config.yaml
package main

import (
	"flag"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"time"

	"netguard/internal/store"
)

// Group IDs seeded by store.Open.
const (
	gDefault = 1
	gKids    = 2
	gSchool  = 3
	gTVs     = 4
)

type q struct {
	domain, status, reason string
	weight                 int
}

func allow(d string, w int) q        { return q{d, "allowed", "", w} }
func blk(d, why string, w int) q     { return q{d, "blocked", why, w} }
func seen(d, cat string, w int) q    { return q{d, "allowed", "seen:" + cat, w} }
func safe(d string, w int) q         { return q{d, "safesearch", "safesearch", w} }
func local(d string, w int) q        { return q{d, "local", "", w} }
func ads(d string, w int) q          { return blk(d, "list:ads", w) }
func tv(d string, w int) q           { return blk(d, "list:smarttv", w) }
func service(d, svc string, w int) q { return blk(d, "service:"+svc, w) }

type device struct {
	ids      []string // MACs; more than one = rotated private address
	name     string
	hostname string
	src      string
	group    int64
	ip       string
	paused   bool
	perDay   int
	hours    [24]int // relative activity per hour of day
	queries  []q
}

var (
	evening  = [24]int{1, 1, 1, 0, 0, 0, 1, 2, 2, 2, 2, 2, 3, 3, 3, 3, 4, 5, 8, 10, 10, 9, 6, 3}
	workday  = [24]int{0, 0, 0, 0, 0, 0, 1, 4, 8, 9, 9, 8, 6, 8, 9, 9, 8, 5, 2, 1, 1, 1, 0, 0}
	kids     = [24]int{0, 0, 0, 0, 0, 0, 1, 3, 2, 0, 0, 0, 0, 1, 1, 5, 8, 9, 8, 7, 5, 2, 0, 0}
	steady   = [24]int{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}
	allDay   = [24]int{1, 0, 0, 0, 0, 0, 2, 5, 6, 5, 4, 5, 6, 5, 4, 5, 6, 7, 8, 8, 7, 5, 3, 2}
	schoolDy = [24]int{0, 0, 0, 0, 0, 0, 1, 3, 6, 7, 7, 6, 5, 6, 6, 4, 5, 6, 6, 5, 4, 2, 0, 0}
)

var devices = []device{
	{ids: []string{"00:00:5e:00:53:10"}, name: "Living Room TV", hostname: "LivingRoom-RokuTV", src: "dhcp", group: gTVs, ip: "192.168.1.31",
		perDay: 2200, hours: evening, queries: []q{
			allow("api-global.netflix.com", 40), allow("nrdp.prod.cloud.netflix.com", 25), allow("occ-0-2433-3.1.nflxso.net", 30),
			allow("youtubei.googleapis.com", 25), allow("api.roku.com", 20), allow("image.roku.com", 15), allow("hulu.com", 10),
			tv("scribe.logs.roku.com", 60), tv("cooper.logs.roku.com", 35), tv("giga.logs.roku.com", 25), tv("ichnaea.netflix.com", 20),
			ads("p.ads.roku.com", 30), ads("customerevents.netflix.com", 15), ads("googleads.g.doubleclick.net", 15), ads("pubads.g.doubleclick.net", 10),
		}},
	{ids: []string{"00:00:5e:00:53:11"}, name: "Bedroom TV", hostname: "Samsung-TV", src: "dhcp", group: gDefault, ip: "192.168.1.32",
		perDay: 1400, hours: evening, queries: []q{
			allow("api-global.netflix.com", 25), allow("samsungtvplus.com", 15), allow("osb.samsungqbe.com", 10),
			seen("acr0.samsungcloudsolution.com", "smarttv", 40), seen("log-config.samsungacr.com", "smarttv", 30), seen("acr-us-prd.samsungcloud.tv", "smarttv", 25),
			ads("ads.samsungads.com", 35), ads("config.samsungads.com", 20), ads("samsung-com.112.2o7.net", 10),
		}},
	{ids: []string{"00:00:5e:00:53:12"}, name: "Kitchen Echo", hostname: "amazon-kitchen", src: "dhcp", group: gDefault, ip: "192.168.1.33",
		perDay: 700, hours: steady, queries: []q{
			allow("api.amazonalexa.com", 40), allow("avs-alexa-4-na.amazon.com", 30), allow("ntp-g7g.amazon.com", 10), allow("ap-gue1.spotify.com", 8),
			ads("device-metrics-us.amazon.com", 35), ads("unagi-na.amazon.com", 20), ads("mads.amazon.com", 10),
		}},
	{ids: []string{"00:00:5e:00:53:13"}, name: "Living Room Echo", hostname: "amazon-living", src: "dhcp", group: gDefault, ip: "192.168.1.34",
		perDay: 550, hours: steady, queries: []q{
			allow("api.amazonalexa.com", 40), allow("avs-alexa-4-na.amazon.com", 25), allow("ntp-g7g.amazon.com", 10),
			ads("device-metrics-us.amazon.com", 30), ads("unagi-na.amazon.com", 15),
		}},
	{ids: []string{"02:4a:91:3c:7e:15", "06:1d:2b:88:40:a3"}, name: "Mom's iPhone", hostname: "Moms-iPhone", src: "dhcp", group: gDefault, ip: "192.168.1.41",
		perDay: 1600, hours: allDay, queries: []q{
			allow("gateway.icloud.com", 30), allow("graph.instagram.com", 25), allow("scontent.cdninstagram.com", 30), allow("weather-data.apple.com", 10),
			allow("www.pinterest.com", 12), allow("api.spotify.com", 15), allow("mail.google.com", 10),
			ads("app-measurement.com", 30), ads("metrics.icloud.com", 15), ads("googleads.g.doubleclick.net", 20), ads("graph.facebook.com", 10), ads("adservice.google.com", 10),
		}},
	{ids: []string{"0a:7c:e2:51:09:6b"}, name: "Dad's iPhone", hostname: "Dads-iPhone", src: "dhcp", group: gDefault, ip: "192.168.1.42",
		perDay: 1300, hours: allDay, queries: []q{
			allow("gateway.icloud.com", 25), allow("site.api.espn.com", 25), allow("www.reddit.com", 20), allow("api.weather.com", 8), allow("maps.apple.com", 8),
			ads("app-measurement.com", 25), ads("metrics.icloud.com", 12), ads("securepubads.g.doubleclick.net", 15), ads("ads.reddit.com", 10),
		}},
	{ids: []string{"02:9e:41:d0:3a:27"}, name: "Kid's iPad", hostname: "Kids-iPad", src: "dhcp", group: gKids, ip: "192.168.1.43",
		perDay: 900, hours: kids, queries: []q{
			allow("pbskids.org", 20), allow("www.abcya.com", 15), allow("api.roblox.com", 15), allow("gateway.icloud.com", 10),
			allow("api-global.netflix.com", 8), blk("adult-site.example", "list:adult", 1),
			service("www.youtube.com", "youtube", 30), service("i.ytimg.com", "youtube", 20), service("youtubei.googleapis.com", "youtube", 15),
			service("www.tiktok.com", "tiktok", 12), safe("www.google.com", 15), blk("mask.icloud.com", "private-relay", 6),
			ads("app-measurement.com", 15), ads("googleads.g.doubleclick.net", 10),
		}},
	{ids: []string{"00:00:5e:00:53:14"}, name: "Teen's Chromebook", hostname: "chromebook-teen", src: "dhcp", group: gSchool, ip: "192.168.1.44",
		perDay: 800, hours: schoolDy, queries: []q{
			allow("classroom.google.com", 30), allow("docs.google.com", 30), allow("www.khanacademy.org", 15), allow("clever.com", 10),
			safe("www.google.com", 20), safe("www.youtube.com", 15), blk("www.draftkings.com", "list:gambling", 2),
			ads("pagead2.googlesyndication.com", 10), ads("www.google-analytics.com", 12),
		}},
	{ids: []string{"00:00:5e:00:53:15"}, name: "Work Laptop", hostname: "LAPTOP-7Q2M", src: "dhcp", group: gDefault, ip: "192.168.1.45",
		perDay: 1800, hours: workday, queries: []q{
			allow("teams.microsoft.com", 40), allow("outlook.office365.com", 35), allow("login.microsoftonline.com", 20), allow("slack.com", 20),
			allow("github.com", 10), local("wpad.lan", 5),
			ads("watson.telemetry.microsoft.com", 15), ads("browser.events.data.msn.com", 10), ads("www.google-analytics.com", 8),
		}},
	{ids: []string{"00:00:5e:00:53:16"}, name: "Family PC", hostname: "DESKTOP-FAMILY", src: "dhcp", group: gDefault, ip: "192.168.1.46",
		perDay: 1500, hours: evening, queries: []q{
			allow("www.google.com", 30), allow("store.steampowered.com", 15), allow("discord.com", 15), allow("www.amazon.com", 15),
			ads("beacons.gvt2.com", 30), ads("beacons4.gvt2.com", 20), ads("analytics.ff.avast.com", 10), ads("www.googletagmanager.com", 12),
		}},
	{ids: []string{"00:00:5e:00:53:17"}, name: "Xbox", hostname: "XboxOne", src: "dhcp", group: gKids, ip: "192.168.1.47", paused: true,
		perDay: 400, hours: kids, queries: []q{
			allow("xboxlive.com", 30), allow("title.mgt.xboxlive.com", 20), ads("settings-win.data.microsoft.com", 10),
		}},
	{ids: []string{"00:00:5e:00:53:18"}, name: "3D Printer", hostname: "BambuLab-P1S", src: "dhcp", group: gDefault, ip: "192.168.1.48",
		perDay: 120, hours: steady, queries: []q{allow("api.bambulab.com", 30), allow("us.mqtt.bambulab.com", 40)}},
	{ids: []string{"00:00:5e:00:53:19"}, name: "Video Doorbell", hostname: "Doorbell", src: "dhcp", group: gDefault, ip: "192.168.1.49",
		perDay: 150, hours: steady, queries: []q{allow("fw.ring.com", 20), allow("es.ring.com", 30), ads("telemetry.ring.com", 15)}},
	{ids: []string{"00:00:5e:00:53:1a"}, name: "Thermostat", hostname: "Thermostat", src: "dhcp", group: gDefault, ip: "192.168.1.50",
		perDay: 90, hours: steady, queries: []q{allow("home.nest.com", 30), allow("time.google.com", 10)}},
}

func pick[T any](r *rand.Rand, items []T, weight func(T) int) T {
	total := 0
	for _, it := range items {
		total += weight(it)
	}
	n := r.IntN(total)
	for _, it := range items {
		if n -= weight(it); n < 0 {
			return it
		}
	}
	return items[len(items)-1]
}

func main() {
	out := flag.String("out", "demo/data/netguard.db", "database file to create")
	days := flag.Int("days", 7, "days of history")
	flag.Parse()
	if _, err := os.Stat(*out); err == nil {
		log.Fatalf("%s already exists; delete it first", *out)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	r := rand.New(rand.NewPCG(42, 2026))
	now := time.Now()
	var all []store.QueryEntry
	seen := map[string]store.Seen{}
	for _, d := range devices {
		first := now.Add(-time.Duration(*days) * 24 * time.Hour).Unix()
		dev := &store.Device{ID: d.ids[0], Name: d.name, GroupID: d.group, Hostname: d.hostname, HostnameSource: d.src,
			LastIP: d.ip, LastIPv4: d.ip, Paused: d.paused, FirstSeen: first, LastSeen: now.Unix()}
		if err := st.InsertDevice(dev, d.ids[0]); err != nil {
			log.Fatal(err)
		}
		for _, extra := range d.ids[1:] {
			if err := st.SetAlias(extra, dev.ID, d.ip, now.Unix()); err != nil {
				log.Fatal(err)
			}
		}

		hours := make([]int, 24)
		for i := range hours {
			hours[i] = i
		}
		for day := *days - 1; day >= 0; day-- {
			date := now.AddDate(0, 0, -day)
			midnight := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
			count := d.perDay * (85 + r.IntN(30)) / 100
			for i := 0; i < count; i++ {
				h := pick(r, hours, func(h int) int { return d.hours[h] })
				ts := midnight.Add(time.Duration(h)*time.Hour + time.Duration(r.IntN(3600))*time.Second)
				if ts.After(now) {
					continue
				}
				qq := pick(r, d.queries, func(x q) int { return x.weight })
				status, reason := qq.status, qq.reason
				if d.paused && now.Sub(ts) < 3*time.Hour {
					status, reason = "blocked", "paused"
				} else if status == "allowed" && reason == "" && r.IntN(10) < 4 {
					status = "cached"
				}
				all = append(all, store.QueryEntry{
					TS: ts.Unix(), DeviceID: dev.ID, ClientIP: d.ip, Domain: qq.domain,
					QType: []string{"A", "A", "A", "AAAA", "HTTPS"}[r.IntN(5)], Status: status, Reason: reason, Ms: int64(r.IntN(40)),
				})
			}
		}
		seen[d.ids[0]] = store.Seen{DeviceID: dev.ID, IP: d.ip, TS: now.Unix() - int64(r.IntN(240))}
	}
	// Insert in time order so the query log reads naturally.
	sort.Slice(all, func(i, j int) bool { return all[i].TS < all[j].TS })
	for i := 0; i < len(all); i += 5000 {
		if err := st.WriteBatch(all[i:min(i+5000, len(all))], nil); err != nil {
			log.Fatal(err)
		}
	}
	if err := st.WriteBatch(nil, seen); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %d devices and %d lookups to %s", len(devices), len(all), *out)
}
