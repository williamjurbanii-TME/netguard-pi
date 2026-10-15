// Package filter decides, per device, whether a DNS name is allowed.
package filter

import (
	"strings"
	"sync"

	"netguard/internal/store"
)

type Action int

const (
	Allow Action = iota
	Block
	NXDomain
	Rewrite // answer with a CNAME to Decision.Target (SafeSearch)
)

type Decision struct {
	Action Action
	Reason string
	Target string
}

type compiledGroup struct {
	group   store.Group
	catMask uint64
	block   map[string]string // domain -> reason
	allow   map[string]bool
}

// Engine holds an in-memory snapshot of groups and devices so the DNS hot
// path never touches the database.
type Engine struct {
	store *store.Store
	lists *ListManager

	// OnChange, if set, runs after every Reload (e.g. to refresh DHCP reservations).
	OnChange func()

	mu      sync.RWMutex
	groups  map[int64]*compiledGroup
	devices map[string]*store.Device // device ID -> device
	aliases map[string]string        // identifier (MAC, ip:<addr>, local) -> device ID
	byName  map[string][]string      // local DNS label -> device IDs using it

	ipMu  sync.Mutex
	devIP map[string]string // device ID -> last IPv4
	ipDev map[string]string // IPv4 -> device ID
}

func NewEngine(s *store.Store, lists *ListManager) (*Engine, error) {
	e := &Engine{store: s, lists: lists}
	return e, e.Reload()
}

// Reload re-reads groups, devices and aliases from the store. Call after any
// edit. The write lock is held throughout so a device created concurrently by
// EnsureDevice can't be lost.
func (e *Engine) Reload() error {
	e.mu.Lock()
	err := e.reloadLocked()
	e.mu.Unlock()
	if err == nil && e.OnChange != nil {
		e.OnChange()
	}
	return err
}

func (e *Engine) reloadLocked() error {
	groups, err := e.store.ListGroups()
	if err != nil {
		return err
	}
	devices, err := e.store.ListDevices() // most recently seen first
	if err != nil {
		return err
	}
	aliases, err := e.store.ListAliases()
	if err != nil {
		return err
	}
	cg := make(map[int64]*compiledGroup, len(groups))
	for _, g := range groups {
		cg[g.ID] = e.compile(g)
	}
	dm := make(map[string]*store.Device, len(devices))
	for i := range devices {
		dm[devices[i].ID] = &devices[i]
	}
	am := make(map[string]string, len(aliases))
	for _, a := range aliases {
		if dm[a.DeviceID] != nil {
			am[a.Identifier] = a.DeviceID
		}
	}
	devIP, ipDev := map[string]string{}, map[string]string{}
	for _, d := range devices {
		if d.LastIPv4 != "" {
			devIP[d.ID] = d.LastIPv4
			if _, taken := ipDev[d.LastIPv4]; !taken {
				ipDev[d.LastIPv4] = d.ID
			}
		}
	}
	e.groups, e.devices, e.aliases = cg, dm, am
	e.byName = buildNames(devices)
	e.ipMu.Lock()
	e.devIP, e.ipDev = devIP, ipDev
	e.ipMu.Unlock()
	return nil
}

func (e *Engine) compile(g store.Group) *compiledGroup {
	c := &compiledGroup{
		group:   g,
		catMask: e.lists.Mask(g.Categories),
		block:   map[string]string{},
		allow:   map[string]bool{},
	}
	if g.BlockDoH {
		for _, d := range dohDomains {
			c.block[d] = "doh"
		}
	}
	for _, svc := range g.Services {
		for _, d := range Services[svc] {
			c.block[d] = "service:" + svc
		}
	}
	for _, d := range g.CustomBlock {
		if d = NormalizeDomain(d); d != "" {
			c.block[d] = "custom"
		}
	}
	for _, d := range g.CustomAllow {
		if d = NormalizeDomain(d); d != "" {
			c.allow[d] = true
		}
	}
	return c
}

// walk calls fn for domain and each of its parents until fn returns true.
func walk(domain string, fn func(string) bool) bool {
	for d := domain; ; {
		if fn(d) {
			return true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return false
		}
		d = d[i+1:]
	}
}

// Check decides what to do with a query for domain from deviceID.
func (e *Engine) Check(deviceID, domain string) Decision {
	e.mu.RLock()
	gid, paused := store.DefaultGroupID, false
	if d := e.devices[deviceID]; d != nil {
		gid, paused = d.GroupID, d.Paused
	}
	g := e.groups[gid]
	if g == nil {
		g = e.groups[store.DefaultGroupID]
	}
	e.mu.RUnlock()

	if paused {
		return Decision{Action: Block, Reason: "paused"}
	}
	if g == nil {
		return Decision{Action: Allow}
	}
	// The allow list overrides blocking but not SafeSearch, so "allow YouTube"
	// on a SafeSearch group still means YouTube in Restricted Mode.
	allowed := walk(domain, func(d string) bool { return g.allow[d] })
	listMask := e.lists.Lookup(domain)
	if !allowed {
		if g.group.BlockDoH {
			if domain == firefoxCanary {
				return Decision{Action: NXDomain, Reason: "doh-canary"}
			}
			if privateRelay[domain] {
				return Decision{Action: NXDomain, Reason: "private-relay"}
			}
		}
		var reason string
		if walk(domain, func(d string) bool { reason = g.block[d]; return reason != "" }) {
			return Decision{Action: Block, Reason: reason}
		}
		if m := listMask & g.catMask; m != 0 {
			return Decision{Action: Block, Reason: "list:" + e.lists.CategoryFor(m)}
		}
	}
	if g.group.SafeSearch {
		if t := safeSearchTarget(domain); t != "" {
			return Decision{Action: Rewrite, Reason: "safesearch", Target: t}
		}
	}
	if allowed {
		return Decision{Action: Allow, Reason: "allowlist"}
	}
	// Not blocked for this group, but on an ad/tracking list: tag it so the
	// Ads dashboard can show what e.g. a TV phones home to before you block it.
	if m := listMask & e.lists.Mask(AdCategories); m != 0 {
		return Decision{Action: Allow, Reason: "seen:" + e.lists.CategoryFor(m)}
	}
	return Decision{Action: Allow}
}

// AdCategories are the blocklist categories counted as ads/tracking.
var AdCategories = []string{"ads", "smarttv", "tracking"}

// AdReasons returns the query-log reasons that count as ad/tracking traffic.
func AdReasons() (blocked, seen []string) {
	for _, c := range AdCategories {
		blocked = append(blocked, "list:"+c)
		seen = append(seen, "seen:"+c)
	}
	return blocked, seen
}
