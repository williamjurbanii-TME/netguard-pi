package filter

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"netguard/internal/config"
	"netguard/internal/store"
)

func TestParseLine(t *testing.T) {
	cases := map[string][]string{
		"0.0.0.0 ads.example.com":           {"ads.example.com"},
		"127.0.0.1 a.com b.com # comment":   {"a.com", "b.com"},
		"tracker.net":                       {"tracker.net"},
		"||ads.site.org^":                   {"ads.site.org"},
		"||ads.site.org^$third-party":       {"ads.site.org"},
		"example.com##.banner":              nil,
		"@@||good.com^":                     nil,
		"# comment":                         nil,
		"0.0.0.0 localhost":                 nil,
		"0.0.0.0 0.0.0.0":                   nil,
		"Bad.Domain.COM.":                   {"bad.domain.com"},
		"/regex/":                           nil,
	}
	for in, want := range cases {
		if got := parseLine(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseLine(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.YouTube.com/watch?v=1": "www.youtube.com",
		"*.example.com":                     "example.com",
		"example.com:443":                   "example.com",
		"not a domain":                      "",
	} {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeSearchTarget(t *testing.T) {
	for in, want := range map[string]string{
		"www.google.com":    "forcesafesearch.google.com",
		"google.co.uk":      "forcesafesearch.google.com",
		"mail.google.com":   "",
		"google.evil.co.uk": "",
		"www.youtube.com":   "restrict.youtube.com",
		"www.bing.com":      "strict.bing.com",
		"example.com":       "",
	} {
		if got := safeSearchTarget(in); got != want {
			t.Errorf("safeSearchTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	listDir := filepath.Join(dir, "lists")
	os.MkdirAll(listDir, 0o755)
	srcs := []config.ListSource{
		{Category: "ads", Name: "ads", URL: "http://test/ads"},
		{Category: "adult", Name: "adult", URL: "http://test/adult"},
	}
	os.WriteFile(filepath.Join(listDir, cacheName(srcs[0].URL)), []byte("0.0.0.0 doubleclick.net\n"), 0o644)
	os.WriteFile(filepath.Join(listDir, cacheName(srcs[1].URL)), []byte("badsite.com\n"), 0o644)
	lm := NewListManager(listDir, srcs)
	lm.Refresh(false) // files are fresh, so nothing is downloaded
	e, err := NewEngine(st, lm)
	if err != nil {
		t.Fatal(err)
	}
	return e, st
}

func TestEngineGroups(t *testing.T) {
	e, st := newTestEngine(t)
	e.EnsureDevice("aa:bb:cc:dd:ee:01", "10.0.0.5")
	d, _ := st.GetDevice("aa:bb:cc:dd:ee:01")
	d.GroupID = 2 // seeded "Kids" group
	st.UpdateDevice(d)
	e.Reload()

	check := func(dev, domain string, want Action, wantReason string) {
		t.Helper()
		got := e.Check(dev, domain)
		if got.Action != want || (wantReason != "" && got.Reason != wantReason) {
			t.Errorf("Check(%s, %s) = %+v, want action %d reason %q", dev, domain, got, want, wantReason)
		}
	}
	// Unknown device -> Default group: ads blocked, adult/YouTube allowed.
	check("unknown", "doubleclick.net", Block, "list:ads")
	check("unknown", "sub.doubleclick.net", Block, "list:ads")
	check("unknown", "badsite.com", Allow, "")
	check("unknown", "www.youtube.com", Allow, "")
	// Kids group.
	kid := "aa:bb:cc:dd:ee:01"
	check(kid, "badsite.com", Block, "list:adult")
	check(kid, "www.youtube.com", Block, "service:youtube")
	check(kid, "www.google.com", Rewrite, "safesearch")
	check(kid, "use-application-dns.net", NXDomain, "doh-canary")
	check(kid, "dns.google", Block, "doh")
	check(kid, "wikipedia.org", Allow, "")

	// Allow list overrides blocking but keeps SafeSearch.
	g, _ := st.GetGroup(2)
	g.CustomAllow = []string{"youtube.com"}
	g.CustomBlock = []string{"wikipedia.org"}
	st.SaveGroup(g)
	e.Reload()
	check(kid, "www.youtube.com", Rewrite, "safesearch")
	check(kid, "music.youtube.com", Allow, "allowlist")
	check(kid, "en.wikipedia.org", Block, "custom")

	// Paused blocks everything.
	d.Paused = true
	st.UpdateDevice(d)
	e.Reload()
	check(kid, "music.youtube.com", Block, "paused")
}
