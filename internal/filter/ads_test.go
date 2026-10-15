package filter

import (
	"testing"
	"time"

	"netguard/internal/store"
)

func TestAdTaggingAndPrivateRelay(t *testing.T) {
	e, st := newTestEngine(t)
	// A group that blocks nothing: ad lookups are allowed but tagged.
	open := &store.Group{Name: "Open"}
	st.SaveGroup(open)
	id, _ := e.EnsureDevice("aa:00:00:00:00:07", "192.168.1.7")
	d, _ := st.GetDevice(id)
	d.GroupID = open.ID
	st.UpdateDevice(d)
	e.Reload()

	if got := e.Check(id, "doubleclick.net"); got.Action != Allow || got.Reason != "seen:ads" {
		t.Errorf("open group: %+v, want Allow seen:ads", got)
	}
	if got := e.Check("unknown", "doubleclick.net"); got.Action != Block || got.Reason != "list:ads" {
		t.Errorf("default group: %+v, want Block list:ads", got)
	}
	if got := e.Check(id, "badsite.com"); got.Action != Allow || got.Reason != "seen:adult" {
		t.Errorf("allowed adult site should be tagged seen:adult, got %+v", got)
	}
	// History logged before tagging gets flagged by RetagHistory.
	now := time.Now().Unix()
	st.WriteBatch([]store.QueryEntry{
		{TS: now - 60, DeviceID: id, ClientIP: "192.168.1.7", Domain: "badsite.com", QType: "A", Status: "allowed"},
		{TS: now - 60, DeviceID: id, ClientIP: "192.168.1.7", Domain: "example.org", QType: "A", Status: "allowed"},
	}, nil)
	if n, err := e.RetagHistory(time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("RetagHistory = %d, %v; want 1 row tagged", n, err)
	}
	if rows, _ := st.RecentQueries(store.QueryFilter{Search: "badsite"}); len(rows) != 1 || rows[0].Reason != "seen:adult" {
		t.Errorf("retagged row = %+v", rows)
	}

	// Kids (group 2) has BlockDoH: Private Relay is switched off for the network.
	d.GroupID = 2
	st.UpdateDevice(d)
	e.Reload()
	if got := e.Check(id, "mask.icloud.com"); got.Action != NXDomain {
		t.Errorf("mask.icloud.com: %+v, want NXDOMAIN", got)
	}
}
