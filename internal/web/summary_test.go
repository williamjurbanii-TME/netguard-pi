package web

import (
	"testing"

	"netguard/internal/store"
)

func TestSummarizeKidsTablet(t *testing.T) {
	rows := []store.DomainActivity{
		{Domain: "www.youtube.com", Reason: "service:youtube", Lookups: 30, Blocked: 30},
		{Domain: "i.ytimg.com", Reason: "service:youtube", Lookups: 4, Blocked: 4},
		{Domain: "api-global.netflix.com", Lookups: 12},
		{Domain: "apis.roblox.com", Lookups: 40},
		{Domain: "www.youtube.com", Reason: "safesearch", Lookups: 2, Restrict: 2},
		{Domain: "pbskids.org", Lookups: 9},
		{Domain: "cdn.pbskids.org", Lookups: 3},
		{Domain: "adultsite.example", Reason: "list:adult", Lookups: 2, Blocked: 2},
		{Domain: "app-measurement.com", Reason: "list:ads", Lookups: 15, Blocked: 15},
		{Domain: "gateway.icloud.com", Lookups: 20},
		{Domain: "mask.icloud.com", Reason: "private-relay", Lookups: 3, Blocked: 3},
	}
	dev := &store.Device{ID: "ipad", GroupID: 2}
	group := &store.Group{ID: 2, Name: "Kids", Services: []string{"youtube"}}
	s := summarize(rows, 24, dev, group)

	apps := map[string]appUse{}
	for _, a := range s.Apps {
		apps[a.Key] = a
	}
	if yt := apps["youtube"]; yt.Lookups != 36 || yt.Blocked != 34 || yt.Restricted != 2 || !yt.BlockedByRule || yt.Category != "Video" {
		t.Errorf("youtube = %+v", yt)
	}
	if rb := apps["roblox"]; rb.Lookups != 40 || rb.Blocked != 0 || rb.BlockedByRule {
		t.Errorf("roblox = %+v", rb)
	}
	if _, ok := apps["netflix"]; !ok {
		t.Error("netflix missing")
	}
	if s.Apps[0].Category != "Video" || s.Apps[len(s.Apps)-1].Key != "roblox" {
		t.Errorf("apps should be ordered by category (Video before Games): %+v", s.Apps)
	}
	if len(s.Flagged) != 1 || s.Flagged[0].Site != "adultsite.example" || s.Flagged[0].Category != "adult" || s.Flagged[0].Blocked != 2 {
		t.Errorf("flagged = %+v", s.Flagged)
	}
	if len(s.Sites) != 1 || s.Sites[0].Site != "pbskids.org" || s.Sites[0].Lookups != 12 {
		t.Errorf("other sites should be just pbskids.org (12 lookups): %+v", s.Sites)
	}
	if s.Background != 15+20+3 {
		t.Errorf("background = %d, want ads + iCloud + private relay = 38", s.Background)
	}
	if s.Lookups != 140 || s.GroupName != "Kids" {
		t.Errorf("totals = %d lookups, group %q", s.Lookups, s.GroupName)
	}
}
