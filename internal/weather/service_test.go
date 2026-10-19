package weather

import (
	"context"
	"errors"
	"testing"
	"time"
	"weather-sum/internal/location"
)

type fakeProvider struct {
	name   string
	points []ForecastPoint
	err    error
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Forecast(context.Context, location.Location, ForecastPeriod) ([]ForecastPoint, error) {
	return f.points, f.err
}
func ptr[T any](v T) *T { return &v }

var at = time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)
var city = location.Location{Name: "Test", Latitude: 55, Longitude: 37, Timezone: "UTC"}

func hourly(temp float64, prob int) []ForecastPoint {
	var out []ForecastPoint
	for i := 1; i <= 7; i++ {
		end := at.Add(time.Duration(i) * time.Hour)
		out = append(out, ForecastPoint{Time: end, TemperatureC: ptr(temp), PrecipitationFrom: end.Add(-time.Hour), PrecipitationTo: end, PrecipitationProb: ptr(prob), PrecipitationMM: ptr(0.0)})
	}
	return out
}
func service(p ...Provider) *Service {
	s := NewService(p...)
	s.now = func() time.Time { return at }
	return s
}
func TestPartialFailureAndEmpty(t *testing.T) {
	good := fakeProvider{name: "good", points: hourly(-5, 0)}
	bad := fakeProvider{name: "bad", err: errors.New("down")}
	v, err := service(good, bad).GetTodayWeather(context.Background(), city)
	if err != nil || !v.Sources[0].IsAvailable || v.Sources[1].IsAvailable || v.Rain.Complete {
		t.Fatalf("partial forecast: %+v %v", v, err)
	}
}
func TestNoData(t *testing.T) {
	for _, s := range []*Service{service(), service(fakeProvider{name: "empty"}), service(fakeProvider{name: "down", err: errors.New("down")})} {
		_, err := s.GetTodayWeather(context.Background(), city)
		if !errors.Is(err, ErrNoData) {
			t.Fatal(err)
		}
	}
}
func TestTemperatureAndPast(t *testing.T) {
	for _, tc := range []struct {
		values   []float64
		min, max float64
	}{{[]float64{0, 5}, 0, 5}, {[]float64{-10, -5}, -10, -5}, {[]float64{-5, 0, -2}, -5, 0}, {[]float64{4}, 4, 4}} {
		points := hourly(0, 0)[:len(tc.values)]
		for i, v := range tc.values {
			points[i].TemperatureC = ptr(v)
		}
		past := ForecastPoint{Time: at.Add(-12 * time.Hour), TemperatureC: ptr(-99.0)}
		points = append(points, past)
		v, err := service(fakeProvider{name: "p", points: points}).GetTodayWeather(context.Background(), city)
		if err != nil || v.Temperature.MinC != tc.min || v.Temperature.MaxC != tc.max {
			t.Fatalf("%v: %+v %v", tc.values, v.Temperature, err)
		}
	}
}
func TestRainAgreement(t *testing.T) {
	for _, tc := range []struct {
		a, b           int
		rain, disagree bool
		level          UmbrellaAdviceLevel
	}{{1, 1, false, false, UmbrellaNo}, {80, 0, true, true, UmbrellaYes}, {0, 0, false, false, UmbrellaNo}, {40, 40, true, false, UmbrellaOptional}} {
		v, err := service(fakeProvider{name: "a", points: hourly(5, tc.a)}, fakeProvider{name: "b", points: hourly(6, tc.b)}).GetTodayWeather(context.Background(), city)
		if err != nil || v.Rain.HasRain != tc.rain || v.Rain.Disagreement != tc.disagree || v.Umbrella.Level != tc.level {
			t.Fatalf("%+v: %+v %v", tc, v.Rain, err)
		}
	}
}
func TestRiskThresholdsAndDuration(t *testing.T) {
	for _, tc := range []struct {
		prob  int
		mm    float64
		hours int
		want  RainRiskLevel
	}{{19, 0, 1, RainRiskNone}, {20, 0, 1, RainRiskLow}, {21, 0, 1, RainRiskLow}, {39, 0, 1, RainRiskLow}, {40, 0, 1, RainRiskMedium}, {41, 0, 1, RainRiskMedium}, {69, 0, 1, RainRiskMedium}, {70, 0, 1, RainRiskHigh}, {71, 0, 1, RainRiskHigh}, {0, 0.19, 1, RainRiskLow}, {0, 0.2, 1, RainRiskMedium}, {0, 0.21, 1, RainRiskMedium}, {0, 0.99, 1, RainRiskMedium}, {0, 1, 1, RainRiskHigh}, {0, 1.01, 1, RainRiskHigh}, {0, 1, 3, RainRiskMedium}} {
		p := ForecastPoint{PrecipitationFrom: at, PrecipitationTo: at.Add(time.Duration(tc.hours) * time.Hour), PrecipitationProb: ptr(tc.prob), PrecipitationMM: ptr(tc.mm)}
		if pointRisk(p) != tc.want {
			t.Fatalf("%+v", tc)
		}
	}
}
func TestUnknownAndBoundaryInterval(t *testing.T) {
	points := []ForecastPoint{{Time: at.Add(time.Hour), TemperatureC: ptr(5.0)}}
	v, err := service(fakeProvider{name: "temp", points: points}).GetTodayWeather(context.Background(), city)
	if err != nil || v.Rain.Known || v.Umbrella.Level != UmbrellaOptional {
		t.Fatalf("%+v %v", v, err)
	}
	p := ForecastPoint{Time: at.Add(time.Hour), PrecipitationFrom: at.Add(-2 * time.Hour), PrecipitationTo: at.Add(time.Hour), PrecipitationMM: ptr(3.0)}
	s := aggregateSource("a", []ForecastPoint{p}, ForecastPeriod{From: at, To: at.Add(2 * time.Hour)})
	r := rainSummary([]SourceResult{s}, ForecastPeriod{From: at, To: at.Add(2 * time.Hour)})
	if len(r.Intervals) != 1 || !r.Intervals[0].Partial || !r.Intervals[0].Start.Equal(at) || r.Complete {
		t.Fatalf("%+v", r)
	}
}
func TestDSTPeriodAndCancellation(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Berlin")
	s := service()
	s.now = func() time.Time { return time.Date(2026, 10, 25, 0, 0, 0, 0, zone) }
	l := city
	l.Timezone = "Europe/Berlin"
	v, _ := s.GetTodayWeather(context.Background(), l)
	if v.Period.To.Sub(v.Period.From) != 25*time.Hour {
		t.Fatal(v.Period)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service(fakeProvider{name: "a", points: hourly(5, 0)}).GetTodayWeather(ctx, city)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestMergeDoesNotSpreadHighRisk(t *testing.T) {
	points := []RainInterval{{Start: at, End: at.Add(3 * time.Hour), Risk: RainRiskLow, Sources: []string{"a"}}, {Start: at.Add(time.Hour), End: at.Add(2 * time.Hour), Risk: RainRiskHigh, Sources: []string{"b"}}}
	out := mergeIntervals(points)
	if len(out) != 3 || out[0].Risk != RainRiskLow || out[1].Risk != RainRiskHigh || out[2].Risk != RainRiskLow {
		t.Fatal(out)
	}
}

func TestProvidersShareLocalPeriod(t *testing.T) {
	for _, tc := range []struct {
		zone              string
		year              int
		month             time.Month
		day, hour, minute int
		length            time.Duration
	}{
		{"UTC", 2026, 10, 19, 0, 0, 24 * time.Hour},
		{"Asia/Kathmandu", 2026, 10, 19, 23, 59, time.Minute},
		{"Europe/Berlin", 2026, 3, 29, 0, 0, 23 * time.Hour},
		{"Europe/Berlin", 2026, 10, 25, 0, 0, 25 * time.Hour},
	} {
		t.Run(tc.zone+time.Date(tc.year, tc.month, tc.day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), func(t *testing.T) {
			zone, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(tc.year, tc.month, tc.day, tc.hour, tc.minute, 0, 0, zone)
			periods := make(chan ForecastPeriod, 2)
			p := providerFunc(func(_ context.Context, _ location.Location, period ForecastPeriod) ([]ForecastPoint, error) {
				periods <- period
				return []ForecastPoint{{Time: period.From, TemperatureC: ptr(0.0)}}, nil
			})
			s := service(p, p)
			s.now = func() time.Time { return now }
			loc := city
			loc.Timezone = tc.zone
			v, err := s.GetTodayWeather(context.Background(), loc)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				period := <-periods
				if !period.From.Equal(now) || period.To.Sub(period.From) != tc.length || !period.To.Equal(v.Period.To) {
					t.Fatalf("providers received different/wrong periods: %+v", period)
				}
			}
		})
	}
}

func TestAccumulationCrossingEndIsNotScaled(t *testing.T) {
	period := ForecastPeriod{From: at, To: at.Add(time.Hour)}
	p := ForecastPoint{Time: at.Add(2 * time.Hour), PrecipitationFrom: at.Add(-time.Hour), PrecipitationTo: at.Add(2 * time.Hour), PrecipitationMM: ptr(1.5)}
	s := aggregateSource("test", []ForecastPoint{p}, period)
	r := rainSummary([]SourceResult{s}, period)
	if len(r.Intervals) != 1 || !r.Intervals[0].Partial || !r.Intervals[0].End.Equal(period.To) || r.Risk != RainRiskMedium || *s.ForecastPoints[0].PrecipitationMM != 1.5 {
		t.Fatalf("boundary accumulation was rescaled: %+v", r)
	}
}

func TestTomorrowUsesWholeLocalCalendarDay(t *testing.T) {
	for _, tc := range []struct {
		zone   string
		now    time.Time
		length time.Duration
	}{
		{"UTC", time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), 24 * time.Hour},
		{"Asia/Kathmandu", time.Date(2026, 10, 19, 23, 59, 0, 0, time.UTC), 24 * time.Hour},
		{"Europe/Berlin", time.Date(2026, 3, 28, 15, 0, 0, 0, time.UTC), 23 * time.Hour},
		{"Europe/Berlin", time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC), 25 * time.Hour},
	} {
		t.Run(tc.zone+tc.now.Format("2006-01-02"), func(t *testing.T) {
			zone, _ := time.LoadLocation(tc.zone)
			now := tc.now.In(zone)
			tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, zone)
			periods := make(chan ForecastPeriod, 2)
			p := providerFunc(func(_ context.Context, _ location.Location, period ForecastPeriod) ([]ForecastPoint, error) {
				periods <- period
				return []ForecastPoint{
					{Time: tomorrow.Add(-time.Hour), TemperatureC: ptr(-99.0)},
					{Time: tomorrow, TemperatureC: ptr(-5.0)},
					{Time: tomorrow.Add(time.Hour), TemperatureC: ptr(5.0)},
					{Time: tomorrow.AddDate(0, 0, 1), TemperatureC: ptr(69.0)},
				}, nil
			})
			s := NewService(p, p)
			s.now = func() time.Time { return tc.now }
			loc := city
			loc.Timezone = tc.zone
			result, err := s.GetTomorrowWeather(context.Background(), loc)
			if err != nil {
				t.Fatal(err)
			}
			if result.Day != Tomorrow || !result.GeneratedAt.Equal(tc.now) || !result.Period.From.Equal(tomorrow) || result.Period.To.Sub(result.Period.From) != tc.length || result.Temperature.MinC != -5 || result.Temperature.MaxC != 5 {
				t.Fatalf("wrong tomorrow: %+v", result)
			}
			for range 2 {
				if got := <-periods; !got.From.Equal(result.Period.From) || !got.To.Equal(result.Period.To) {
					t.Fatal("providers received different day", got)
				}
			}
		})
	}
}
func TestTomorrowPartialFailureAndNoData(t *testing.T) {
	tomorrow := time.Date(at.Year(), at.Month(), at.Day()+1, 0, 0, 0, 0, time.UTC)
	good := fakeProvider{name: "good", points: []ForecastPoint{{Time: tomorrow, TemperatureC: ptr(0.0)}}}
	bad := fakeProvider{name: "bad", err: errors.New("unavailable")}
	result, err := service(good, bad).GetTomorrowWeather(context.Background(), city)
	if err != nil || !result.Sources[0].IsAvailable || result.Sources[1].IsAvailable || result.Rain.Known {
		t.Fatal("partial tomorrow lost", result, err)
	}
	if _, err := service(fakeProvider{name: "today only", points: hourly(5, 0)[:6]}).GetTomorrowWeather(context.Background(), city); !errors.Is(err, ErrNoData) {
		t.Fatal("today used as tomorrow", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service(good).GetTomorrowWeather(ctx, city); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored", err)
	}
}
