package weather

import (
	"context"
	"fmt"
	"time"
	"weather-sum/internal/cache"
	"weather-sum/internal/location"
	"weather-sum/internal/metrics"
)

type cachedProvider struct {
	provider Provider
	cache    *cache.Cache[[]ForecastPoint]
	metrics  *metrics.Metrics
}

func Cached(p Provider, capacity int, ttl, timeout time.Duration, m *metrics.Metrics) Provider {
	return &cachedProvider{provider: p, cache: cache.New[[]ForecastPoint](capacity, ttl, timeout, m), metrics: m}
}
func (p *cachedProvider) Name() string { return p.provider.Name() }
func (p *cachedProvider) Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) ([]ForecastPoint, error) {
	zone, err := time.LoadLocation(loc.Timezone)
	if err != nil {
		return nil, err
	}
	day := period.From.In(zone)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, zone)
	to := from.AddDate(0, 0, 1)
	key := fmt.Sprintf("%.6f:%.6f:%s:%s", loc.Latitude, loc.Longitude, loc.Timezone, from.Format("2006-01-02"))
	return p.cache.Get(ctx, key, func(ctx context.Context) ([]ForecastPoint, error) {
		p.metrics.ProviderRequests.Add(1)
		points, err := p.provider.Forecast(ctx, loc, ForecastPeriod{From: from, To: to})
		if err == nil && !aggregateSource(p.Name(), points, ForecastPeriod{From: from, To: to}).IsAvailable {
			err = ErrNoData
		}
		if err != nil {
			p.metrics.ProviderFailures.Add(1)
			return nil, err
		}
		return points, nil
	})
}
