package weather

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/safeerror"
)

type WeatherAPIProvider struct {
	client *http.Client
	apiKey string
}

func NewWeatherAPIProvider(c *http.Client, key string) *WeatherAPIProvider {
	return &WeatherAPIProvider{client: c, apiKey: key}
}
func (p *WeatherAPIProvider) Name() string { return "WeatherAPI.com" }
func (p *WeatherAPIProvider) Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) (points []ForecastPoint, err error) {
	defer func() { err = safeerror.Wrap("weatherapi forecast", err) }()
	if p.apiKey == "" {
		return nil, fmt.Errorf("WEATHERAPI_API_KEY is required")
	}
	if err = loc.Validate(); err != nil {
		return nil, err
	}
	if !period.To.After(period.From) || period.To.Sub(period.From) > 48*time.Hour {
		return nil, fmt.Errorf("invalid forecast period")
	}
	// Two days cover local midnight differences between geocoders without using
	// features beyond the Free plan's three-day forecast.
	query := url.Values{"key": {p.apiKey}, "q": {fmt.Sprintf("%.4f,%.4f", loc.Latitude, loc.Longitude)}, "days": {"2"}, "aqi": {"no"}, "alerts": {"no"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.weatherapi.com/v1/forecast.json?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &httpx.StatusError{Code: res.StatusCode}
	}
	return parseWeatherAPI(res)
}
func parseWeatherAPI(res *http.Response) ([]ForecastPoint, error) {
	var body struct {
		Forecast struct {
			Days []struct {
				Hours []struct {
					Time          *int64   `json:"time_epoch"`
					Temperature   *float64 `json:"temp_c"`
					Precipitation *float64 `json:"precip_mm"`
					Rain          *int     `json:"chance_of_rain"`
					Snow          *int     `json:"chance_of_snow"`
				} `json:"hour"`
			} `json:"forecastday"`
		} `json:"forecast"`
	}
	if err := httpx.Decode(res.Body, 2<<20, &body); err != nil {
		return nil, err
	}
	if len(body.Forecast.Days) > 3 {
		return nil, httpx.ErrInvalidResponse
	}
	var points []ForecastPoint
	var previous time.Time
	for _, day := range body.Forecast.Days {
		if len(day.Hours) > 25 {
			return nil, httpx.ErrInvalidResponse
		}
		for _, r := range day.Hours {
			if r.Time == nil || *r.Time <= 0 {
				return nil, httpx.ErrInvalidResponse
			}
			at := time.Unix(*r.Time, 0).UTC()
			if !at.After(previous) {
				return nil, httpx.ErrInvalidResponse
			}
			previous = at
			for _, probability := range []*int{r.Rain, r.Snow} {
				if probability != nil && (*probability < 0 || *probability > 100) {
					return nil, httpx.ErrInvalidResponse
				}
			}
			// Use the largest reported risk; rain and snow probabilities cannot be
			// added to obtain a probability of either event.
			probability := r.Rain
			if r.Snow != nil && (probability == nil || *r.Snow > *probability) {
				probability = r.Snow
			}
			point := ForecastPoint{Time: at, TemperatureC: r.Temperature, PrecipitationFrom: at, PrecipitationTo: at.Add(time.Hour), PrecipitationMM: r.Precipitation, PrecipitationProb: probability}
			if !validPoint(point) {
				return nil, httpx.ErrInvalidResponse
			}
			points = append(points, point)
		}
	}
	if len(points) == 0 {
		return nil, ErrNoData
	}
	return points, nil
}
