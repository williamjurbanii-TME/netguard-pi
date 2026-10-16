package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// fourDays writes lookups spread over the last four days and returns the
// store and the start of the window (local midnight four days ago).
func fourDays(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().Unix()
	var entries []QueryEntry
	add := func(ago int64, dev, domain, status, reason string) {
		entries = append(entries, QueryEntry{TS: now - ago, DeviceID: dev, ClientIP: "192.168.1.9", Domain: domain, QType: "A", Status: status, Reason: reason})
	}
	for day := int64(0); day < 4; day++ {
		for i := int64(0); i < 5; i++ {
			ago := day*86400 + i*3000 + 60
			add(ago, "tv", "logs.example-tv.com", "blocked", "list:smarttv")
			add(ago, "tv", "video.example.com", "allowed", "")
			add(ago, "tablet", "ads.example.net", "blocked", "list:ads")
			add(ago, "tablet", "tracker.example.org", "allowed", "seen:ads")
			add(ago, "tablet", "www.search.example", "safesearch", "safesearch")
			add(ago, "tablet", "wpad.lan", "local", "")
		}
	}
	if err := s.WriteBatch(entries, nil); err != nil {
		t.Fatal(err)
	}
	return s, dayStart(now - 4*86400)
}

func sortedSites(rows []SiteRow) []SiteRow {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return a.Domain+a.DeviceID+a.Reason < b.Domain+b.DeviceID+b.Reason
	})
	return rows
}

func TestDailyMatchesRawLog(t *testing.T) {
	s, since := fourDays(t)
	if !useDaily(since) {
		t.Fatal("a 4-day window should use the rollup")
	}
	// Raw answers for the same window, computed directly from the log.
	rawCounts := map[string]Counts{}
	rows, _ := s.db.Query(`SELECT device_id, COUNT(*), SUM(status='blocked') FROM queries WHERE ts>=? GROUP BY device_id`, since)
	for rows.Next() {
		var id string
		var c Counts
		rows.Scan(&id, &c.Total, &c.Blocked)
		rawCounts[id] = c
	}
	rows.Close()

	got, err := s.CountsByDevice(since)
	if err != nil || !reflect.DeepEqual(got, rawCounts) {
		t.Errorf("CountsByDevice from rollup = %v, %v; raw = %v", got, err, rawCounts)
	}

	f := AdFilter{Blocked: []string{"list:ads", "list:smarttv"}, Seen: []string{"seen:ads", "seen:smarttv"}, Since: since}
	byDev, _ := s.AdByDevice(f)
	if byDev["tv"] != (AdCount{Blocked: 20}) || byDev["tablet"] != (AdCount{Blocked: 20, Allowed: 20}) {
		t.Errorf("AdByDevice from rollup = %+v", byDev)
	}
	doms, _ := s.AdDomains(f, 10)
	if len(doms) != 3 {
		t.Errorf("AdDomains from rollup = %+v", doms)
	}
	_, off := time.Now().Zone()
	series, _ := s.AdSeries(f, 86400, int64(off))
	var total int64
	for _, b := range series {
		total += b.Blocked + b.Allowed
	}
	if len(series) != 4 || total != 60 {
		t.Errorf("AdSeries from rollup: %d days, %d lookups; want 4 days, 60", len(series), total)
	}

	// Sites and device activity: rollup vs raw over the same aligned window.
	daily, _ := s.SiteRows(QueryFilter{}, since, 1000)
	var raw []SiteRow
	r2, _ := s.db.Query(`SELECT domain, device_id, reason, COUNT(*), SUM(status='blocked'), MIN(ts), MAX(ts) FROM queries
		WHERE ts>=? AND status!='local' GROUP BY domain, device_id, reason`, since)
	for r2.Next() {
		var x SiteRow
		r2.Scan(&x.Domain, &x.DeviceID, &x.Reason, &x.Lookups, &x.Blocked, &x.First, &x.Last)
		raw = append(raw, x)
	}
	r2.Close()
	if !reflect.DeepEqual(sortedSites(daily), sortedSites(raw)) {
		t.Errorf("SiteRows rollup != raw:\n%v\n%v", daily, raw)
	}
	act, _ := s.DeviceActivity("tablet", since)
	var restricted int64
	for _, a := range act {
		restricted += a.Restrict
		if a.Domain == "wpad.lan" {
			t.Error("local answers must be excluded")
		}
	}
	if len(act) != 3 || restricted != 20 {
		t.Errorf("DeviceActivity from rollup = %+v", act)
	}
}

func TestDailyFollowsMergeRetagPruneAndBackfill(t *testing.T) {
	s, since := fourDays(t)
	sum := func(where string, args ...any) int64 {
		var n int64
		s.db.QueryRow(`SELECT COALESCE(SUM(lookups),0) FROM daily WHERE `+where, args...).Scan(&n)
		return n
	}

	// Merge moves the rollup too, adding up overlapping rows.
	s.db.Exec(`INSERT INTO devices (id) VALUES ('tv'), ('tv2')`)
	s.WriteBatch([]QueryEntry{{TS: time.Now().Unix(), DeviceID: "tv2", Domain: "video.example.com", Status: "allowed", QType: "A"}}, nil)
	if err := s.MergeDevices("tv2", "tv"); err != nil {
		t.Fatal(err)
	}
	if sum("device_id='tv2'") != 0 || sum("device_id='tv' AND domain='video.example.com'") != 21 {
		t.Error("merge did not move the rollup")
	}

	// Retagging updates the rollup's reason.
	if _, err := s.TagDomains(map[string]string{"video.example.com": "seen:smarttv"}, since); err != nil {
		t.Fatal(err)
	}
	if sum("domain='video.example.com' AND reason=''") != 0 || sum("domain='video.example.com' AND reason='seen:smarttv'") != 21 {
		t.Error("retag did not update the rollup")
	}

	// Pruning drops whole old days from the rollup.
	if _, err := s.PruneQueries(36 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if sum("day<?", dayStart(time.Now().Add(-36*time.Hour).Unix())) != 0 {
		t.Error("prune left old rollup days")
	}

	// Backfill rebuilds an identical rollup from the raw log.
	type row struct {
		day                      int64
		dev, dom, reason, status string
		n, first, last           int64
	}
	snapshot := func() []string {
		var out []string
		rows, _ := s.db.Query(`SELECT day, device_id, domain, reason, status, lookups, first_ts, last_ts FROM daily ORDER BY 1,2,3,4,5`)
		defer rows.Close()
		for rows.Next() {
			var r row
			rows.Scan(&r.day, &r.dev, &r.dom, &r.reason, &r.status, &r.n, &r.first, &r.last)
			out = append(out, fmt.Sprint(r))
		}
		return out
	}
	before := snapshot()
	s.db.Exec(`DELETE FROM daily`)
	if err := s.backfillDaily(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("backfill differs from incremental rollup:\n%v\n%v", before, after)
	}
}
