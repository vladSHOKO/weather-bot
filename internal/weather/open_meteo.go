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

type OpenMeteoProvider struct{ client *http.Client }

func NewOpenMeteoProvider(c *http.Client) *OpenMeteoProvider { return &OpenMeteoProvider{client: c} }
func (p *OpenMeteoProvider) Name() string                    { return "Open-Meteo" }
func (p *OpenMeteoProvider) Forecast(ctx context.Context, loc location.Location, period ForecastPeriod) (points []ForecastPoint, err error) {
	defer func() { err = safeerror.Wrap("openmeteo forecast", err) }()
	if !period.To.After(period.From) || period.To.Sub(period.From) > 48*time.Hour {
		return nil, fmt.Errorf("invalid forecast period")
	}
	zone, err := time.LoadLocation(loc.Timezone)
	if err != nil {
		return nil, err
	}
	query := url.Values{"latitude": {fmt.Sprintf("%f", loc.Latitude)}, "longitude": {fmt.Sprintf("%f", loc.Longitude)}, "hourly": {"temperature_2m,precipitation,precipitation_probability"}, "timezone": {loc.Timezone}, "timeformat": {"unixtime"}, "start_date": {period.From.In(zone).Format("2006-01-02")}, "end_date": {period.To.In(zone).Format("2006-01-02")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.open-meteo.com/v1/forecast?"+query.Encode(), nil)
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
	return p.parseForecastPoints(res, loc)
}
func (p *OpenMeteoProvider) parseForecastPoints(res *http.Response, loc location.Location) ([]ForecastPoint, error) {
	var body struct {
		Hourly struct {
			Time          []*int64   `json:"time"`
			Temperature   []*float64 `json:"temperature_2m"`
			Precipitation []*float64 `json:"precipitation"`
			Probability   []*int     `json:"precipitation_probability"`
		} `json:"hourly"`
	}
	if err := httpx.Decode(res.Body, 1<<20, &body); err != nil {
		return nil, err
	}
	h := body.Hourly
	n := len(h.Time)
	if n > 72 || n != len(h.Temperature) || n != len(h.Precipitation) || n != len(h.Probability) {
		return nil, httpx.ErrInvalidResponse
	}
	zone, err := time.LoadLocation(loc.Timezone)
	if err != nil {
		return nil, err
	}
	points := make([]ForecastPoint, 0, n)
	for i, stamp := range h.Time {
		if stamp == nil || *stamp <= 0 {
			return nil, httpx.ErrInvalidResponse
		}
		at := time.Unix(*stamp, 0).In(zone)
		point := ForecastPoint{Time: at, TemperatureC: h.Temperature[i], PrecipitationFrom: at.Add(-time.Hour), PrecipitationTo: at, PrecipitationMM: h.Precipitation[i], PrecipitationProb: h.Probability[i]}
		if !validPoint(point) {
			return nil, httpx.ErrInvalidResponse
		}
		points = append(points, point)
	}
	return points, nil
}
