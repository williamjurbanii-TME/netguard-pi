// Package web serves the dashboard and its JSON API.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"netguard/internal/config"
	"netguard/internal/dhcpserver"
	"netguard/internal/filter"
	"netguard/internal/names"
	"netguard/internal/store"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg           config.WebConfig
	version       string
	domain        string
	retentionDays int
	store         *store.Store
	engine        *filter.Engine
	lists         *filter.ListManager
	dhcp          *dhcpserver.Server // nil when DHCP is disabled
	started       time.Time
	srv           *http.Server
}

func New(cfg config.WebConfig, version, localDomain string, retentionDays int, st *store.Store, engine *filter.Engine, lists *filter.ListManager, dhcp *dhcpserver.Server) *Server {
	s := &Server{cfg: cfg, version: version, domain: localDomain, retentionDays: retentionDays, store: st, engine: engine, lists: lists, dhcp: dhcp, started: time.Now()}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache") // embedded files have no mtime; always revalidate
		files.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /api/summary", s.summary)
	mux.HandleFunc("GET /api/catalog", s.catalog)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("PUT /api/devices/{id}", s.updateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.deleteDevice)
	mux.HandleFunc("GET /api/devices/{id}/stats", s.deviceStats)
	mux.HandleFunc("POST /api/devices/{id}/merge", s.mergeDevice)
	mux.HandleFunc("DELETE /api/identifiers/{identifier}", s.detachIdentifier)
	mux.HandleFunc("GET /api/groups", s.listGroups)
	mux.HandleFunc("POST /api/groups", s.saveGroup)
	mux.HandleFunc("PUT /api/groups/{id}", s.saveGroup)
	mux.HandleFunc("DELETE /api/groups/{id}", s.deleteGroup)
	mux.HandleFunc("POST /api/groups/{id}/rule", s.addRule)
	mux.HandleFunc("GET /api/queries", s.queries)
	mux.HandleFunc("GET /api/ads", s.ads)
	mux.HandleFunc("GET /api/sites", s.sites)
	mux.HandleFunc("GET /api/leases", s.leases)
	mux.HandleFunc("POST /api/lists/refresh", s.refreshLists)

	s.srv = &http.Server{Addr: cfg.Listen, Handler: s.auth(mux), ReadHeaderTimeout: 10 * time.Second}
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	if s.cfg.Password == "" {
		log.Printf("web: WARNING no password set - anyone on the network can change filtering rules")
	}
	log.Printf("web: dashboard on http://%s", s.cfg.Listen)
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("web: %v", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) { _ = s.srv.Shutdown(ctx) }

func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(s.cfg.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NetGuard"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func since(r *http.Request) int64 {
	h, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if h <= 0 || h > 24*90 {
		h = 24
	}
	return time.Now().Add(-time.Duration(h) * time.Hour).Unix()
}

// ---- overview ----

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	from := since(r)
	counts, err := s.store.CountsByDevice(from)
	if err != nil {
		fail(w, 500, err)
		return
	}
	var total, blocked int64
	for _, c := range counts {
		total += c.Total
		blocked += c.Blocked
	}
	hourly, _ := s.store.Hourly("", from)
	topBlocked, _ := s.store.TopDomains("", true, from, 10)
	topAllowed, _ := s.store.TopDomains("", false, from, 10)
	lists, domains := s.lists.Status()
	devices, _ := s.store.ListDevices()
	active := 0
	for _, d := range devices {
		if time.Now().Unix()-d.LastSeen < 15*60 {
			active++
		}
	}
	writeJSON(w, map[string]any{
		"queries": total, "blocked": blocked, "devices": len(devices), "active_devices": active,
		"hourly": hourly, "top_blocked": topBlocked, "top_allowed": topAllowed,
		"lists": lists, "list_domains": domains, "dhcp_enabled": s.dhcp != nil, "local_domain": s.domain,
		"uptime": int64(time.Since(s.started).Seconds()), "history": s.store.History(), "retention_days": s.retentionDays, "version": s.version,
	})
}

// ---- ads & tracking ----

type adDevice struct {
	ID      string `json:"id"`
	Queries int64  `json:"queries"`
	store.AdCount
}

type adCompany struct {
	Company string   `json:"company"` // registrable domain, e.g. samsungads.com
	Domains []string `json:"domains"` // busiest hostnames under it
	Devices []string `json:"devices"` // busiest devices talking to it
	store.AdCount
}

func (s *Server) ads(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*90 {
		hours = 24
	}
	blocked, seen := filter.AdReasons()
	f := store.AdFilter{Blocked: blocked, Seen: seen, Since: time.Now().Add(-time.Duration(hours) * time.Hour).Unix(),
		DeviceID: r.URL.Query().Get("device")}

	bucket, offset := int64(3600), int64(0)
	if hours > 48 {
		_, off := time.Now().Zone()
		bucket, offset = 86400, int64(off) // local calendar days
	}
	series, err := s.store.AdSeries(f, bucket, offset)
	if err != nil {
		fail(w, 500, err)
		return
	}
	byDev, _ := s.store.AdByDevice(f)
	totals, _ := s.store.CountsByDevice(f.Since)
	var total store.AdCount
	var allQueries int64
	devices := []adDevice{}
	for id, c := range byDev {
		total.Blocked += c.Blocked
		total.Allowed += c.Allowed
		devices = append(devices, adDevice{ID: id, Queries: totals[id].Total, AdCount: c})
	}
	for id, c := range totals {
		if f.DeviceID == "" || id == f.DeviceID {
			allQueries += c.Total
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Blocked+devices[i].Allowed > devices[j].Blocked+devices[j].Allowed
	})

	rows, _ := s.store.AdDomains(f, 3000)
	writeJSON(w, map[string]any{
		"hours": hours, "bucket": bucket, "series": series, "queries": allQueries,
		"blocked": total.Blocked, "allowed": total.Allowed,
		"devices": devices, "companies": groupCompanies(rows, 30),
	})
}

// ---- query log: collapsed "sites" view ----

type siteView struct {
	Site       string   `json:"site"` // registrable domain, e.g. example.com
	DeviceID   string   `json:"device_id"`
	Lookups    int64    `json:"lookups"`
	Blocked    int64    `json:"blocked"`
	First      int64    `json:"first"`
	Last       int64    `json:"last"`
	Hosts      []string `json:"hosts"`      // busiest hostnames under the site
	Flags      []string `json:"flags"`      // sensitive categories (adult, gambling, malware)
	Reasons    []string `json:"reasons"`    // distinct block/tag reasons
	Background bool     `json:"background"` // every lookup was ad/tracking traffic
}

func siteOf(domain string) string {
	if s, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return s
	}
	return domain
}

// sites collapses the query log into one row per (site, device), so a visit
// to a website shows as one line instead of dozens of lookups.
func (s *Server) sites(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hours, _ := strconv.Atoi(q.Get("hours"))
	if hours <= 0 || hours > 24*90 {
		hours = 24
	}
	rows, err := s.store.SiteRows(store.QueryFilter{DeviceID: q.Get("device"), Search: q.Get("search")},
		time.Now().Add(-time.Duration(hours)*time.Hour).Unix(), 20000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	adBlocked, adSeen := filter.AdReasons()
	adReason := map[string]bool{}
	for _, r := range append(adBlocked, adSeen...) {
		adReason[r] = true
	}
	flagReason := map[string]string{}
	for _, c := range filter.FlagCategories {
		flagReason["list:"+c], flagReason["seen:"+c] = c, c
	}

	type acc struct {
		siteView
		hosts          map[string]int64
		reasons, flags map[string]bool
		adLookups      int64
	}
	byKey := map[string]*acc{}
	var order []*acc
	for _, row := range rows {
		site := siteOf(row.Domain)
		key := site + "|" + row.DeviceID
		a := byKey[key]
		if a == nil {
			a = &acc{siteView: siteView{Site: site, DeviceID: row.DeviceID, First: row.First},
				hosts: map[string]int64{}, reasons: map[string]bool{}, flags: map[string]bool{}}
			byKey[key] = a
			order = append(order, a)
		}
		a.Lookups += row.Lookups
		a.Blocked += row.Blocked
		a.First = min(a.First, row.First)
		a.Last = max(a.Last, row.Last)
		a.hosts[row.Domain] += row.Lookups
		if row.Reason != "" {
			a.reasons[row.Reason] = true
		}
		if c := flagReason[row.Reason]; c != "" {
			a.flags[c] = true
		}
		if adReason[row.Reason] {
			a.adLookups += row.Lookups
		}
	}

	onlyFlagged, hideBackground := q.Get("flagged") == "1", q.Get("hide_background") == "1"
	out := []siteView{}
	for _, a := range order {
		a.Background = a.adLookups == a.Lookups
		if (onlyFlagged && len(a.flags) == 0) || (hideBackground && a.Background) {
			continue
		}
		a.Hosts = topKeys(a.hosts, 3)
		a.Reasons = sortedKeys(a.reasons)
		a.Flags = sortedKeys(a.flags)
		out = append(out, a.siteView)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	if len(out) > 400 {
		out = out[:400]
	}
	writeJSON(w, out)
}

func topKeys(m map[string]int64, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// groupCompanies folds hostnames into their registrable domain
// (ads.samsung.com, log-config.samsungacr.com -> samsung.com, samsungacr.com).
func groupCompanies(rows []store.AdDomainRow, limit int) []adCompany {
	type acc struct {
		adCompany
		domains, devices map[string]int64
	}
	byCo := map[string]*acc{}
	for _, r := range rows {
		co, err := publicsuffix.EffectiveTLDPlusOne(r.Domain)
		if err != nil {
			co = r.Domain
		}
		a := byCo[co]
		if a == nil {
			a = &acc{adCompany: adCompany{Company: co}, domains: map[string]int64{}, devices: map[string]int64{}}
			byCo[co] = a
		}
		n := r.Blocked + r.Allowed
		a.Blocked += r.Blocked
		a.Allowed += r.Allowed
		a.domains[r.Domain] += n
		a.devices[r.DeviceID] += n
	}
	top := func(m map[string]int64, n int) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
		if len(keys) > n {
			keys = keys[:n]
		}
		return keys
	}
	out := make([]adCompany, 0, len(byCo))
	for _, a := range byCo {
		a.Domains, a.Devices = top(a.domains, 3), top(a.devices, 3)
		out = append(out, a.adCompany)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Blocked+out[i].Allowed > out[j].Blocked+out[j].Allowed })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"categories": s.lists.Categories(), "services": filter.ServiceNames()})
}

// ---- devices ----

type identifierView struct {
	store.Alias
	Private bool `json:"private"` // randomized (private Wi-Fi) MAC
}

type deviceView struct {
	store.Device
	GroupName   string           `json:"group_name"`
	Queries     int64            `json:"queries"`
	Blocked     int64            `json:"blocked"`
	Online      bool             `json:"online"`
	LocalName   string           `json:"local_name"`   // e.g. "emmas-iphone.lan"
	Identifiers []identifierView `json:"identifiers"`  // MACs / IPs this device has used
	PrivateMAC  bool             `json:"private_mac"`  // uses a private Wi-Fi address
	SameNameAs  []string         `json:"same_name_as"` // other devices with the same distinctive name
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices()
	if err != nil {
		fail(w, 500, err)
		return
	}
	aliases, _ := s.store.ListAliases()
	counts, _ := s.store.CountsByDevice(since(r))
	groups, _ := s.store.ListGroups()
	groupNames := map[int64]string{}
	for _, g := range groups {
		groupNames[g.ID] = g.Name
	}
	idents := map[string][]identifierView{}
	for _, a := range aliases {
		idents[a.DeviceID] = append(idents[a.DeviceID], identifierView{Alias: a, Private: names.IsRandomMAC(a.Identifier)})
	}
	byHostname := map[string][]string{}
	for _, d := range devices {
		if !names.IsGeneric(d.Hostname) {
			k := strings.ToLower(d.Hostname)
			byHostname[k] = append(byHostname[k], d.ID)
		}
	}
	out := make([]deviceView, 0, len(devices))
	now := time.Now().Unix()
	for _, d := range devices {
		c := counts[d.ID]
		v := deviceView{Device: d, GroupName: groupNames[d.GroupID], Queries: c.Total, Blocked: c.Blocked,
			Online: now-d.LastSeen < 10*60, Identifiers: idents[d.ID], SameNameAs: []string{}}
		if v.Identifiers == nil {
			v.Identifiers = []identifierView{}
		}
		for _, iv := range v.Identifiers {
			v.PrivateMAC = v.PrivateMAC || iv.Private
		}
		if label := s.engine.LocalName(d.ID); label != "" && s.domain != "" {
			v.LocalName = label + "." + s.domain
		}
		if !names.IsGeneric(d.Hostname) {
			for _, other := range byHostname[strings.ToLower(d.Hostname)] {
				if other != d.ID {
					v.SameNameAs = append(v.SameNameAs, other)
				}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

func (s *Server) mergeDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Into string `json:"into"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.engine.Merge(r.PathValue("id"), in.Into); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) detachIdentifier(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Detach(r.PathValue("identifier")); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reload() {
	if err := s.engine.Reload(); err != nil {
		log.Printf("reload: %v", err)
	}
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.store.GetDevice(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	var in struct {
		Name     *string `json:"name"`
		GroupID  *int64  `json:"group_id"`
		Paused   *bool   `json:"paused"`
		StaticIP *string `json:"static_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Name != nil {
		d.Name = strings.TrimSpace(*in.Name)
	}
	if in.GroupID != nil {
		if _, err := s.store.GetGroup(*in.GroupID); err != nil {
			http.Error(w, "unknown group", 400)
			return
		}
		d.GroupID = *in.GroupID
	}
	if in.Paused != nil {
		d.Paused = *in.Paused
	}
	if in.StaticIP != nil {
		ip := strings.TrimSpace(*in.StaticIP)
		if ip != "" && net.ParseIP(ip).To4() == nil {
			http.Error(w, "static_ip must be an IPv4 address", 400)
			return
		}
		d.StaticIP = ip
	}
	if err := s.store.UpdateDevice(d); err != nil {
		fail(w, 500, err)
		return
	}
	s.reload()
	writeJSON(w, d)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDevice(r.PathValue("id")); err != nil {
		fail(w, 500, err)
		return
	}
	s.reload()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deviceStats(w http.ResponseWriter, r *http.Request) {
	id, from := r.PathValue("id"), since(r)
	hourly, err := s.store.Hourly(id, from)
	if err != nil {
		fail(w, 500, err)
		return
	}
	topAllowed, _ := s.store.TopDomains(id, false, from, 20)
	topBlocked, _ := s.store.TopDomains(id, true, from, 20)
	writeJSON(w, map[string]any{"hourly": hourly, "top_allowed": topAllowed, "top_blocked": topBlocked})
}

// ---- groups ----

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, groups)
}

func cleanDomains(in []string) []string {
	out := []string{}
	for _, d := range in {
		if d = filter.NormalizeDomain(d); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request) {
	var g store.Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		fail(w, 400, err)
		return
	}
	g.ID = 0
	if idStr := r.PathValue("id"); idStr != "" {
		g.ID, _ = strconv.ParseInt(idStr, 10, 64)
		if _, err := s.store.GetGroup(g.ID); err != nil {
			fail(w, 404, err)
			return
		}
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	g.CustomBlock, g.CustomAllow = cleanDomains(g.CustomBlock), cleanDomains(g.CustomAllow)
	if _, err := s.store.SaveGroup(&g); err != nil {
		fail(w, 400, err)
		return
	}
	s.reload()
	writeJSON(w, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.store.DeleteGroup(id); err != nil {
		fail(w, 400, err)
		return
	}
	s.reload()
	w.WriteHeader(http.StatusNoContent)
}

// addRule adds one domain to a group's block or allow list (from the query log).
func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	g, err := s.store.GetGroup(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	var in struct{ Domain, Action string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err)
		return
	}
	d := filter.NormalizeDomain(in.Domain)
	if d == "" {
		http.Error(w, "invalid domain", 400)
		return
	}
	remove := func(list []string) []string {
		return slices.DeleteFunc(list, func(x string) bool { return x == d })
	}
	switch in.Action {
	case "block":
		g.CustomAllow = remove(g.CustomAllow)
		g.CustomBlock = append(remove(g.CustomBlock), d)
	case "allow":
		g.CustomBlock = remove(g.CustomBlock)
		g.CustomAllow = append(remove(g.CustomAllow), d)
	default:
		http.Error(w, "action must be block or allow", 400)
		return
	}
	if _, err := s.store.SaveGroup(g); err != nil {
		fail(w, 500, err)
		return
	}
	s.reload()
	writeJSON(w, g)
}

// ---- log, leases, lists ----

func (s *Server) queries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := s.store.RecentQueries(store.QueryFilter{
		DeviceID: q.Get("device"), Status: q.Get("status"), Search: q.Get("search"), Limit: limit,
	})
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) leases(w http.ResponseWriter, r *http.Request) {
	if s.dhcp == nil {
		writeJSON(w, []store.Lease{})
		return
	}
	writeJSON(w, s.dhcp.Leases())
}

func (s *Server) refreshLists(w http.ResponseWriter, r *http.Request) {
	go s.lists.Refresh(true)
	w.WriteHeader(http.StatusAccepted)
}
