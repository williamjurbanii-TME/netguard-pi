package web

import (
	"testing"

	"netguard/internal/store"
)

func TestGroupCompanies(t *testing.T) {
	rows := []store.AdDomainRow{
		{Domain: "ads.samsung.com", DeviceID: "tv", AdCount: store.AdCount{Blocked: 50}},
		{Domain: "config.samsung.com", DeviceID: "tv", AdCount: store.AdCount{Allowed: 20}},
		{Domain: "log.samsungacr.com", DeviceID: "tv", AdCount: store.AdCount{Blocked: 30}},
		{Domain: "doubleclick.net", DeviceID: "phone", AdCount: store.AdCount{Blocked: 10}},
		{Domain: "ads.bbc.co.uk", DeviceID: "phone", AdCount: store.AdCount{Blocked: 5}},
	}
	got := groupCompanies(rows, 10)
	if len(got) != 4 {
		t.Fatalf("got %d companies: %+v", len(got), got)
	}
	if got[0].Company != "samsung.com" || got[0].Blocked != 50 || got[0].Allowed != 20 || len(got[0].Domains) != 2 {
		t.Errorf("first = %+v", got[0])
	}
	if got[3].Company != "bbc.co.uk" {
		t.Errorf("public-suffix grouping failed: %+v", got[3])
	}
}
