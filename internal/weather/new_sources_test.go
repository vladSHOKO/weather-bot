package weather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/metrics"
)

const metFixture = `{"properties":{"meta":{"units":{"air_temperature":"celsius","precipitation_amount":"mm"}},"timeseries":[{"time":"2026-10-19T09:00:00Z","data":{"instant":{"details":{"air_temperature":-2}},"next_1_hours":{"details":{"precipitation_amount":0}}}},{"time":"2026-10-19T10:00:00Z","data":{"instant":{"details":{"air_temperature":0}},"next_6_hours":{"details":{"precipitation_amount":3}}}},{"time":"2026-10-19T16:00:00Z","data":{"instant":{"details":{"air_temperature":null}}}}]}}`
const weatherAPIFixture = `{"forecast":{"forecastday":[{"hour":[{"time_epoch":1792400400,"temp_c":-2,"precip_mm":0,"chance_of_rain":0,"chance_of_snow":80},{"time_epoch":1792404000,"temp_c":0,"precip_mm":null,"chance_of_rain":null,"chance_of_snow":null}]}]}}`

func TestMetNoIntervalsAndUnknowns(t *testing.T) {
	points, err := parseMetNo(response(metFixture))
	if err != nil || len(points) != 3 {
		t.Fatalf("%+v %v", points, err)
	}
	if points[0].TemperatureC == nil || *points[0].TemperatureC != -2 || *points[0].PrecipitationMM != 0 || points[0].PrecipitationProb != nil {
		t.Fatal("zero, negative or unknown values lost")
	}
	if !points[0].PrecipitationFrom.Equal(points[0].Time) || points[1].PrecipitationTo.Sub(points[1].PrecipitationFrom) != 6*time.Hour || *points[1].PrecipitationMM != 3 {
		t.Fatal("forward accumulation intervals lost")
	}
	if points[2].TemperatureC != nil || points[2].PrecipitationMM != nil {
		t.Fatal("missing data became zero")
	}
	period := ForecastPeriod{From: points[1].Time.Add(time.Hour), To: points[1].Time.Add(3 * time.Hour)}
	summary := aggregateSource("MET Norway", points, period)
	rain := rainSummary([]SourceResult{summary}, period)
	if !rain.Known || len(rain.Intervals) != 1 || !rain.Intervals[0].Partial || rain.Risk != RainRiskMedium {
		t.Fatal("six-hour accumulation mishandled", rain)
	}
}
func TestWeatherAPIIntervalsAndSnow(t *testing.T) {
	points, err := parseWeatherAPI(response(weatherAPIFixture))
	if err != nil || len(points) != 2 {
		t.Fatalf("%+v %v", points, err)
	}
	if *points[0].PrecipitationProb != 80 || *points[0].TemperatureC != -2 || !points[0].Time.Equal(points[0].PrecipitationFrom) || points[0].PrecipitationTo.Sub(points[0].Time) != time.Hour {
		t.Fatal("hourly forecast parsed incorrectly")
	}
	if *points[1].TemperatureC != 0 || points[1].PrecipitationMM != nil || points[1].PrecipitationProb != nil {
		t.Fatal("nulls became zero")
	}
}
func TestNewSourcesRejectMalformedResponses(t *testing.T) {
	for _, body := range []string{"null", "{}", strings.Replace(metFixture, "celsius", "fahrenheit", 1), strings.Replace(metFixture, "\"precipitation_amount\":3", "\"precipitation_amount\":-1", 1), strings.Replace(metFixture, "2026-10-19T16:00:00Z", "2026-10-19T09:00:00Z", 1)} {
		if _, err := parseMetNo(response(body)); err == nil {
			t.Fatal("invalid MET accepted", body)
		}
	}
	for _, body := range []string{"null", "{}", strings.Replace(weatherAPIFixture, "\"time_epoch\":1792400400", "\"time_epoch\":null", 1), strings.Replace(weatherAPIFixture, "\"chance_of_rain\":0", "\"chance_of_rain\":101", 1), strings.Replace(weatherAPIFixture, "\"precip_mm\":0", "\"precip_mm\":-1", 1)} {
		if _, err := parseWeatherAPI(response(body)); err == nil {
			t.Fatal("invalid WeatherAPI accepted", body)
		}
	}
}
func metTestProvider(t *testing.T, capacity int) (*MetNoProvider, *int) {
	t.Helper()
	calls := new(int)
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.UserAgent() != "weather-sum/1.0 synthetic@example.org" {
			t.Error("missing identification")
		}
		for _, key := range []string{"lat", "lon"} {
			value := r.URL.Query().Get(key)
			if dot := strings.IndexByte(value, '.'); dot < 0 || len(value[dot+1:]) > 4 {
				t.Error("coordinates too precise")
			}
		}
		header := make(http.Header)
		header.Set("Expires", now.Add(30*time.Minute).Format(http.TimeFormat))
		header.Set("Last-Modified", now.Add(-time.Hour).Format(http.TimeFormat))
		if *calls == 1 {
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(metFixture))}, nil
		}
		if r.Header.Get("If-Modified-Since") != now.Add(-time.Hour).Format(http.TimeFormat) {
			t.Error("missing conditional request")
		}
		header.Set("Expires", now.Add(time.Hour).Format(http.TimeFormat))
		return &http.Response{StatusCode: 304, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	p := NewMetNoProvider(client, "weather-sum/1.0 synthetic@example.org", capacity)
	p.now = func() time.Time { return now }
	return p, calls
}
func TestMetNoHonorsExpiresAndRevalidates(t *testing.T) {
	p, calls := metTestProvider(t, 2)
	ctx := context.Background()
	period := ForecastPeriod{From: p.now(), To: p.now().Add(time.Hour)}
	loc := city
	loc.Latitude, loc.Longitude = 55.7558123, 37.6173123
	first, err := p.Forecast(ctx, loc, period)
	if err != nil {
		t.Fatal(err)
	}
	// Sharing across timezones and days must not bypass the HTTP cache.
	loc.Timezone = "Europe/Berlin"
	_, _ = p.Forecast(ctx, loc, ForecastPeriod{From: period.From.Add(24 * time.Hour), To: period.To.Add(24 * time.Hour)})
	if *calls != 1 {
		t.Fatal("refetched before Expires")
	}
	now := p.now().Add(31 * time.Minute)
	p.now = func() time.Time { return now }
	second, err := p.Forecast(ctx, loc, period)
	if err != nil || len(second) != len(first) || *calls != 2 {
		t.Fatal("304 failed", err)
	}
	_, _ = p.Forecast(ctx, loc, period)
	if *calls != 2 {
		t.Fatal("304 freshness lost")
	}
}
func TestMetNoCapacityPreservesFreshEntries(t *testing.T) {
	p, calls := metTestProvider(t, 1)
	period := ForecastPeriod{From: p.now(), To: p.now().Add(time.Hour)}
	_, _ = p.Forecast(context.Background(), city, period)
	other := city
	other.Longitude++
	if _, err := p.Forecast(context.Background(), other, period); !errors.Is(err, ErrNoData) {
		t.Fatal("full cache should reject new coordinate", err)
	}
	_, _ = p.Forecast(context.Background(), city, period)
	if *calls != 1 {
		t.Fatal("fresh entry evicted")
	}
}
func TestMetNoRateLimitAndInvalid304(t *testing.T) {
	for _, code := range []int{429, 304} {
		calls := 0
		client := &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
			calls++
			header := make(http.Header)
			header.Set("Retry-After", "120")
			return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		p := NewMetNoProvider(client, "weather-sum test@example.org", 10)
		period := ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)}
		_, err := p.Forecast(context.Background(), city, period)
		if code == 304 {
			if !errors.Is(err, httpx.ErrInvalidResponse) {
				t.Fatal("unsolicited 304 accepted", err)
			}
			continue
		}
		other := city
		other.Longitude++
		_, err = p.Forecast(context.Background(), other, period)
		var status *httpx.StatusError
		if calls != 1 || !errors.As(err, &status) || status.Code != 429 || status.RetryAfter <= 0 {
			t.Fatal("rate limit ignored", calls, err)
		}
	}
}
func TestNewSourcesPartialFailureAndCaching(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	client := &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		counts[r.URL.Host]++
		mu.Unlock()
		if r.URL.Host == "api.met.no" {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":"ignored"}`))}, nil
		}
		if r.URL.Query().Get("days") != "2" || r.URL.Query().Get("key") != "synthetic" || r.URL.Query().Get("aqi") != "no" {
			t.Error("invalid free forecast request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(weatherAPIFixture))}, nil
	})}
	m := &metrics.Metrics{}
	svc := NewService(Cached(NewWeatherAPIProvider(client, "synthetic"), 10, time.Hour, time.Second, m), Cached(NewMetNoProvider(client, "weather-sum test@example.org", 10), 10, time.Hour, time.Second, m))
	svc.now = func() time.Time { return time.Unix(1792400400, 0) }
	for range 2 {
		result, err := svc.GetTodayWeather(context.Background(), city)
		if err != nil || !result.Sources[0].IsAvailable || result.Sources[1].IsAvailable || result.Rain.Complete {
			t.Fatal("partial source handling", result, err)
		}
	}
	if counts["api.weatherapi.com"] != 1 {
		t.Fatal("WeatherAPI cache lost")
	}
}
func TestWeatherAPIErrorsDoNotExposeKey(t *testing.T) {
	const key = "SYNTHETIC_WEATHERAPI_KEY"
	cause := fmt.Errorf("network failure %s", key)
	client := &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}
	p := NewWeatherAPIProvider(client, key)
	_, err := p.Forecast(context.Background(), city, ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)})
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), key) || !errors.Is(err, cause) {
		t.Fatal("unsafe or missing error", err)
	}
	client.Transport = securityTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"` + key + `"}}`))}, nil
	})
	_, err = p.Forecast(context.Background(), city, ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)})
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatal("provider description leaked")
	}
}

func TestMetNoSharesRequestsWhenOneWaiterCancels(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	client := &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		header := make(http.Header)
		header.Set("Expires", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(metFixture))}, nil
	})}
	p := NewMetNoProvider(client, "weather-sum test@example.org", 2)
	period := ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := p.Forecast(ctx, city, period); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled waiter did not stop", err)
	}
	second := make(chan error, 1)
	go func() { _, err := p.Forecast(context.Background(), city, period); second <- err }()
	close(release)
	if err := <-second; err != nil {
		t.Fatal("shared request canceled", err)
	}
	if calls != 1 {
		t.Fatal("duplicate network request")
	}
}
