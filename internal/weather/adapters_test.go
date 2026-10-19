package weather

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func response(body string) *http.Response {
	return &http.Response{Body: io.NopCloser(strings.NewReader(body))}
}
func TestMeteoNullsAndDSTEpoch(t *testing.T) {
	p := NewOpenMeteoProvider(nil)
	loc := city
	loc.Timezone = "Europe/Berlin"
	a := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC).Unix()
	body := fmt.Sprintf(`{"hourly":{"time":[%d,%d],"temperature_2m":[null,0],"precipitation":[null,0],"precipitation_probability":[null,0]}}`, a, a+3600)
	points, err := p.parseForecastPoints(response(body), loc)
	if err != nil || len(points) != 2 || points[0].TemperatureC != nil || points[1].TemperatureC == nil || !points[1].Time.After(points[0].Time) {
		t.Fatalf("%+v %v", points, err)
	}
	if points[0].Time.Format("15:04") != points[1].Time.Format("15:04") {
		t.Fatal("test should cover repeated local hour")
	}
}
func TestMalformedForecasts(t *testing.T) {
	for _, body := range []string{`null`, `{"hourly":{"time":[null],"temperature_2m":[0],"precipitation":[0],"precipitation_probability":[0]}}`, `{"hourly":{"time":[1700000000],"temperature_2m":[],"precipitation":[0],"precipitation_probability":[0]}}`, `{"hourly":{"time":[1700000000],"temperature_2m":[0],"precipitation":[0],"precipitation_probability":[101]}}`} {
		if _, err := NewOpenMeteoProvider(nil).parseForecastPoints(response(body), city); err == nil {
			t.Fatal("invalid meteo accepted")
		}
	}
	for _, body := range []string{`null`, `{"list":[{"dt":null}]}`, `{"list":[{"dt":1700000000,"pop":1.1}]}`, `{"list":[{"dt":1700000000,"rain":{"3h":-1}}]}`} {
		if _, err := NewOpenWeatherProvider(nil, "").parseForecastPoints(response(body), city); err == nil {
			t.Fatal("invalid OpenWeather accepted")
		}
	}
}
func TestOpenWeatherAccumulationAndUnknown(t *testing.T) {
	body := `{"list":[{"dt":1700000000,"main":{"temp":null},"pop":null},{"dt":1700010800,"main":{"temp":-5},"pop":0.8,"rain":{"3h":1},"snow":{"3h":0.5}}]}`
	points, err := NewOpenWeatherProvider(nil, "").parseForecastPoints(response(body), city)
	if err != nil || points[0].PrecipitationProb != nil || points[0].TemperatureC != nil || *points[1].PrecipitationProb != 80 || *points[1].PrecipitationMM != 1.5 || points[1].PrecipitationTo.Sub(points[1].PrecipitationFrom) != 3*time.Hour {
		t.Fatalf("%+v %v", points, err)
	}
}
