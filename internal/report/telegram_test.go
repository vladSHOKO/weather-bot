package report

import (
	"strings"
	"testing"
	"time"
	"weather-sum/internal/location"
	"weather-sum/internal/weather"
)

func TestReportUnknownAndLocalized(t *testing.T) {
	v := weather.WeatherResult{Location: location.Location{Name: "Москва", Timezone: "Europe/Moscow"}, Period: weather.ForecastPeriod{From: time.Now(), To: time.Now().Add(time.Hour)}, Sources: []weather.SourceResult{{SourceName: "provider", State: "нет данных"}}, Umbrella: weather.UmbrellaAdvice{Text: "Нужен ли зонт — пока неясно", Reason: "Нет данных"}}
	text := NewTelegramRenderer().RenderDetails(v)
	for _, want := range []string{"нет данных", "Москва", "0 из 1", "Europe/Moscow"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	for _, bad := range []string{"false", "true", "optional", "0.0 °C"} {
		if strings.Contains(text, bad) {
			t.Fatal(text)
		}
	}
}

func TestForecastStatesAreConsistent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rain         weather.RainSummary
		want, absent string
	}{
		{"dry", weather.RainSummary{Known: true, Complete: true}, "Существенный риск осадков не обнаружен", "Осадки возможны"},
		{"partial", weather.RainSummary{Known: true}, "Данные об осадках неполные", "Существенный риск осадков не обнаружен"},
		{"disagreement", weather.RainSummary{Known: true, Complete: true, HasRain: true, Disagreement: true}, "Источники расходятся в оценке осадков", "Существенный риск осадков не обнаружен"},
		{"unknown", weather.RainSummary{}, "Осадки: нет данных", "Существенный риск осадков не обнаружен"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := weather.WeatherResult{Rain: tc.rain, Temperature: weather.TemperatureSummary{Available: true, MinC: -5, MaxC: 5}}
			text := NewTelegramRenderer().RenderDetails(v)
			if !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) {
				t.Fatal(text)
			}
		})
	}
}

func TestNewSourceAttributionAndLocalIntervals(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Moscow")
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	v := weather.WeatherResult{
		Period:  weather.ForecastPeriod{From: at.In(zone), To: at.Add(time.Hour).In(zone)},
		Sources: []weather.SourceResult{{SourceName: "MET Norway", IsAvailable: true}, {SourceName: "WeatherAPI.com", IsAvailable: true}},
		Rain:    weather.RainSummary{Known: true, HasRain: true, Intervals: []weather.RainInterval{{Start: at, End: at.Add(time.Hour), Risk: weather.RainRiskLow}}},
	}
	text := NewTelegramRenderer().RenderDetails(v)
	for _, want := range []string{"12:00–13:00", "https://www.met.no/", "https://www.weatherapi.com/", "https://creativecommons.org/licenses/by/4.0/", "Сводка бота"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing attribution or local time", want, text)
		}
	}
}

func TestCompactSummaryKeepsOnlyUsefulInformation(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Moscow")
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, zone)
	v := weather.WeatherResult{
		Location:    location.Location{Name: "Москва", Timezone: "Europe/Moscow"},
		Period:      weather.ForecastPeriod{From: at, To: at.Add(12 * time.Hour)},
		Temperature: weather.TemperatureSummary{Available: true, MinC: 4, MaxC: 8},
		Umbrella:    weather.UmbrellaAdvice{Text: "Зонт лучше взять", Reason: "Есть высокий риск дождя"},
		Sources:     []weather.SourceResult{{SourceName: "MET Norway", IsAvailable: true, State: "доступен"}, {SourceName: "other", State: "недоступен"}},
		Rain: weather.RainSummary{Known: true, Complete: true, HasRain: true, Intervals: []weather.RainInterval{
			{Start: at.Add(3 * time.Hour), End: at.Add(4 * time.Hour), Risk: weather.RainRiskLow},
			{Start: at.Add(4 * time.Hour), End: at.Add(6 * time.Hour), Risk: weather.RainRiskHigh},
		}},
	}
	renderer := NewTelegramRenderer()
	text := renderer.Render(v)
	for _, want := range []string{"📍 <b>Москва</b> · сегодня до полуночи", "🌡 +4…+8 °C", "🌧 Осадки возможны: 15:00–18:00", "☂️ Зонт лучше взять", `href="https://www.met.no/"`, `href="https://creativecommons.org/licenses/by/4.0/"`} {
		if !strings.Contains(text, want) {
			t.Fatal("missing summary content", want, text)
		}
	}
	for _, absent := range []string{"Источники:", "Europe/Moscow", "Покрытие", "границ", "высокий риск", "недоступен", "неопределённость"} {
		if strings.Contains(text, absent) {
			t.Fatal("diagnostics in compact summary", absent, text)
		}
	}
	details := renderer.RenderDetails(v)
	for _, want := range []string{"Источники: 1 из 2", "Europe/Moscow", "недоступен", "высокий риск"} {
		if !strings.Contains(details, want) {
			t.Fatal("diagnostics missing in details", want)
		}
	}
}
func TestCompactSummaryUnknownsAndUncertainty(t *testing.T) {
	for _, tc := range []struct {
		rain         weather.RainSummary
		want, absent string
	}{
		{weather.RainSummary{}, "Нет данных об осадках", "Осадков не ожидается"},
		{weather.RainSummary{Known: true}, "По имеющимся данным — без осадков", "Осадков не ожидается"},
		{weather.RainSummary{Known: true, Complete: true}, "Осадков не ожидается", "неопределённость"},
		{weather.RainSummary{Known: true, Complete: true, HasRain: true, Disagreement: true}, "По осадкам есть неопределённость", "Осадков не ожидается"},
	} {
		text := NewTelegramRenderer().Render(weather.WeatherResult{Rain: tc.rain})
		if !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) || strings.Contains(text, "0 °C") {
			t.Fatal(text)
		}
	}
}
func TestCompactWindowsKeepDryGapsAndLocalTime(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Moscow")
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	v := weather.WeatherResult{Period: weather.ForecastPeriod{From: at.In(zone), To: at.Add(12 * time.Hour).In(zone)}}
	for _, hour := range []int{0, 2, 4, 6} {
		v.Rain.Intervals = append(v.Rain.Intervals, weather.RainInterval{Start: at.Add(time.Duration(hour) * time.Hour), End: at.Add(time.Duration(hour+1) * time.Hour)})
	}
	if got := rainWindows(v); got != "12:00–13:00, 14:00–15:00, 16:00–17:00, и позже" {
		t.Fatal(got)
	}
}
func TestWeatherHTMLEscapesExternalText(t *testing.T) {
	v := weather.WeatherResult{Location: location.Location{Name: "<b>A&B</b>\nГород"}, Umbrella: weather.UmbrellaAdvice{Text: "<a href='bad'>Зонт</a>", Reason: "A&B"}, Sources: []weather.SourceResult{{SourceName: "<evil>", State: "<script>", IsAvailable: true}}}
	r := NewTelegramRenderer()
	for _, text := range []string{r.Render(v), r.RenderDetails(v)} {
		if strings.Contains(text, "<evil>") || strings.Contains(text, "<script>") || strings.Contains(text, "<b>A&B</b>") || strings.Contains(text, "<a href='bad'>") {
			t.Fatal("unescaped provider data", text)
		}
		if !strings.Contains(text, "&lt;b&gt;A&amp;B&lt;/b&gt; Город") {
			t.Fatal("location not escaped", text)
		}
	}
	for _, tc := range []struct {
		value float64
		want  string
	}{{0, "0"}, {0.02, "0"}, {-0.02, "0"}, {-3, "-3"}, {4.55, "+4.6"}} {
		if got := temperature(tc.value); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestTomorrowSummaryShowsTargetDate(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Moscow")
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, zone)
	result := weather.WeatherResult{Day: weather.Tomorrow, Location: location.Location{Name: "Москва", Timezone: "Europe/Moscow"}, Period: weather.ForecastPeriod{From: from, To: from.AddDate(0, 0, 1)}, GeneratedAt: from.Add(-time.Hour)}
	r := NewTelegramRenderer()
	text := r.Render(result)
	if !strings.Contains(text, "📍 <b>Москва</b> · завтра, 20.10") || strings.Contains(text, "сегодня") {
		t.Fatal(text)
	}
	details := r.RenderDetails(result)
	if !strings.Contains(details, "20.10.2026 с 00:00 до 21.10 00:00") {
		t.Fatal(details)
	}
}
