package weather

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
)

var ErrNoData = errors.New("no usable forecast data")

type Service struct {
	providers []Provider
	now       func() time.Time
}

func NewService(providers ...Provider) *Service { return &Service{providers: providers, now: time.Now} }
func (s *Service) GetTodayWeather(ctx context.Context, loc location.Location) (WeatherResult, error) {
	return s.getDayWeather(ctx, loc, Today)
}
func (s *Service) GetTomorrowWeather(ctx context.Context, loc location.Location) (WeatherResult, error) {
	return s.getDayWeather(ctx, loc, Tomorrow)
}
func (s *Service) getDayWeather(ctx context.Context, loc location.Location, day ForecastDay) (WeatherResult, error) {
	if err := loc.Validate(); err != nil {
		return WeatherResult{}, err
	}
	zone, _ := time.LoadLocation(loc.Timezone)
	now := s.now().In(zone)
	period := ForecastPeriod{From: now, To: time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, zone)}
	if day == Tomorrow {
		period.From = period.To
		period.To = period.From.AddDate(0, 0, 1)
	}
	result := WeatherResult{Day: day, Location: loc, Period: period, GeneratedAt: now, Sources: make([]SourceResult, len(s.providers))}
	var wg sync.WaitGroup
	for i, p := range s.providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			points, err := p.Forecast(ctx, loc, period)
			source := SourceResult{SourceName: p.Name(), State: "недоступен"}
			if err != nil {
				if httpx.Retryable(err) {
					source.State = "временно недоступен"
				} else if errors.Is(err, ErrNoData) {
					source.State = "нет данных"
				} else {
					source.State = "ошибка источника"
				}
			}
			if err == nil {
				source = aggregateSource(p.Name(), points, period)
			}
			result.Sources[i] = source
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Temperature = temperatureSummary(result.Sources)
	result.Rain = rainSummary(result.Sources, period)
	available := 0
	for _, source := range result.Sources {
		if source.IsAvailable {
			available++
		}
	}
	if available == 0 {
		return result, ErrNoData
	}
	result.Umbrella = umbrellaAdvice(result.Rain)
	return result, nil
}
func validPoint(p ForecastPoint) bool {
	if p.Time.IsZero() {
		return false
	}
	if p.TemperatureC != nil && (math.IsNaN(*p.TemperatureC) || math.IsInf(*p.TemperatureC, 0) || *p.TemperatureC < -100 || *p.TemperatureC > 70) {
		return false
	}
	if p.PrecipitationMM != nil && (math.IsNaN(*p.PrecipitationMM) || math.IsInf(*p.PrecipitationMM, 0) || *p.PrecipitationMM < 0) {
		return false
	}
	if p.PrecipitationProb != nil && (*p.PrecipitationProb < 0 || *p.PrecipitationProb > 100) {
		return false
	}
	if (p.PrecipitationMM != nil || p.PrecipitationProb != nil) && !p.PrecipitationTo.After(p.PrecipitationFrom) {
		return false
	}
	return true
}
func overlaps(p ForecastPoint, period ForecastPeriod) bool {
	return p.PrecipitationFrom.Before(period.To) && p.PrecipitationTo.After(period.From)
}
func aggregateSource(name string, points []ForecastPoint, period ForecastPeriod) SourceResult {
	source := SourceResult{SourceName: name, State: "нет данных"}
	for _, p := range points {
		if !validPoint(p) {
			source.Partial = true
			continue
		}
		tempInside := !p.Time.Before(period.From) && p.Time.Before(period.To)
		rainInside := overlaps(p, period)
		if !tempInside && !rainInside {
			continue
		}
		q := p
		if !tempInside {
			q.TemperatureC = nil
		}
		if !rainInside {
			q.PrecipitationMM = nil
			q.PrecipitationProb = nil
		}
		if q.TemperatureC == nil && q.PrecipitationMM == nil && q.PrecipitationProb == nil {
			continue
		}
		source.ForecastPoints = append(source.ForecastPoints, q)
		if q.TemperatureC != nil {
			v := *q.TemperatureC
			if !source.HasTemperature {
				source.MinTemperatureC = v
				source.MaxTemperatureC = v
				source.HasTemperature = true
			}
			source.MinTemperatureC = min(source.MinTemperatureC, v)
			source.MaxTemperatureC = max(source.MaxTemperatureC, v)
		}
		if rainInside && rank(pointRisk(q)) > 0 {
			source.RainDetected = true
		}
	}
	source.IsAvailable = len(source.ForecastPoints) > 0
	if source.IsAvailable {
		source.State = "доступен"
	}
	return source
}
func temperatureSummary(sources []SourceResult) TemperatureSummary {
	var result TemperatureSummary
	for _, s := range sources {
		if !s.IsAvailable || !s.HasTemperature {
			continue
		}
		if !result.Available {
			result.MinC = s.MinTemperatureC
			result.MaxC = s.MaxTemperatureC
			result.Available = true
		}
		result.MinC = min(result.MinC, s.MinTemperatureC)
		result.MaxC = max(result.MaxC, s.MaxTemperatureC)
	}
	return result
}

// Thresholds apply to hourly intensity, not equal volumes with different duration.
func pointRisk(p ForecastPoint) RainRiskLevel {
	intensity := 0.0
	prob := 0
	if p.PrecipitationMM != nil {
		hours := p.PrecipitationTo.Sub(p.PrecipitationFrom).Hours()
		if hours > 0 {
			intensity = *p.PrecipitationMM / hours
		}
	}
	if p.PrecipitationProb != nil {
		prob = *p.PrecipitationProb
	}
	switch {
	case intensity >= 1 || prob >= 70:
		return RainRiskHigh
	case intensity >= 0.2 || prob >= 40:
		return RainRiskMedium
	case intensity > 0 || prob >= 20:
		return RainRiskLow
	default:
		return RainRiskNone
	}
}
func rank(r RainRiskLevel) int {
	switch r {
	case RainRiskLow:
		return 1
	case RainRiskMedium:
		return 2
	case RainRiskHigh:
		return 3
	}
	return 0
}
func rainSummary(sources []SourceResult, period ForecastPeriod) RainSummary {
	result := RainSummary{Risk: RainRiskNone, Complete: len(sources) > 0}
	var intervals []RainInterval
	dry := 0
	for _, s := range sources {
		if !s.IsAvailable {
			result.Complete = false
			continue
		}
		var coverage []ForecastPeriod
		rainy := false
		known := false
		for _, p := range s.ForecastPoints {
			if !overlaps(p, period) {
				continue
			}
			if p.PrecipitationMM == nil && p.PrecipitationProb == nil {
				continue
			}
			known = true
			start := p.PrecipitationFrom
			end := p.PrecipitationTo
			partial := start.Before(period.From) || end.After(period.To)
			start = maxTime(start, period.From)
			end = minTime(end, period.To)
			coverage = append(coverage, ForecastPeriod{From: start, To: end})
			risk := pointRisk(p)
			if rank(risk) > 0 {
				rainy = true
				intervals = append(intervals, RainInterval{Start: start, End: end, Risk: risk, Sources: []string{s.SourceName}, Partial: partial})
				if rank(risk) > rank(result.Risk) {
					result.Risk = risk
				}
			}
		}
		if !known {
			result.Complete = false
			continue
		}
		result.Known = true
		result.SourcesCount++
		if rainy {
			result.RainySources++
		} else {
			dry++
		}
		if !covers(coverage, period) || s.Partial {
			result.Complete = false
		}
	}
	result.HasRain = result.RainySources > 0
	result.Disagreement = result.HasRain && dry > 0
	result.Intervals = mergeIntervals(intervals)
	return result
}
func covers(points []ForecastPeriod, p ForecastPeriod) bool {
	slices.SortFunc(points, func(a, b ForecastPeriod) int { return a.From.Compare(b.From) })
	end := p.From
	for _, v := range points {
		if v.From.After(end) {
			return false
		}
		if v.To.After(end) {
			end = v.To
		}
	}
	return !end.Before(p.To)
}
func mergeIntervals(points []RainInterval) []RainInterval {
	// Sweep boundaries preserves risk changes instead of stretching the highest risk.
	var bounds []time.Time
	for _, p := range points {
		bounds = append(bounds, p.Start, p.End)
	}
	slices.SortFunc(bounds, func(a, b time.Time) int { return a.Compare(b) })
	bounds = slices.CompactFunc(bounds, func(a, b time.Time) bool { return a.Equal(b) })
	var out []RainInterval
	for i := 0; i+1 < len(bounds); i++ {
		v := RainInterval{Start: bounds[i], End: bounds[i+1], Risk: RainRiskNone}
		names := map[string]bool{}
		for _, p := range points {
			if p.Start.Before(v.End) && p.End.After(v.Start) {
				if rank(p.Risk) > rank(v.Risk) {
					v.Risk = p.Risk
				}
				v.Partial = v.Partial || p.Partial
				for _, name := range p.Sources {
					names[name] = true
				}
			}
		}
		if rank(v.Risk) == 0 {
			continue
		}
		for name := range names {
			v.Sources = append(v.Sources, name)
		}
		slices.Sort(v.Sources)
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.End.Equal(v.Start) && last.Risk == v.Risk && last.Partial == v.Partial && slices.Equal(last.Sources, v.Sources) {
				last.End = v.End
				continue
			}
		}
		out = append(out, v)
	}
	return out
}
func umbrellaAdvice(r RainSummary) UmbrellaAdvice {
	if !r.Known {
		return UmbrellaAdvice{Level: UmbrellaOptional, Text: "Не удалось определить, нужен ли зонт", Reason: "Нет данных об осадках"}
	}
	reason := "Существенный риск дождя не обнаружен"
	level := UmbrellaNo
	text := "Зонт, скорее всего, не понадобится"
	switch r.Risk {
	case RainRiskHigh:
		level = UmbrellaYes
		text = "Зонт лучше взять"
		reason = "Есть высокий риск дождя"
	case RainRiskMedium:
		level = UmbrellaOptional
		text = "Зонт стоит взять при длительной прогулке"
		reason = "Есть умеренный риск дождя"
	case RainRiskLow:
		level = UmbrellaOptional
		text = "Зонт можно взять на всякий случай"
		reason = "Есть небольшой риск дождя"
	}
	if !r.Complete && level == UmbrellaNo {
		level = UmbrellaOptional
		text = "Нужен ли зонт — пока неясно"
		reason = "Недостаточно данных на весь период"
	}
	if r.Disagreement {
		reason += "; источники расходятся"
	}
	if !r.Complete && !strings.Contains(reason, "Недостаточно") {
		reason += "; данные неполные"
	}
	return UmbrellaAdvice{Level: level, Text: text, Reason: reason}
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
