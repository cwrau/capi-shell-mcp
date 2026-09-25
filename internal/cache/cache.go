// Package cache provides a generic in-memory TTL cache with lazy
// expiry-on-read (no background sweep), matching the semantics of the
// TypeScript implementation this project ports.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is a generic in-memory cache where entries expire ttl after
// being set. Expiry is checked lazily on Get; there is no background sweep.
type TTLCache[K comparable, V any] struct {
	ttl   time.Duration
	now   func() time.Time
	mu    sync.Mutex
	store map[K]entry[V]
}

// New creates a TTLCache whose entries live for ttl after being Set.
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return &TTLCache[K, V]{
		ttl:   ttl,
		now:   time.Now,
		store: make(map[K]entry[V]),
	}
}

// Get returns the cached value for key and true, or the zero value and
// false if the key is absent or its entry has expired.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.store[key]
	if !ok {
		var zero V
		return zero, false
	}
	if c.now().After(e.expiresAt) {
		delete(c.store, key)
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value under key, resetting its TTL.
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.store[key] = entry[V]{value: value, expiresAt: c.now().Add(c.ttl)}
}

// Delete removes key from the cache, if present.
func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.store, key)
}

// Clear removes all entries from the cache.
func (c *TTLCache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.store = make(map[K]entry[V])
}
