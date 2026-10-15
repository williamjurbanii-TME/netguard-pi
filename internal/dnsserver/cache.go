package dnsserver

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type cacheEntry struct {
	msg     *dns.Msg
	stored  time.Time
	expires time.Time
}

// Cache is a small TTL-respecting response cache.
type Cache struct {
	mu  sync.Mutex
	max int
	m   map[string]cacheEntry
}

func NewCache(max int) *Cache {
	return &Cache{max: max, m: make(map[string]cacheEntry, max)}
}

func cacheKey(q dns.Question) string {
	return fmt.Sprintf("%s|%d|%d", strings.ToLower(q.Name), q.Qtype, q.Qclass)
}

func (c *Cache) Get(q dns.Question) *dns.Msg {
	key := cacheKey(q)
	now := time.Now()
	c.mu.Lock()
	e, ok := c.m[key]
	if ok && now.After(e.expires) {
		delete(c.m, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	msg := e.msg.Copy()
	elapsed := uint32(now.Sub(e.stored).Seconds())
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			h := rr.Header()
			if h.Rrtype == dns.TypeOPT {
				continue
			}
			if h.Ttl > elapsed {
				h.Ttl -= elapsed
			} else {
				h.Ttl = 0
			}
		}
	}
	return msg
}

func (c *Cache) Set(q dns.Question, msg *dns.Msg) {
	if msg.Truncated || (msg.Rcode != dns.RcodeSuccess && msg.Rcode != dns.RcodeNameError) {
		return
	}
	ttl := minTTL(msg)
	if ttl == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		// Evict ~10% of entries; map iteration order is effectively random.
		n := c.max / 10
		for k := range c.m {
			delete(c.m, k)
			if n--; n <= 0 {
				break
			}
		}
	}
	c.m[cacheKey(q)] = cacheEntry{msg: msg.Copy(), stored: now, expires: now.Add(time.Duration(ttl) * time.Second)}
}

func minTTL(msg *dns.Msg) uint32 {
	var ttl uint32
	found := false
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns} {
		for _, rr := range section {
			h := rr.Header()
			if h.Rrtype == dns.TypeOPT {
				continue
			}
			if !found || h.Ttl < ttl {
				ttl, found = h.Ttl, true
			}
		}
	}
	if !found {
		ttl = 60 // negative answer without SOA
	}
	if len(msg.Answer) == 0 && ttl > 300 {
		ttl = 300 // cap negative caching
	}
	if ttl > 3600 {
		ttl = 3600
	}
	return ttl
}
