package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"netguard/internal/config"
	"netguard/internal/filter"
	"netguard/internal/store"
)

func TestSitesCollapsesAndFlags(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().Unix()
	e := func(domain, status, reason string) store.QueryEntry {
		return store.QueryEntry{TS: now - 30, DeviceID: "pc", ClientIP: "192.168.1.10", Domain: domain, QType: "A", Status: status, Reason: reason}
	}
	st.WriteBatch([]store.QueryEntry{
		e("adultsite.example.com", "allowed", "seen:adult"),
		e("cdn.adultsite.example.com", "allowed", ""),
		e("collector.adultsite.example.com", "blocked", "list:ads"),
		e("beacons.gvt2.com", "blocked", "list:ads"),
		e("beacons2.gvt2.com", "blocked", "list:ads"),
		e("wpad.lan", "local", ""),
		e("news.example.org", "allowed", ""),
	}, nil)
	engine, _ := filter.NewEngine(st, filter.NewListManager(t.TempDir(), nil))
	s := New(config.WebConfig{}, "test", "lan", 30, st, engine, filter.NewListManager(t.TempDir(), nil), nil)

	get := func(query string) []siteView {
		rec := httptest.NewRecorder()
		s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sites?"+query, nil))
		var out []siteView
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v (%s)", query, err, rec.Body.String())
		}
		return out
	}
	bySite := func(rows []siteView) map[string]siteView {
		m := map[string]siteView{}
		for _, r := range rows {
			m[r.Site] = r
		}
		return m
	}

	all := bySite(get("hours=1"))
	a, ok := all["example.com"]
	if !ok || a.Lookups != 3 || a.Blocked != 1 || len(a.Flags) != 1 || a.Flags[0] != "adult" || a.Background {
		t.Errorf("adult site row = %+v", a)
	}
	if g := all["gvt2.com"]; g.Lookups != 2 || !g.Background {
		t.Errorf("beacons should collapse into one background row: %+v", g)
	}
	if _, ok := all["wpad.lan"]; ok {
		t.Error("local answers should not appear")
	}

	if rows := bySite(get("hours=1&hide_background=1")); rows["gvt2.com"].Site != "" || rows["example.org"].Site == "" {
		t.Errorf("hide_background should drop pure ad rows only: %v", rows)
	}
	if rows := get("hours=1&flagged=1"); len(rows) != 1 || rows[0].Site != "example.com" {
		t.Errorf("flagged=1 should return only the adult site: %+v", rows)
	}
}
