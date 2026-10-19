package report

import (
	"fmt"
	"html"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"weather-sum/internal/weather"
)

type TelegramRenderer struct{}

func NewTelegramRenderer() *TelegramRenderer { return &TelegramRenderer{} }
func (r *TelegramRenderer) RenderDetails(v weather.WeatherResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Локация: %s\nПрогноз: %s с %s до %s (%s)\n", escape(v.Location.Name), v.Period.From.Format("02.01.2006"), v.Period.From.Format("15:04"), v.Period.To.Format("02.01 15:04"), escape(v.Location.Timezone))
	if v.Temperature.Available {
		fmt.Fprintf(&b, "Температура: от %.1f до %.1f °C\n", v.Temperature.MinC, v.Temperature.MaxC)
	} else {
		b.WriteString("Температура: нет данных\n")
	}
	available := 0
	for _, s := range v.Sources {
		if s.IsAvailable {
			available++
		}
	}
	fmt.Fprintf(&b, "Источники: %d из %d\n", available, len(v.Sources))
	for _, s := range v.Sources {
		fmt.Fprintf(&b, "• %s: %s\n", escape(s.SourceName), escape(s.State))
	}
	if !v.Rain.Known {
		b.WriteString("Осадки: нет данных\n")
	} else if v.Rain.HasRain {
		b.WriteString("Осадки возможны\n")
	} else if v.Rain.Complete {
		b.WriteString("Существенный риск осадков не обнаружен\n")
	} else {
		b.WriteString("Данные об осадках неполные\n")
	}
	if v.Rain.Disagreement {
		b.WriteString("Источники расходятся в оценке осадков\n")
	}
	labels := map[weather.RainRiskLevel]string{weather.RainRiskLow: "небольшой риск", weather.RainRiskMedium: "умеренный риск", weather.RainRiskHigh: "высокий риск"}
	for i, p := range v.Rain.Intervals {
		if i >= 12 {
			b.WriteString("Другие интервалы объединены в общую оценку\n")
			break
		}
		fmt.Fprintf(&b, "%s–%s: %s (%s)", p.Start.In(v.Period.From.Location()).Format("15:04"), p.End.In(v.Period.From.Location()).Format("15:04"), labels[p.Risk], escape(strings.Join(p.Sources, ", ")))
		if p.Partial {
			b.WriteString("; исходный интервал пересекает границу прогноза")
		}
		b.WriteByte('\n')
	}
	if !v.Rain.Complete {
		b.WriteString("Покрытие периода неполное; границы риска приблизительны\n")
	}
	fmt.Fprintf(&b, "%s. %s.\n", escape(v.Umbrella.Text), escape(v.Umbrella.Reason))
	b.WriteString(attribution(v))
	return b.String()
}

// Render is the everyday summary. Diagnostics belong in RenderDetails.
func (r *TelegramRenderer) Render(v weather.WeatherResult) string {
	var b strings.Builder
	if v.Day == weather.Tomorrow {
		fmt.Fprintf(&b, "📍 <b>%s</b> · завтра, %s\n", escape(v.Location.Name), v.Period.From.Format("02.01"))
	} else {
		fmt.Fprintf(&b, "📍 <b>%s</b> · сегодня до полуночи\n", escape(v.Location.Name))
	}
	if v.Temperature.Available {
		low, high := temperature(v.Temperature.MinC), temperature(v.Temperature.MaxC)
		if low == high {
			fmt.Fprintf(&b, "🌡 %s °C\n", low)
		} else {
			fmt.Fprintf(&b, "🌡 %s…%s °C\n", low, high)
		}
	} else {
		b.WriteString("🌡 Нет данных о температуре\n")
	}
	switch {
	case !v.Rain.Known:
		b.WriteString("🌧 Нет данных об осадках\n")
	case v.Rain.HasRain:
		b.WriteString("🌧 Осадки возможны")
		if windows := rainWindows(v); windows != "" {
			b.WriteString(": " + windows)
		}
		b.WriteByte('\n')
	case v.Rain.Complete:
		b.WriteString("🌤 Осадков не ожидается\n")
	default:
		b.WriteString("🌦 По имеющимся данным — без осадков\n")
	}
	advice := strings.TrimSpace(v.Umbrella.Text)
	if advice == "" {
		advice = "Нужен ли зонт — пока неясно"
	}
	fmt.Fprintf(&b, "☂️ %s\n", escape(advice))
	if v.Rain.Known && (!v.Rain.Complete || v.Rain.Disagreement) {
		b.WriteString("⚠️ По осадкам есть неопределённость\n")
	}
	if footer := attribution(v); footer != "" {
		b.WriteByte('\n')
		b.WriteString(footer)
	}
	return strings.TrimSpace(b.String())
}
func temperature(value float64) string {
	value = math.Round(value*10) / 10
	if value == 0 {
		return "0"
	}
	result := strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0")
	if value > 0 {
		result = "+" + result
	}
	return result
}
func rainWindows(v weather.WeatherResult) string {
	// Adjacent intervals often differ only in risk or contributing sources.
	// Join them for the summary, preserving actual dry gaps.
	var windows []weather.ForecastPeriod
	for _, interval := range v.Rain.Intervals {
		start, end := interval.Start, interval.End
		if start.Before(v.Period.From) {
			start = v.Period.From
		}
		if end.After(v.Period.To) {
			end = v.Period.To
		}
		if end.After(start) {
			windows = append(windows, weather.ForecastPeriod{From: start, To: end})
		}
	}
	slices.SortFunc(windows, func(a, b weather.ForecastPeriod) int { return a.From.Compare(b.From) })
	var merged []weather.ForecastPeriod
	for _, window := range windows {
		if len(merged) > 0 && !window.From.After(merged[len(merged)-1].To) {
			if window.To.After(merged[len(merged)-1].To) {
				merged[len(merged)-1].To = window.To
			}
		} else {
			merged = append(merged, window)
		}
	}
	var labels []string
	zone := v.Period.From.Location()
	for i, window := range merged {
		if i == 3 {
			labels = append(labels, "и позже")
			break
		}
		labels = append(labels, clockTime(window.From, zone)+"–"+clockTime(window.To, zone))
	}
	return strings.Join(labels, ", ")
}
func clockTime(at time.Time, zone *time.Location) string { return at.In(zone).Format("15:04") }
func attribution(v weather.WeatherResult) string {
	var names []string
	cc := false
	for _, source := range v.Sources {
		if !source.IsAvailable {
			continue
		}
		switch source.SourceName {
		case "Open-Meteo":
			names = append(names, `<a href="https://open-meteo.com/">Open-Meteo</a>`)
			cc = true
		case "MET Norway":
			names = append(names, `<a href="https://www.met.no/">MET Norway</a>`)
			cc = true
		case "WeatherAPI.com":
			names = append(names, `<a href="https://www.weatherapi.com/">WeatherAPI</a>`)
		case "OpenWeather":
			names = append(names, `<a href="https://openweathermap.org/">OpenWeather</a>`)
		default:
			names = append(names, escape(source.SourceName))
		}
	}
	if len(names) == 0 {
		return ""
	}
	if cc {
		names = append(names, `<a href="https://creativecommons.org/licenses/by/4.0/">CC BY 4.0</a>`)
	}
	return "<i>Сводка бота · " + strings.Join(names, " · ") + "</i>\n"
}

func escape(text string) string {
	return html.EscapeString(strings.NewReplacer("\r", " ", "\n", " ").Replace(text))
}
