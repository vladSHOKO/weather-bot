package location

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"weather-sum/internal/cache"
	"weather-sum/internal/httpx"
	"weather-sum/internal/metrics"
	"weather-sum/internal/safeerror"
)

var ErrLocationNotFound = errors.New("location not found")

type OpenMeteoGeocoder struct {
	client *http.Client
	cache  *cache.Cache[[]Location]
}

func NewOpenMeteoGeocoder(client *http.Client) *OpenMeteoGeocoder {
	return &OpenMeteoGeocoder{client: client, cache: cache.New[[]Location](64, 10*time.Minute, client.Timeout+time.Second, &metrics.Metrics{})}
}
func (o *OpenMeteoGeocoder) Search(ctx context.Context, city string) (Location, error) {
	list, err := o.Candidates(ctx, city)
	if err != nil {
		return Location{}, err
	}
	if len(list) == 0 {
		return Location{}, ErrLocationNotFound
	}
	return list[0], nil
}
func (o *OpenMeteoGeocoder) Candidates(ctx context.Context, city string) (result []Location, err error) {
	defer func() { err = safeerror.Wrap("geocoding", err) }()
	city = strings.TrimSpace(city)
	if len(city) < 2 || len(city) > 256 {
		return nil, fmt.Errorf("invalid city length")
	}
	return o.cache.Get(ctx, strings.ToLower(city), func(ctx context.Context) ([]Location, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://geocoding-api.open-meteo.com/v1/search?name="+url.QueryEscape(city)+"&language=ru&count=5", nil)
		if err != nil {
			return nil, err
		}
		resp, err := o.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, &httpx.StatusError{Code: resp.StatusCode}
		}
		var body struct {
			Results []struct {
				Name      string   `json:"name"`
				Latitude  *float64 `json:"latitude"`
				Longitude *float64 `json:"longitude"`
				Timezone  string   `json:"timezone"`
				Country   string   `json:"country"`
				Region    string   `json:"admin1"`
			} `json:"results"`
		}
		if err := httpx.Decode(resp.Body, 128<<10, &body); err != nil {
			return nil, err
		}
		list := make([]Location, 0, len(body.Results))
		for _, r := range body.Results {
			if r.Latitude == nil || r.Longitude == nil {
				return nil, httpx.ErrInvalidResponse
			}
			l := Location{Name: r.Name, Latitude: *r.Latitude, Longitude: *r.Longitude, Timezone: r.Timezone, Country: r.Country, Region: r.Region}
			if l.Validate() != nil {
				return nil, httpx.ErrInvalidResponse
			}
			list = append(list, l)
		}
		if len(list) == 0 {
			return nil, ErrLocationNotFound
		}
		return list, nil
	})
}
