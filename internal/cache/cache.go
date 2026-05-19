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

	// hits / misses / evictions are per-cache counters surfaced
	// by Stats(). Otel metrics already track these too — these
	// in-cache counters are for the cache_stats MCP tool which
	// runs inside the same process as the cache.
	hits      int64
	misses    int64
	evictions int64
	// oldestExpiresAt is the wall-clock timestamp of the next
	// entry due to expire. Updated lazily on Set; surfaced via
	// Stats so cache_stats can render "oldest entry age".
	oldestExpiresAt time.Time
}

// Stats is a snapshot of a Cache's counters at a point in time.
// Returned by Stats() — designed to be JSON-encodable so the
// cache_stats MCP tool can forward it verbatim. Zero values
// distinguish "no activity" from "disabled" (Cache nil).
type Stats struct {
	Entries   int   `json:"entries"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	// OldestEntryAgeSeconds is approximate (the entry might not
	// be the strict oldest if Set was concurrent with Get's
	// touch). Useful for "is the cache cold?" diagnostics, not
	// strict correctness.
	OldestEntryAgeSeconds float64 `json:"oldest_entry_age_seconds"`
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
		c.misses++
		return nil, false
	}
	if c.now().After(e.expiresAt) {
		delete(c.entries, key)
		c.removeFromOrderLocked(key)
		c.misses++
		return nil, false
	}
	c.touchLocked(key)
	c.hits++
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
	// Track the youngest expiration as "oldest entry" hint —
	// reset on each Set so Stats reports the most recently
	// inserted entry's age. Approximate by design (see Stats).
	if c.oldestExpiresAt.IsZero() || now.Before(c.oldestExpiresAt) {
		c.oldestExpiresAt = now
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
	c.evictions++
}

// Stats returns a counter snapshot. Safe to call concurrently with
// Get/Set; counters are read under the cache mutex.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{
		Entries:   len(c.entries),
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
	}
	if !c.oldestExpiresAt.IsZero() && len(c.entries) > 0 {
		s.OldestEntryAgeSeconds = c.now().Sub(c.oldestExpiresAt).Seconds()
	}
	return s
}

// Purge wipes every entry + resets the LRU order. Counters survive
// (operators sometimes want to know the historic hit rate even
// after a purge). Safe to call concurrently with Get/Set.
func (c *Cache) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.entries)
	c.entries = map[string]*entry{}
	c.order = c.order[:0]
	c.oldestExpiresAt = time.Time{}
	return n
}
