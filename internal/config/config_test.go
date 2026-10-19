package config

import (
	"strings"
	"testing"
)

func base(t *testing.T) {
	t.Helper()
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:synthetic")
	t.Setenv("DATABASE_URL", "postgres://synthetic")
	t.Setenv("MET_NO_USER_AGENT", "")
	t.Setenv("WEATHERAPI_API_KEY", "")
	for _, k := range []string{"HTTP_TIMEOUT", "DB_TIMEOUT", "POLL_TIMEOUT", "SEND_TIMEOUT", "COMMAND_TIMEOUT", "SHUTDOWN_TIMEOUT", "CACHE_TTL", "CHAT_COOLDOWN", "WORKERS", "QUEUE_CAPACITY", "CACHE_CAPACITY", "MAX_ATTEMPTS", "REQUESTS_PER_SECOND", "HEALTH_ADDR"} {
		t.Setenv(k, defaultFor(k))
	}
}
func defaultFor(k string) string {
	switch k {
	case "WORKERS":
		return "4"
	case "QUEUE_CAPACITY":
		return "1000"
	case "CACHE_CAPACITY":
		return "128"
	case "MAX_ATTEMPTS":
		return "3"
	case "REQUESTS_PER_SECOND":
		return "5"
	case "HEALTH_ADDR":
		return "127.0.0.1:8080"
	default:
		return "5s"
	}
}
func TestValidation(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"HTTP_TIMEOUT", "0s"}, {"HTTP_TIMEOUT", "-1s"}, {"HTTP_TIMEOUT", "broken-SYNTHETIC_SECRET"}, {"HTTP_TIMEOUT", "2m"}, {"DATABASE_URL", ""}, {"TELEGRAM_BOT_TOKEN", ""}, {"TELEGRAM_BOT_TOKEN", "x/y"}, {"QUEUE_CAPACITY", "99"}, {"WORKERS", "33"}, {"HEALTH_ADDR", "0.0.0.0:8080"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			base(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC_SECRET") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
}
func TestOptionalWeatherKey(t *testing.T) {
	base(t)
	t.Setenv("OPENWEATHER_API_KEY", "")
	c, err := Load()
	if err != nil || c.OpenWeatherAPIKey != "" {
		t.Fatalf("%v %v", c, err)
	}
}

func TestOptionalNewWeatherSources(t *testing.T) {
	base(t)
	t.Setenv("WEATHERAPI_API_KEY", " synthetic-weatherapi ")
	t.Setenv("MET_NO_USER_AGENT", "weather-sum/1.0 https://example.org/contact")
	c, err := Load()
	if err != nil || c.WeatherAPIKey != "synthetic-weatherapi" || c.MetNoUserAgent == "" {
		t.Fatalf("configuration: %+v %v", c, err)
	}
	for _, ua := range []string{"generic", "weather-sum/1.0 x@example.org\r\nInjected: value", strings.Repeat("x", 513) + "@example.org"} {
		t.Setenv("MET_NO_USER_AGENT", ua)
		if _, err := Load(); err == nil {
			t.Fatal("invalid User-Agent accepted")
		}
	}
}
