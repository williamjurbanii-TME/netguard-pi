package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAdStats(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().Unix()
	q := func(dev, domain, status, reason string, ago int64) QueryEntry {
		return QueryEntry{TS: now - ago, DeviceID: dev, ClientIP: "10.0.0.1", Domain: domain, QType: "A", Status: status, Reason: reason}
	}
	err = s.WriteBatch([]QueryEntry{
		q("tv", "ads.samsung.com", "blocked", "list:smarttv", 10),
		q("tv", "ads.samsung.com", "blocked", "list:smarttv", 20),
		q("tv", "log.samsungacr.com", "allowed", "seen:smarttv", 30),
		q("tv", "netflix.com", "allowed", "", 40),
		q("phone", "doubleclick.net", "blocked", "list:ads", 50),
		q("phone", "old.ad.com", "blocked", "list:ads", 3*86400), // outside 24h
		q("phone", "pornsite.com", "blocked", "list:adult", 60),  // not an ad category
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := AdFilter{
		Blocked: []string{"list:ads", "list:smarttv"}, Seen: []string{"seen:ads", "seen:smarttv"},
		Since: now - 86400,
	}
	byDev, err := s.AdByDevice(f)
	if err != nil {
		t.Fatal(err)
	}
	if byDev["tv"] != (AdCount{Blocked: 2, Allowed: 1}) || byDev["phone"] != (AdCount{Blocked: 1}) {
		t.Errorf("AdByDevice = %+v", byDev)
	}
	series, err := s.AdSeries(f, 86400, 0)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range series {
		total += b.Blocked + b.Allowed
		if b.T%86400 != 0 {
			t.Errorf("bucket %d not aligned to a day", b.T)
		}
	}
	if total != 4 {
		t.Errorf("series total = %d, want 4", total)
	}
	f.DeviceID = "tv"
	rows, _ := s.AdDomains(f, 10)
	if len(rows) != 2 || rows[0].Domain != "ads.samsung.com" || rows[0].Blocked != 2 {
		t.Errorf("AdDomains = %+v", rows)
	}
	if h := s.History(); h.Queries != 7 || h.Bytes == 0 {
		t.Errorf("History = %+v", h)
	}
}
