// Package clients maps a DNS client's IP address to a network identifier:
// its MAC address when it can be found, otherwise "ip:<addr>".
package clients

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LeaseLookup is implemented by the DHCP server.
type LeaseLookup interface {
	MACForIP(ip net.IP) string
}

const (
	minRefreshGap = 2 * time.Second  // at most one on-demand table read per gap
	negativeTTL   = time.Minute      // don't re-read the table for the same unknown IP
	backgroundGap = 60 * time.Second // periodic refresh so reassigned IPs are noticed
)

type Resolver struct {
	leases LeaseLookup // may be nil when DHCP is disabled

	refreshMu sync.Mutex // serialises reads of the OS neighbour table

	mu        sync.Mutex
	table     map[string]string    // ip -> mac (IPv4 and IPv6)
	negative  map[string]time.Time // ip -> last time it was not found
	refreshed time.Time
}

func NewResolver(leases LeaseLookup) *Resolver {
	return &Resolver{leases: leases, table: map[string]string{}, negative: map[string]time.Time{}}
}

// Run refreshes the neighbour table periodically until ctx is done.
func (r *Resolver) Run(ctx context.Context) {
	t := time.NewTicker(backgroundGap)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refresh()
		}
	}
}

// Identify returns the identifier for a client IP: a MAC address (from the
// DHCP lease, then the OS ARP/NDP neighbour table), "ip:<addr>", or "local".
func (r *Resolver) Identify(ip net.IP) string {
	if ip == nil {
		return "unknown"
	}
	if ip.IsLoopback() {
		return "local"
	}
	if r.leases != nil {
		if mac := r.leases.MACForIP(ip); mac != "" {
			return mac
		}
	}
	key := ip.String()
	if mac := r.cached(key); mac != "" {
		return mac
	}
	if r.refreshFor(key) {
		if mac := r.cached(key); mac != "" {
			return mac
		}
	}
	return "ip:" + key
}

func (r *Resolver) cached(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.table[key]
}

// refreshFor re-reads the neighbour table for an unknown IP, rate limited.
func (r *Resolver) refreshFor(key string) bool {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.mu.Lock()
	_, found := r.table[key] // someone else may have refreshed while we waited
	skip := found || time.Since(r.negative[key]) < negativeTTL || time.Since(r.refreshed) < minRefreshGap
	r.mu.Unlock()
	if found {
		return true
	}
	if skip {
		return false
	}
	r.readTable()
	r.mu.Lock()
	if _, ok := r.table[key]; !ok {
		r.negative[key] = time.Now()
	}
	r.mu.Unlock()
	return true
}

func (r *Resolver) refresh() {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.readTable()
}

// readTable must be called with refreshMu held.
func (r *Resolver) readTable() {
	table, err := readNeighbors()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshed = time.Now()
	if err == nil && len(table) > 0 {
		r.table = table
	}
	for ip, t := range r.negative {
		if time.Since(t) > negativeTTL {
			delete(r.negative, ip)
		}
	}
}

func normMAC(s string) string {
	hw, err := net.ParseMAC(strings.ReplaceAll(s, "-", ":"))
	if err != nil || len(hw) != 6 {
		return ""
	}
	if hw[0]&1 == 1 || hw.String() == "00:00:00:00:00:00" { // multicast/broadcast/incomplete
		return ""
	}
	return hw.String()
}

func normIP(s string) string {
	s = strings.Trim(s, "()")
	if i := strings.IndexByte(s, '%'); i >= 0 { // drop IPv6 zone
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// parseNeighbors reads lines that contain an IP followed somewhere later by a
// MAC. This covers `arp -a` (Windows/macOS), `netsh ... show neighbors`,
// `ip neigh` and `ndp -an`.
func parseNeighbors(text string, into map[string]string) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			ip := normIP(f)
			if ip == "" {
				continue
			}
			for _, g := range fields[i+1:] {
				if mac := normMAC(g); mac != "" {
					into[ip] = mac
					break
				}
			}
			break
		}
	}
}

func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}

// Neighbors returns the OS neighbour table (IP -> MAC), for diagnostics.
func Neighbors() (map[string]string, error) { return readNeighbors() }

func readNeighbors() (map[string]string, error) {
	out := map[string]string{}
	switch runtime.GOOS {
	case "linux":
		if text := run("ip", "neigh", "show"); text != "" {
			parseNeighbors(text, out)
			return out, nil
		}
		f, err := os.Open("/proc/net/arp") // fallback: IPv4 only
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			if fields := strings.Fields(sc.Text()); len(fields) >= 4 {
				if mac := normMAC(fields[3]); mac != "" {
					out[fields[0]] = mac
				}
			}
		}
		return out, sc.Err()
	case "windows":
		parseNeighbors(run("arp", "-a"), out)
		parseNeighbors(run("netsh", "interface", "ipv6", "show", "neighbors"), out)
	default: // macOS / BSD
		parseNeighbors(run("arp", "-an"), out)
		parseNeighbors(run("ndp", "-an"), out)
	}
	return out, nil
}
