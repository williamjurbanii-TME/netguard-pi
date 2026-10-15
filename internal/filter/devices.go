package filter

import (
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"netguard/internal/names"
	"netguard/internal/store"
)

// How much each hostname source is trusted. A lower-ranked source never
// overwrites a name from a higher-ranked one. DHCP wins: it is the name the
// device chose to announce (mDNS may return e.g. a Roku's serial number).
var sourceRank = map[string]int{"dhcp": 3, "mdns": 2, "netbios": 1}

const placeholderAdoptWindow = 24 * 60 * 60 // seconds
const autoMergeWindow = 7 * 24 * 60 * 60    // seconds

func isMAC(identifier string) bool {
	_, err := net.ParseMAC(identifier)
	return err == nil
}

// unconfigured reports whether the user has never customised d, so it is
// safe to fold it into another device automatically.
func unconfigured(d *store.Device) bool {
	return d.Name == "" && d.GroupID == store.DefaultGroupID && !d.Paused && d.StaticIP == ""
}

// EnsureDevice returns the device for a network identifier, creating it the
// first time the identifier is seen. Cheap when the identifier is known.
func (e *Engine) EnsureDevice(identifier, ip string) (deviceID string, isNew bool) {
	e.mu.RLock()
	id, ok := e.aliases[identifier]
	e.mu.RUnlock()
	if ok {
		return id, false
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if id, ok := e.aliases[identifier]; ok {
		return id, false
	}
	now := time.Now().Unix()

	// A device's first query can arrive before its MAC shows up in the
	// neighbour table, leaving an "ip:<addr>" placeholder. Adopt it.
	if isMAC(identifier) && ip != "" {
		placeholder := "ip:" + ip
		if pid, ok := e.aliases[placeholder]; ok {
			if p := e.devices[pid]; p != nil && unconfigured(p) && now-p.FirstSeen < placeholderAdoptWindow {
				if err := e.store.SetAlias(identifier, pid, ip, now); err == nil {
					_ = e.store.DeleteAlias(placeholder)
					delete(e.aliases, placeholder)
					e.aliases[identifier] = pid
					return pid, false
				}
			}
		}
	}

	nd := &store.Device{ID: identifier, GroupID: store.DefaultGroupID, LastIP: ip, FirstSeen: now, LastSeen: now}
	if e.devices[nd.ID] != nil { // ID taken by a device this identifier was detached from
		nd.ID = identifier + "#" + strconv.FormatInt(now, 36)
	}
	if ipv4 := net.ParseIP(ip); ipv4 != nil && ipv4.To4() != nil {
		nd.LastIPv4 = ip
	}
	if err := e.store.InsertDevice(nd, identifier); err != nil {
		log.Printf("engine: insert device %s: %v", identifier, err)
	}
	e.devices[nd.ID] = nd
	e.aliases[identifier] = nd.ID
	return nd.ID, true
}

// Device returns a copy of the device, or nil.
func (e *Engine) Device(id string) *store.Device {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if d := e.devices[id]; d != nil {
		cp := *d
		return &cp
	}
	return nil
}

// SetHostname records a discovered name (from DHCP, mDNS or NetBIOS). If an
// unconfigured device turns out to have the same distinctive name as a known
// device, it is merged into it - this is how a phone that rotates its private
// MAC address keeps its group and rules.
func (e *Engine) SetHostname(deviceID, name, source string) {
	name = names.Clean(name)
	if name == "" {
		return
	}
	d := e.Device(deviceID)
	if d == nil || (d.Hostname == name && d.HostnameSource == source) {
		return
	}
	if d.Hostname != "" && sourceRank[source] < sourceRank[d.HostnameSource] {
		return
	}
	// Never replace a descriptive name with a generic one ("iPhone", "linux").
	if d.Hostname != "" && names.IsGeneric(name) && !names.IsGeneric(d.Hostname) {
		return
	}
	if err := e.store.SetHostname(deviceID, name, source); err != nil {
		log.Printf("engine: set hostname %s: %v", deviceID, err)
		return
	}
	d.Hostname = name
	if target := e.mergeTarget(d); target != "" {
		if err := e.store.MergeDevices(deviceID, target); err != nil {
			log.Printf("engine: auto-merge %s into %s: %v", deviceID, target, err)
		} else {
			log.Printf("engine: %q (%s) is a known device; merged into %s", name, deviceID, target)
		}
	}
	if err := e.Reload(); err != nil {
		log.Printf("engine: reload: %v", err)
	}
}

// mergeTarget picks the device that d should be folded into, or "".
// Only a device first seen with a private (randomized) MAC is merged
// automatically: that is the only way a device changes MAC on its own.
// Different devices with factory MACs can share a name (e.g. two mesh
// satellites both reporting the same model name), so those are only merged by hand.
func (e *Engine) mergeTarget(d *store.Device) string {
	firstMAC, _, _ := strings.Cut(d.ID, "#")
	if !names.IsRandomMAC(firstMAC) || names.IsGeneric(d.Hostname) || !unconfigured(d) ||
		time.Now().Unix()-d.FirstSeen > autoMergeWindow {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	match := ""
	for id, o := range e.devices {
		if id != d.ID && strings.EqualFold(o.Hostname, d.Hostname) {
			if match != "" {
				return "" // ambiguous: two other devices share the name
			}
			match = id
		}
	}
	return match
}

// Merge folds device src into dst (manual merge from the dashboard).
func (e *Engine) Merge(src, dst string) error {
	if e.Device(src) == nil || e.Device(dst) == nil {
		return fmt.Errorf("unknown device")
	}
	if err := e.store.MergeDevices(src, dst); err != nil {
		return err
	}
	return e.Reload()
}

// Detach removes an identifier from its device. The next query from that
// identifier creates a new device.
func (e *Engine) Detach(identifier string) error {
	e.mu.RLock()
	devID, ok := e.aliases[identifier]
	count := 0
	for _, d := range e.aliases {
		if d == devID {
			count++
		}
	}
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown identifier")
	}
	if count < 2 {
		return fmt.Errorf("this is the device's only address; use Forget instead")
	}
	if err := e.store.DeleteAlias(identifier); err != nil {
		return err
	}
	return e.Reload()
}

// TouchIP records the IPv4 address a device is currently using, for local
// name resolution. Called on the DNS hot path.
func (e *Engine) TouchIP(deviceID string, ip net.IP) {
	v4 := ip.To4()
	if v4 == nil || v4.IsLoopback() {
		return
	}
	s := v4.String()
	e.ipMu.Lock()
	if e.devIP[deviceID] != s {
		if old := e.devIP[deviceID]; old != "" && e.ipDev[old] == deviceID {
			delete(e.ipDev, old)
		}
		e.devIP[deviceID] = s
		e.ipDev[s] = deviceID
	}
	e.ipMu.Unlock()
}

// IPv4For returns the last IPv4 address seen for a device.
func (e *Engine) IPv4For(deviceID string) net.IP {
	e.ipMu.Lock()
	defer e.ipMu.Unlock()
	return net.ParseIP(e.devIP[deviceID])
}

func localName(d *store.Device) string {
	if d.Name != "" {
		return names.Slug(d.Name)
	}
	if !names.IsGeneric(d.Hostname) {
		return names.Slug(d.Hostname)
	}
	return ""
}

// buildNames maps DNS labels to the devices using them. A user-given name
// claims its label outright; a hostname shared by several devices (e.g. two
// mesh satellites both reporting the same model name) maps to all of them, and LookupName
// then only answers each device with its own address.
func buildNames(devices []store.Device) map[string][]string {
	out := map[string][]string{}
	named := map[string]bool{}
	for i := range devices {
		if d := &devices[i]; d.Name != "" {
			label := names.Slug(d.Name)
			if label != "" && !named[label] {
				out[label] = []string{d.ID}
				named[label] = true
			}
		}
	}
	for i := range devices {
		d := &devices[i]
		if d.Name != "" || names.IsGeneric(d.Hostname) {
			continue
		}
		if label := names.Slug(d.Hostname); label != "" && !named[label] {
			out[label] = append(out[label], d.ID)
		}
	}
	return out
}

// LocalName returns the DNS label for a device ("emmas-iphone"), or "".
func (e *Engine) LocalName(deviceID string) string {
	if d := e.Device(deviceID); d != nil {
		return localName(d)
	}
	return ""
}

// LookupName resolves a local DNS label to a device's current IPv4. asker is
// the querying device: if several devices share the label, the asker gets its
// own address and anyone else gets nothing rather than a guess.
func (e *Engine) LookupName(label, asker string) net.IP {
	e.mu.RLock()
	ids := e.byName[label]
	e.mu.RUnlock()
	switch {
	case slices.Contains(ids, asker):
		return e.IPv4For(asker)
	case len(ids) == 1:
		return e.IPv4For(ids[0])
	}
	return nil
}

// NameForIP returns the local DNS label of the device using an IPv4 address.
func (e *Engine) NameForIP(ip net.IP) string {
	v4 := ip.To4()
	if v4 == nil {
		return ""
	}
	e.ipMu.Lock()
	id := e.ipDev[v4.String()]
	e.ipMu.Unlock()
	if id == "" {
		return ""
	}
	return e.LocalName(id)
}
