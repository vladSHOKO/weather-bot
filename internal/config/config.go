package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken, DatabaseURL, OpenWeatherAPIKey, WeatherAPIKey, MetNoUserAgent      string
	HTTPTimeout, DBTimeout, PollTimeout, SendTimeout, CommandTimeout, ShutdownTimeout time.Duration
	CacheTTL, ChatCooldown                                                            time.Duration
	Workers, QueueCapacity, CacheCapacity, MaxAttempts, RequestsPerSecond             int
	HealthAddr                                                                        string
}

func Load() (*Config, error) {
	c := &Config{TelegramToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")), DatabaseURL: os.Getenv("DATABASE_URL"), OpenWeatherAPIKey: strings.TrimSpace(os.Getenv("OPENWEATHER_API_KEY")), WeatherAPIKey: strings.TrimSpace(os.Getenv("WEATHERAPI_API_KEY")), MetNoUserAgent: strings.TrimSpace(os.Getenv("MET_NO_USER_AGENT")), HealthAddr: value("HEALTH_ADDR", "127.0.0.1:8080")}
	if c.TelegramToken == "" || strings.ContainsAny(c.TelegramToken, "/?&# \t\r\n") {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is missing or invalid")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.MetNoUserAgent != "" {
		if len(c.MetNoUserAgent) > 512 || strings.ContainsAny(c.MetNoUserAgent, "\r\n") || !(strings.Contains(c.MetNoUserAgent, "@") || strings.Contains(c.MetNoUserAgent, "https://")) {
			return nil, fmt.Errorf("MET_NO_USER_AGENT must identify the app and include a contact email or HTTPS URL")
		}
	}
	for _, setting := range []struct {
		key      string
		dst      *time.Duration
		def, max time.Duration
	}{
		{"HTTP_TIMEOUT", &c.HTTPTimeout, 5 * time.Second, time.Minute},
		{"DB_TIMEOUT", &c.DBTimeout, 5 * time.Second, time.Minute},
		{"POLL_TIMEOUT", &c.PollTimeout, 30 * time.Second, 50 * time.Second},
		{"SEND_TIMEOUT", &c.SendTimeout, 10 * time.Second, time.Minute},
		{"COMMAND_TIMEOUT", &c.CommandTimeout, 25 * time.Second, 2 * time.Minute},
		{"SHUTDOWN_TIMEOUT", &c.ShutdownTimeout, 15 * time.Second, time.Minute},
		{"CACHE_TTL", &c.CacheTTL, 5 * time.Minute, time.Hour},
		{"CHAT_COOLDOWN", &c.ChatCooldown, 2 * time.Second, time.Minute},
	} {
		d, err := time.ParseDuration(value(setting.key, setting.def.String()))
		if err != nil || d <= 0 || d > setting.max {
			return nil, fmt.Errorf("%s must be positive and at most %s", setting.key, setting.max)
		}
		*setting.dst = d
	}
	for _, setting := range []struct {
		key           string
		dst           *int
		def, min, max int
	}{
		{"WORKERS", &c.Workers, 4, 1, 32}, {"QUEUE_CAPACITY", &c.QueueCapacity, 1000, 100, 10000},
		{"CACHE_CAPACITY", &c.CacheCapacity, 128, 1, 4096}, {"MAX_ATTEMPTS", &c.MaxAttempts, 3, 1, 5},
		{"REQUESTS_PER_SECOND", &c.RequestsPerSecond, 5, 1, 30},
	} {
		n, err := strconv.Atoi(value(setting.key, strconv.Itoa(setting.def)))
		if err != nil || n < setting.min || n > setting.max {
			return nil, fmt.Errorf("%s must be between %d and %d", setting.key, setting.min, setting.max)
		}
		*setting.dst = n
	}
	host, port, err := net.SplitHostPort(c.HealthAddr)
	portNumber, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("HEALTH_ADDR must use a loopback IP and port")
	}
	return c, nil
}

func value(key, fallback string) string {
	if s, ok := os.LookupEnv(key); ok {
		return s
	}
	return fallback
}
