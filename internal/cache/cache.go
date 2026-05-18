// Package cache is scry's per-upstream TTL+LRU query result cache.
// Right for agents that re-call the same read query repeatedly
// during a task — context refreshes, multi-step plans that revisit
// the same node. Mutations bypass unconditionally; the cache only
// helps reads.
//
// Keying: SHA-256(query | "\x00" | jsonVars | "\x00" | operationName).
// JSON canonicalisation of variables keeps semantically-equivalent
// inputs colliding into one entry (sort.Strings on the map keys
// before marshal).
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Cache is the per-upstream cache. Safe for concurrent use. Zero
// max disables the entry limit; zero TTL disables caching entirely
// (Get/Set become no-ops).
type Cache struct {
	ttl time.Duration
	max int

	mu      sync.Mutex
	entries map[string]*entry
	// order is the LRU list, oldest first. Tracked as a slice
	// because per-cache contention is low (one mutex per
	// upstream) and slice + linear scan is faster than a doubly-
	// linked list at the small cache sizes that benefit scry.
	order []string
	// now is the clock injection seam. Tests swap to fast-forward
	// through TTL expiry without sleeping.
	now func() time.Time
}

type entry struct {
	value     []byte
	expiresAt time.Time
}

// New returns a Cache with the given TTL + max size. ttl=0
// effectively disables the cache (every Get returns miss, Set is
// a no-op).
func New(ttl time.Duration, max int) *Cache {
	return &Cache{
		ttl:     ttl,
		max:     max,
		entries: map[string]*entry{},
		now:     time.Now,
	}
}

// Key derives the deterministic cache key for one request. Exposed
// so the caller can compute it once + use it for both Get + Set
// without hashing twice.
func Key(query string, variables map[string]any, opName string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(query))
	_, _ = h.Write([]byte{0})
	if len(variables) > 0 {
		// Sort keys so encoder output is deterministic across
		// runs / Go versions.
		keys := make([]string, 0, len(variables))
		for k := range variables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ordered := make(map[string]any, len(variables))
		for _, k := range keys {
			ordered[k] = variables[k]
		}
		buf, _ := json.Marshal(ordered)
		_, _ = h.Write(buf)
	}
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(opName))
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached value for key + true on a hit, nil + false
// otherwise. Expired entries are evicted on access; the caller
// always sees fresh data or a clean miss.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().After(e.expiresAt) {
		delete(c.entries, key)
		c.removeFromOrderLocked(key)
		return nil, false
	}
	c.touchLocked(key)
	return e.value, true
}

// Set stores value under key with the configured TTL. Evicts the
// oldest entry when max is set and the cache is at capacity.
func (c *Cache) Set(key string, value []byte) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.entries[key] = &entry{value: value, expiresAt: now.Add(c.ttl)}
	c.touchLocked(key)
	if c.max > 0 && len(c.entries) > c.max {
		c.evictOldestLocked()
	}
}

// Len returns the current number of cached entries. Exposed for
// metrics + tests.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// touchLocked moves key to the end of the LRU order. Caller MUST
// hold c.mu.
func (c *Cache) touchLocked(key string) {
	c.removeFromOrderLocked(key)
	c.order = append(c.order, key)
}

// removeFromOrderLocked drops one key from the LRU slice. O(n) in
// the worst case but cache sizes targeted by scry (≤ 10k) keep
// that cheap.
func (c *Cache) removeFromOrderLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// evictOldestLocked drops the oldest entry. Caller MUST hold c.mu
// AND have set c.max > 0.
func (c *Cache) evictOldestLocked() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.entries, oldest)
}
