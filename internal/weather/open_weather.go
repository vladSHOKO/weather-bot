package weather

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/safeerror"
)

type OpenWeatherProvider struct {
	client *http.Client
	apiKey string
}

func NewOpenWeatherProvider(c *http.Client, key string) *OpenWeatherProvider {
	return &OpenWeatherProvider{client: c, apiKey: key}
}
func (p *OpenWeatherProvider) Name() string { return "OpenWeather" }
func (p *OpenWeatherProvider) Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) (points []ForecastPoint, err error) {
	defer func() { err = safeerror.Wrap("openweather forecast", err) }()
	if p.apiKey == "" {
		return nil, fmt.Errorf("openweather key missing")
	}
	if !period.To.After(period.From) {
		return nil, fmt.Errorf("invalid period")
	}
	query := url.Values{"lat": {fmt.Sprintf("%f", loc.Latitude)}, "lon": {fmt.Sprintf("%f", loc.Longitude)}, "units": {"metric"}, "appid": {p.apiKey}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openweathermap.org/data/2.5/forecast?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, &httpx.StatusError{Code: res.StatusCode}
	}
	// Filtering belongs to Service, including overlapping accumulation intervals.
	return p.parseForecastPoints(res, loc)
}
func (p *OpenWeatherProvider) parseForecastPoints(res *http.Response, loc location.Location) ([]ForecastPoint, error) {
	type volume struct {
		MM *float64 `json:"3h"`
	}
	var body struct {
		List []struct {
			Time *int64 `json:"dt"`
			Main struct {
				Temp *float64 `json:"temp"`
			} `json:"main"`
			Pop  *float64 `json:"pop"`
			Rain *volume  `json:"rain"`
			Snow *volume  `json:"snow"`
		} `json:"list"`
	}
	if err := httpx.Decode(res.Body, 1<<20, &body); err != nil {
		return nil, err
	}
	if len(body.List) > 80 {
		return nil, httpx.ErrInvalidResponse
	}
	zone, err := time.LoadLocation(loc.Timezone)
	if err != nil {
		return nil, err
	}
	points := make([]ForecastPoint, 0, len(body.List))
	for _, r := range body.List {
		if r.Time == nil || *r.Time <= 0 {
			return nil, httpx.ErrInvalidResponse
		}
		at := time.Unix(*r.Time, 0).In(zone)
		var prob *int
		if r.Pop != nil {
			if math.IsNaN(*r.Pop) || *r.Pop < 0 || *r.Pop > 1 {
				return nil, httpx.ErrInvalidResponse
			}
			v := int(math.Round(*r.Pop * 100))
			prob = &v
		}
		var mm *float64
		for _, v := range []*volume{r.Rain, r.Snow} {
			if v != nil {
				if v.MM == nil || *v.MM < 0 {
					return nil, httpx.ErrInvalidResponse
				}
				if mm == nil {
					value := 0.0
					mm = &value
				}
				*mm += *v.MM
			}
		}
		point := ForecastPoint{Time: at, TemperatureC: r.Main.Temp, PrecipitationFrom: at.Add(-3 * time.Hour), PrecipitationTo: at, PrecipitationMM: mm, PrecipitationProb: prob}
		if !validPoint(point) {
			return nil, httpx.ErrInvalidResponse
		}
		points = append(points, point)
	}
	return points, nil
}
