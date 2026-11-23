// Package cache provides a bounded, thread-safe, TTL-aware LRU cache for
// translation results.
package cache

import (
	"container/list"
	"slices"
	"sync"
	"time"
)

const (
	// DefaultCapacity is the default maximum number of cached entries.
	DefaultCapacity = 256
	// DefaultTTL is the default lifetime of a cached translation result.
	DefaultTTL = 30 * time.Minute
)

// Key uniquely identifies a cacheable translation request.
type Key struct {
	Source     string
	TargetLang string
	Mode       string
	Service    string
}

// ProviderSnapshot captures a single provider card's completed result for
// multi-engine cache hits.
type ProviderSnapshot struct {
	ID      string
	Name    string
	Kind    string
	Result  string
	ErrText string
	ErrHint string
}

// Value stores the cached single-engine or multi-engine translation output.
type Value struct {
	Result       string
	ProviderName string
	MultiResults []ProviderSnapshot
}

type cacheEntry struct {
	key       Key
	value     Value
	expiresAt time.Time
}

// TranslationCache is a bounded LRU cache safe for concurrent use.
// A nil *TranslationCache is safe (no-op writes, cache misses on reads) and a
// zero-value TranslationCache initializes lazily with DefaultCapacity.
type TranslationCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	ll       *list.List
	items    map[Key]*list.Element
}

// New creates a TranslationCache with the given capacity (defaulting to
// DefaultCapacity if <= 0) and TTL (no expiry if <= 0).
func New(capacity int, ttl time.Duration) *TranslationCache {
	return NewWithClock(capacity, ttl, time.Now)
}

// NewWithClock creates a TranslationCache using a custom clock function,
// primarily for deterministic expiration testing.
func NewWithClock(capacity int, ttl time.Duration, now func() time.Time) *TranslationCache {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if now == nil {
		now = time.Now
	}
	return &TranslationCache{
		capacity: capacity,
		ttl:      ttl,
		now:      now,
		ll:       list.New(),
		items:    make(map[Key]*list.Element, capacity),
	}
}

func (c *TranslationCache) initLocked() {
	if c.capacity <= 0 {
		c.capacity = DefaultCapacity
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.ll == nil {
		c.ll = list.New()
	}
	if c.items == nil {
		c.items = make(map[Key]*list.Element, c.capacity)
	}
}

func cloneValue(v Value) Value {
	if v.MultiResults != nil {
		v.MultiResults = slices.Clone(v.MultiResults)
	}
	return v
}

// Get retrieves a non-expired entry for k and marks it most recently used.
func (c *TranslationCache) Get(k Key) (Value, bool) {
	if c == nil {
		return Value{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.items == nil {
		return Value{}, false
	}
	el, ok := c.items[k]
	if !ok {
		return Value{}, false
	}
	entry := el.Value.(*cacheEntry)
	if !entry.expiresAt.IsZero() && !c.now().Before(entry.expiresAt) {
		c.ll.Remove(el)
		delete(c.items, k)
		return Value{}, false
	}
	c.ll.MoveToFront(el)
	return cloneValue(entry.value), true
}

// Set inserts or updates k with v, evicting the least recently used entry if
// the cache exceeds capacity.
func (c *TranslationCache) Set(k Key, v Value) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.initLocked()
	var expiresAt time.Time
	if c.ttl > 0 {
		expiresAt = c.now().Add(c.ttl)
	}
	cloned := cloneValue(v)

	if el, ok := c.items[k]; ok {
		entry := el.Value.(*cacheEntry)
		entry.value = cloned
		entry.expiresAt = expiresAt
		c.ll.MoveToFront(el)
		return
	}

	el := c.ll.PushFront(&cacheEntry{
		key:       k,
		value:     cloned,
		expiresAt: expiresAt,
	})
	c.items[k] = el

	for c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.ll.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).key)
	}
}

// Delete removes k from the cache if present.
func (c *TranslationCache) Delete(k Key) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.items == nil {
		return
	}
	if el, ok := c.items[k]; ok {
		c.ll.Remove(el)
		delete(c.items, k)
	}
}

// Clear removes all entries from the cache.
func (c *TranslationCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ll != nil {
		c.ll.Init()
	}
	clear(c.items)
}

// Len returns the number of entries currently stored in the cache.
func (c *TranslationCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.items)
}
