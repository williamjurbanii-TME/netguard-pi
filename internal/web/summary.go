package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"netguard/internal/filter"
	"netguard/internal/store"
)

// A device's "report card": which apps it used or tried, flagged sites, and
// other sites visited, without timestamps or background noise.

type appUse struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Category      string `json:"category"`
	Lookups       int64  `json:"lookups"`
	Blocked       int64  `json:"blocked"`
	Restricted    int64  `json:"restricted"`      // answered with SafeSearch / Restricted Mode
	BlockedByRule bool   `json:"blocked_by_rule"` // the device's group blocks this service
}

type siteUse struct {
	Site     string `json:"site"`
	Category string `json:"category,omitempty"` // for flagged sites: adult, gambling, malware
	Lookups  int64  `json:"lookups"`
	Blocked  int64  `json:"blocked"`
}

type deviceSummary struct {
	Hours      int       `json:"hours"`
	GroupID    int64     `json:"group_id"`
	GroupName  string    `json:"group_name"`
	Paused     bool      `json:"paused"`
	Lookups    int64     `json:"lookups"`
	Blocked    int64     `json:"blocked"`
	Apps       []appUse  `json:"apps"`
	Flagged    []siteUse `json:"flagged"`
	Sites      []siteUse `json:"sites"`
	Background int64     `json:"background"` // lookups hidden as ads, trackers or system traffic
}

func (s *Server) deviceSummary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dev, err := s.store.GetDevice(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*90 {
		hours = 24
	}
	rows, err := s.store.DeviceActivity(id, time.Now().Add(-time.Duration(hours)*time.Hour).Unix())
	if err != nil {
		fail(w, 500, err)
		return
	}
	group, _ := s.store.GetGroup(dev.GroupID)
	if group == nil {
		group = &store.Group{ID: dev.GroupID}
	}
	writeJSON(w, summarize(rows, hours, dev, group))
}

func summarize(rows []store.DomainActivity, hours int, dev *store.Device, group *store.Group) deviceSummary {
	adBlocked, adSeen := filter.AdReasons()
	isAd := map[string]bool{}
	for _, r := range append(adBlocked, adSeen...) {
		isAd[r] = true
	}
	flagCat := map[string]string{}
	for _, c := range filter.FlagCategories {
		flagCat["list:"+c], flagCat["seen:"+c] = c, c
	}

	sum := deviceSummary{Hours: hours, GroupID: group.ID, GroupName: group.Name, Paused: dev.Paused,
		Apps: []appUse{}, Flagged: []siteUse{}, Sites: []siteUse{}}
	apps := map[string]*appUse{}
	flagged := map[string]*siteUse{}
	sites := map[string]*siteUse{}
	for _, r := range rows {
		sum.Lookups += r.Lookups
		sum.Blocked += r.Blocked
		site := siteOf(r.Domain)
		switch key := filter.ServiceFor(r.Domain); {
		case key != "":
			a := apps[key]
			if a == nil {
				svc := filter.Services[key]
				a = &appUse{Key: key, Label: svc.Label, Category: svc.Category, BlockedByRule: slices.Contains(group.Services, key)}
				apps[key] = a
			}
			a.Lookups += r.Lookups
			a.Blocked += r.Blocked
			a.Restricted += r.Restrict
		case flagCat[r.Reason] != "":
			f := flagged[site]
			if f == nil {
				f = &siteUse{Site: site, Category: flagCat[r.Reason]}
				flagged[site] = f
			}
			f.Lookups += r.Lookups
			f.Blocked += r.Blocked
		case isAd[r.Reason] || filter.IsBackground(site) || r.Reason == "doh" || r.Reason == "doh-canary" || r.Reason == "private-relay":
			sum.Background += r.Lookups
		default:
			u := sites[site]
			if u == nil {
				u = &siteUse{Site: site}
				sites[site] = u
			}
			u.Lookups += r.Lookups
			u.Blocked += r.Blocked
		}
	}

	catRank := map[string]int{}
	for i, c := range filter.ServiceCategories {
		catRank[c] = i
	}
	for _, a := range apps {
		sum.Apps = append(sum.Apps, *a)
	}
	sort.Slice(sum.Apps, func(i, j int) bool {
		a, b := sum.Apps[i], sum.Apps[j]
		if catRank[a.Category] != catRank[b.Category] {
			return catRank[a.Category] < catRank[b.Category]
		}
		return a.Lookups > b.Lookups
	})
	byLookups := func(m map[string]*siteUse, limit int) []siteUse {
		out := []siteUse{}
		for _, v := range m {
			out = append(out, *v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Lookups > out[j].Lookups })
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	sum.Flagged = byLookups(flagged, 50)
	sum.Sites = byLookups(sites, 40)
	return sum
}

// setGroupService blocks or unblocks one service for a group (from the device summary).
func (s *Server) setGroupService(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	g, err := s.store.GetGroup(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	var in struct {
		Service string `json:"service"`
		Block   bool   `json:"block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err)
		return
	}
	if _, ok := filter.Services[in.Service]; !ok {
		fail(w, 400, fmt.Errorf("unknown service %q", in.Service))
		return
	}
	g.Services = slices.DeleteFunc(g.Services, func(x string) bool { return strings.EqualFold(x, in.Service) })
	if in.Block {
		g.Services = append(g.Services, in.Service)
	}
	if _, err := s.store.SaveGroup(g); err != nil {
		fail(w, 500, err)
		return
	}
	s.reload()
	writeJSON(w, g)
}
