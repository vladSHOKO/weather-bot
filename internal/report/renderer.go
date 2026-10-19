package report

import "weather-sum/internal/weather"

// Renderer returns Telegram HTML with self-contained markup on each line.
type Renderer interface {
	Render(result weather.WeatherResult) string
	RenderDetails(result weather.WeatherResult) string
}
