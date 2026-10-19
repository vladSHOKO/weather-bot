package metrics

import "sync/atomic"

// Aggregate counters have no user IDs, cities or unbounded label sets.
type Metrics struct {
	Commands         atomic.Int64
	Failures         atomic.Int64
	CommandNanos     atomic.Int64
	ProviderRequests atomic.Int64
	ProviderFailures atomic.Int64
	CacheHits        atomic.Int64
	CacheMisses      atomic.Int64
	RateLimited      atomic.Int64
}

func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{"commands": m.Commands.Load(), "failures": m.Failures.Load(), "command_nanoseconds": m.CommandNanos.Load(), "provider_requests": m.ProviderRequests.Load(), "provider_failures": m.ProviderFailures.Load(), "cache_hits": m.CacheHits.Load(), "cache_misses": m.CacheMisses.Load(), "rate_limited": m.RateLimited.Load()}
}
