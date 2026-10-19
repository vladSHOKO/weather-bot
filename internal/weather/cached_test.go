package weather

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
	"weather-sum/internal/location"
	"weather-sum/internal/metrics"
)

type providerFunc func(context.Context, location.Location, ForecastPeriod) ([]ForecastPoint, error)

func (f providerFunc) Name() string { return "test" }
func (f providerFunc) Forecast(ctx context.Context, l location.Location, p ForecastPeriod) ([]ForecastPoint, error) {
	return f(ctx, l, p)
}

func TestCacheRejectsUnusableForecastAndRecovers(t *testing.T) {
	for name, points := range map[string][]ForecastPoint{
		"empty":     nil,
		"unknown":   {{Time: at}},
		"invalid":   {{Time: at, TemperatureC: ptr(math.NaN())}},
		"other day": {{Time: at.AddDate(0, 0, 1), TemperatureC: ptr(5.0)}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			m := &metrics.Metrics{}
			p := Cached(providerFunc(func(context.Context, location.Location, ForecastPeriod) ([]ForecastPoint, error) {
				calls++
				if calls == 1 {
					return points, nil
				}
				return hourly(0, 0), nil
			}), 2, time.Minute, time.Second, m)
			period := ForecastPeriod{From: at, To: at.Add(time.Hour)}
			if _, err := p.Forecast(context.Background(), city, period); !errors.Is(err, ErrNoData) {
				t.Fatalf("unusable forecast accepted: %v", err)
			}
			for i := 0; i < 2; i++ {
				if _, err := p.Forecast(context.Background(), city, period); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 || m.ProviderFailures.Load() != 1 || m.CacheHits.Load() != 1 {
				t.Fatalf("failed response cached or zero data rejected: calls=%d metrics=%v", calls, m.Snapshot())
			}
		})
	}
}

func TestCachedForecastRefiltersTimeAndChangesDay(t *testing.T) {
	calls := 0
	p := Cached(providerFunc(func(_ context.Context, _ location.Location, period ForecastPeriod) ([]ForecastPoint, error) {
		calls++
		if period.From.Hour() != 0 || period.To.Sub(period.From) != 24*time.Hour {
			t.Error("provider did not receive a whole local day")
		}
		return []ForecastPoint{
			{Time: period.From.Add(18 * time.Hour), TemperatureC: ptr(-5.0)},
			{Time: period.From.Add(20 * time.Hour), TemperatureC: ptr(5.0)},
		}, nil
	}), 2, time.Minute, time.Second, &metrics.Metrics{})
	s := service(p)
	first, err := s.GetTodayWeather(context.Background(), city)
	if err != nil || first.Temperature.MinC != -5 {
		t.Fatalf("first forecast: %+v %v", first.Temperature, err)
	}
	s.now = func() time.Time { return at.Add(2 * time.Hour) }
	later, err := s.GetTodayWeather(context.Background(), city)
	if err != nil || later.Temperature.MinC != 5 || calls != 1 {
		t.Fatalf("cached past data affected forecast: %+v calls=%d err=%v", later.Temperature, calls, err)
	}
	s.now = func() time.Time { return at.AddDate(0, 0, 1) }
	if _, err := s.GetTodayWeather(context.Background(), city); err != nil || calls != 2 {
		t.Fatalf("previous day's cache reused: calls=%d err=%v", calls, err)
	}
}

func TestTodayAndTomorrowHaveSeparateCacheEntries(t *testing.T) {
	calls := 0
	provider := providerFunc(func(_ context.Context, _ location.Location, period ForecastPeriod) ([]ForecastPoint, error) {
		calls++
		// Give different days different temperatures to catch cross-day cache reuse.
		value := float64(period.From.Day())
		return []ForecastPoint{{Time: period.From.Add(20 * time.Hour), TemperatureC: ptr(value)}}, nil
	})
	s := service(Cached(provider, 4, time.Hour, time.Second, &metrics.Metrics{}))
	for range 2 {
		today, err := s.GetTodayWeather(context.Background(), city)
		if err != nil {
			t.Fatal(err)
		}
		tomorrow, err := s.GetTomorrowWeather(context.Background(), city)
		if err != nil {
			t.Fatal(err)
		}
		if today.Temperature.MinC == tomorrow.Temperature.MinC {
			t.Fatal("cached today forecast used for tomorrow")
		}
	}
	if calls != 2 {
		t.Fatal("forecast not cached by local date", calls)
	}
}
