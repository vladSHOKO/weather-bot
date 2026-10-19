package cache

import (
	"context"
	"fmt"
	"golang.org/x/sync/singleflight"
	"sync"
	"time"
	"weather-sum/internal/metrics"
)

type entry[V any] struct {
	value   V
	expires time.Time
}
type Cache[V any] struct {
	mu           sync.Mutex
	items        map[string]entry[V]
	group        singleflight.Group
	capacity     int
	ttl, timeout time.Duration
	metrics      *metrics.Metrics
}

func New[V any](capacity int, ttl, timeout time.Duration, m *metrics.Metrics) *Cache[V] {
	return &Cache[V]{items: make(map[string]entry[V]), capacity: capacity, ttl: ttl, timeout: timeout, metrics: m}
}
func (c *Cache[V]) lookup(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if ok && time.Now().Before(e.expires) {
		return e.value, true
	}
	delete(c.items, key)
	var zero V
	return zero, false
}
func (c *Cache[V]) Get(ctx context.Context, key string, load func(context.Context) (V, error)) (V, error) {
	if v, ok := c.lookup(key); ok {
		c.metrics.CacheHits.Add(1)
		return v, nil
	}
	c.metrics.CacheMisses.Add(1)
	ch := c.group.DoChan(key, func() (any, error) {
		if v, ok := c.lookup(key); ok {
			return v, nil
		}
		// A canceled waiter must not cancel the shared, bounded load.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		defer cancel()
		v, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.items) >= c.capacity {
			oldKey := ""
			oldest := time.Time{}
			for k, e := range c.items {
				if oldKey == "" || e.expires.Before(oldest) {
					oldKey, oldest = k, e.expires
				}
			}
			delete(c.items, oldKey)
		}
		c.items[key] = entry[V]{value: v, expires: time.Now().Add(c.ttl)}
		return v, nil
	})
	select {
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			var zero V
			return zero, r.Err
		}
		v, ok := r.Val.(V)
		if !ok {
			var zero V
			return zero, fmt.Errorf("cache type mismatch")
		}
		return v, nil
	}
}
