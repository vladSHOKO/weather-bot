package weather

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/safeerror"
)

type metEntry struct {
	points   []ForecastPoint
	expires  time.Time
	modified string
	loading  bool
}

type MetNoProvider struct {
	client       *http.Client
	userAgent    string
	capacity     int
	mu           sync.Mutex
	entries      map[string]metEntry
	group        singleflight.Group
	blockedUntil time.Time
	now          func() time.Time
}

func NewMetNoProvider(c *http.Client, userAgent string, capacity int) *MetNoProvider {
	return &MetNoProvider{client: c, userAgent: userAgent, capacity: max(1, capacity), entries: make(map[string]metEntry), now: time.Now}
}
func (p *MetNoProvider) Name() string { return "MET Norway" }
func (p *MetNoProvider) Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) (points []ForecastPoint, err error) {
	defer func() { err = safeerror.Wrap("met norway forecast", err) }()
	if err = loc.Validate(); err != nil {
		return nil, err
	}
	if !period.To.After(period.From) || period.To.Sub(period.From) > 48*time.Hour {
		return nil, fmt.Errorf("invalid forecast period")
	}
	if strings.TrimSpace(p.userAgent) == "" {
		return nil, fmt.Errorf("MET_NO_USER_AGENT is required")
	}
	// MET requires at most four decimal places; nearby requests share HTTP cache.
	query := url.Values{"lat": {fmt.Sprintf("%.4f", math.Trunc(loc.Latitude*1e4)/1e4)}, "lon": {fmt.Sprintf("%.4f", math.Trunc(loc.Longitude*1e4)/1e4)}}
	key := query.Encode()
	result := p.group.DoChan(key, func() (any, error) {
		// A canceled waiter must not cancel another chat's shared forecast request.
		timeout := p.client.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return p.load(loadCtx, key)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]ForecastPoint), nil
	}
}
func (p *MetNoProvider) load(ctx context.Context, key string) (points []ForecastPoint, err error) {
	p.mu.Lock()
	now := p.now()
	previous, exists := p.entries[key]
	if exists && now.Before(previous.expires) {
		p.mu.Unlock()
		return previous.points, nil
	}
	if now.Before(p.blockedUntil) {
		retry := p.blockedUntil.Sub(now)
		p.mu.Unlock()
		return nil, &httpx.StatusError{Code: 429, RetryAfter: retry}
	}
	if !exists && len(p.entries) >= p.capacity {
		for k, e := range p.entries {
			if !e.loading && !now.Before(e.expires) {
				delete(p.entries, k)
			}
		}
		if len(p.entries) >= p.capacity {
			p.mu.Unlock()
			return nil, ErrNoData
		}
	}
	// Reserve capacity before network I/O. Never evict an unexpired response:
	// doing so could cause a repeat request before MET's Expires deadline.
	reserved := previous
	reserved.loading = true
	p.entries[key] = reserved
	p.mu.Unlock()
	defer func() {
		if err != nil {
			p.mu.Lock()
			delete(p.entries, key)
			p.mu.Unlock()
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.met.no/weatherapi/locationforecast/2.0/compact?"+key, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent)
	if previous.modified != "" {
		req.Header.Set("If-Modified-Since", previous.modified)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotModified:
		if len(previous.points) == 0 {
			return nil, httpx.ErrInvalidResponse
		}
		points = previous.points
	case http.StatusOK, http.StatusNonAuthoritativeInfo:
		if res.StatusCode == http.StatusNonAuthoritativeInfo {
			slog.Warn("MET Norway API version is deprecated")
		}
		points, err = parseMetNo(res)
		if err != nil {
			return nil, err
		}
	default:
		retry := time.Duration(0)
		if res.StatusCode == 429 {
			retry = metRetryAfter(res.Header.Get("Retry-After"), p.now())
			p.mu.Lock()
			if until := p.now().Add(retry); until.After(p.blockedUntil) {
				p.blockedUntil = until
			}
			p.mu.Unlock()
		}
		return nil, &httpx.StatusError{Code: res.StatusCode, RetryAfter: retry}
	}
	expires, err := http.ParseTime(res.Header.Get("Expires"))
	if err != nil || !expires.After(p.now()) {
		expires = p.now().Add(5 * time.Minute)
	}
	modified := previous.modified
	if header := res.Header.Get("Last-Modified"); header != "" {
		if _, parseErr := http.ParseTime(header); parseErr == nil {
			modified = header
		}
	}
	p.mu.Lock()
	p.entries[key] = metEntry{points: points, expires: expires, modified: modified}
	p.mu.Unlock()
	return points, nil
}
func metRetryAfter(header string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return time.Minute
}
func parseMetNo(res *http.Response) ([]ForecastPoint, error) {
	type accumulation struct {
		Details struct {
			MM *float64 `json:"precipitation_amount"`
		} `json:"details"`
	}
	var body struct {
		Properties struct {
			Meta struct {
				Units struct {
					Temperature   string `json:"air_temperature"`
					Precipitation string `json:"precipitation_amount"`
				} `json:"units"`
			} `json:"meta"`
			Timeseries []struct {
				Time time.Time `json:"time"`
				Data struct {
					Instant struct {
						Details struct {
							Temperature *float64 `json:"air_temperature"`
						} `json:"details"`
					} `json:"instant"`
					Next1  *accumulation `json:"next_1_hours"`
					Next6  *accumulation `json:"next_6_hours"`
					Next12 *accumulation `json:"next_12_hours"`
				} `json:"data"`
			} `json:"timeseries"`
		} `json:"properties"`
	}
	if err := httpx.Decode(res.Body, 2<<20, &body); err != nil {
		return nil, err
	}
	data := body.Properties
	if data.Meta.Units.Temperature != "celsius" || data.Meta.Units.Precipitation != "mm" || len(data.Timeseries) > 256 {
		return nil, httpx.ErrInvalidResponse
	}
	if len(data.Timeseries) == 0 {
		return nil, ErrNoData
	}
	points := make([]ForecastPoint, 0, len(data.Timeseries))
	var previous time.Time
	for _, r := range data.Timeseries {
		if r.Time.IsZero() || !r.Time.After(previous) {
			return nil, httpx.ErrInvalidResponse
		}
		previous = r.Time
		point := ForecastPoint{Time: r.Time, TemperatureC: r.Data.Instant.Details.Temperature}
		for _, interval := range []struct {
			value *accumulation
			hours int
		}{{r.Data.Next1, 1}, {r.Data.Next6, 6}, {r.Data.Next12, 12}} {
			if interval.value != nil && interval.value.Details.MM != nil {
				point.PrecipitationMM = interval.value.Details.MM
				point.PrecipitationFrom, point.PrecipitationTo = r.Time, r.Time.Add(time.Duration(interval.hours)*time.Hour)
				break
			}
		}
		// Compact has no probability field. Missing probability stays unknown.
		if !validPoint(point) {
			return nil, httpx.ErrInvalidResponse
		}
		points = append(points, point)
	}
	return points, nil
}
