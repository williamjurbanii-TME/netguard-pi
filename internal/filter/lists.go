package filter

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"netguard/internal/config"
)

const listMaxAge = 24 * time.Hour

type ListStatus struct {
	Name     string    `json:"name"`
	Category string    `json:"category"`
	URL      string    `json:"url"`
	Domains  int       `json:"domains"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error,omitempty"`
}

// ListManager downloads blocklists, caches them on disk and answers
// "which categories is this domain in?" lookups.
type ListManager struct {
	dir     string
	sources []config.ListSource
	client  *http.Client
	cats    []string          // category names, index == bit position
	catBits map[string]uint64 // category -> bit

	refreshMu sync.Mutex

	mu      sync.RWMutex
	domains map[string]uint64 // domain -> category bitmask
	status  []ListStatus
}

func NewListManager(dir string, sources []config.ListSource) *ListManager {
	lm := &ListManager{
		dir:     dir,
		sources: sources,
		client:  &http.Client{Timeout: 90 * time.Second},
		catBits: map[string]uint64{},
		domains: map[string]uint64{},
	}
	for _, s := range sources {
		if _, ok := lm.catBits[s.Category]; !ok && len(lm.cats) < 64 {
			lm.catBits[s.Category] = 1 << len(lm.cats)
			lm.cats = append(lm.cats, s.Category)
		}
	}
	return lm
}

func (lm *ListManager) Categories() []string { return append([]string(nil), lm.cats...) }

// Mask converts category names into a bitmask. Unknown names are ignored.
func (lm *ListManager) Mask(cats []string) uint64 {
	var m uint64
	for _, c := range cats {
		m |= lm.catBits[c]
	}
	return m
}

// CategoryFor returns the first category name present in mask.
func (lm *ListManager) CategoryFor(mask uint64) string {
	for i, c := range lm.cats {
		if mask&(1<<i) != 0 {
			return c
		}
	}
	return ""
}

// Lookup returns the category bitmask for domain, matching the domain itself
// and every parent domain (so listing example.com also blocks a.example.com).
func (lm *ListManager) Lookup(domain string) uint64 {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	var m uint64
	for d := domain; ; {
		m |= lm.domains[d]
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return m
}

func (lm *ListManager) Status() (lists []ListStatus, total int) {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return append([]ListStatus(nil), lm.status...), len(lm.domains)
}

// Refresh downloads stale lists (or all of them when force is set), then
// rebuilds the in-memory index. A failed download falls back to the cached copy.
func (lm *ListManager) Refresh(force bool) {
	lm.refreshMu.Lock()
	defer lm.refreshMu.Unlock()
	if err := os.MkdirAll(lm.dir, 0o755); err != nil {
		log.Printf("lists: %v", err)
		return
	}
	domains := make(map[string]uint64, 200_000)
	status := make([]ListStatus, 0, len(lm.sources))
	for _, src := range lm.sources {
		st := ListStatus{Name: src.Name, Category: src.Category, URL: src.URL}
		path := filepath.Join(lm.dir, cacheName(src.URL))
		fi, statErr := os.Stat(path)
		if force || statErr != nil || time.Since(fi.ModTime()) > listMaxAge {
			if err := lm.download(src.URL, path); err != nil {
				st.Error = err.Error()
				log.Printf("lists: download %s: %v", src.Name, err)
			}
		}
		if fi, err := os.Stat(path); err == nil {
			st.Updated = fi.ModTime()
		}
		n, err := parseFile(path, lm.catBits[src.Category], domains)
		if err != nil && st.Error == "" {
			st.Error = err.Error()
		}
		st.Domains = n
		status = append(status, st)
	}
	lm.mu.Lock()
	lm.domains = domains
	lm.status = status
	lm.mu.Unlock()
	log.Printf("lists: %d unique domains loaded from %d lists", len(domains), len(status))
}

func cacheName(url string) string {
	h := sha1.Sum([]byte(url))
	return hex.EncodeToString(h[:8]) + ".txt"
}

func (lm *ListManager) download(url, path string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "NetGuard/0.1")
	resp, err := lm.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseFile(path string, bit uint64, into map[string]uint64) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		for _, d := range parseLine(sc.Text()) {
			into[d] |= bit
			n++
		}
	}
	return n, sc.Err()
}

// parseLine understands hosts files ("0.0.0.0 a.com b.com"), plain domain
// lists ("a.com") and simple adblock rules ("||a.com^").
func parseLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' || line[0] == '!' || line[0] == '[' || strings.HasPrefix(line, "@@") {
		return nil
	}
	if strings.Contains(line, "##") || strings.Contains(line, "#@#") || strings.Contains(line, "#?#") {
		return nil // cosmetic adblock rule
	}
	if i := strings.Index(line, " #"); i >= 0 {
		line = line[:i]
	}
	if strings.HasPrefix(line, "||") {
		rest := line[2:]
		i := strings.IndexByte(rest, '^')
		if i < 0 {
			return nil
		}
		if d := cleanDomain(rest[:i]); d != "" {
			return []string{d}
		}
		return nil
	}
	fields := strings.Fields(line)
	if len(fields) > 1 && net.ParseIP(fields[0]) != nil {
		fields = fields[1:]
	} else if len(fields) != 1 {
		return nil
	}
	var out []string
	for _, f := range fields {
		if d := cleanDomain(f); d != "" {
			out = append(out, d)
		}
	}
	return out
}

var ignoredHosts = map[string]bool{
	"localhost": true, "localhost.localdomain": true, "local": true, "broadcasthost": true,
	"ip6-localhost": true, "ip6-loopback": true, "ip6-localnet": true, "ip6-mcastprefix": true,
	"ip6-allnodes": true, "ip6-allrouters": true, "ip6-allhosts": true, "0.0.0.0": true,
}

func cleanDomain(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if s == "" || len(s) > 253 || ignoredHosts[s] || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return ""
		}
	}
	return s
}

// NormalizeDomain cleans user input such as "https://www.YouTube.com/watch" or
// "*.example.com" into a bare domain, or "" if it is not a valid domain.
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#:"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "*.")
	return cleanDomain(s)
}
