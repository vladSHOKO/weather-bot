package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/metrics"
	"weather-sum/internal/report"
	"weather-sum/internal/storage"
	"weather-sum/internal/weather"

	"github.com/joho/godotenv"
)

// Live checks are explicitly opt-in. They do not poll/acknowledge Telegram
// updates and never connect to DATABASE_URL or modify existing locations.
func liveSettings(t *testing.T) map[string]string {
	t.Helper()
	if os.Getenv("LIVE_BOT_TESTS") != "1" {
		t.Skip("requires LIVE_BOT_TESTS=1 and explicit live testing authorization")
	}
	values, err := godotenv.Read("../../.env")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal("cannot read live test configuration")
	}
	if values == nil {
		values = map[string]string{}
	}
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "OPENWEATHER_API_KEY"} {
		if v, ok := os.LookupEnv(key); ok {
			values[key] = v
		}
	}
	if values["TELEGRAM_BOT_TOKEN"] == "" {
		t.Fatal("live Telegram token is required")
	}
	return values
}

func TestLiveBotIdentity(t *testing.T) {
	c := liveSettings(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b := NewTelegramBot(c["TELEGRAM_BOT_TOKEN"], nil, nil, nil, nil, &http.Client{Timeout: 15 * time.Second})
	if _, err := b.GetMe(ctx); err != nil {
		t.Fatal(err) // GetMe wraps errors without credentials or external descriptions.
	}
	t.Logf("live bot identity verified: @%s", b.Username)
}

type liveTarget struct {
	ChatID  int64     `json:"chat_id"`
	UserID  int64     `json:"user_id"`
	Expires time.Time `json:"expires"`
}

// Reading offset zero does not acknowledge pending Telegram updates. A unique
// challenge binds the recipient to the person actively authorizing this run.
func TestLiveResolveChat(t *testing.T) {
	c := liveSettings(t)
	code, path := os.Getenv("LIVE_RESOLVE_CODE"), os.Getenv("LIVE_SMOKE_TARGET_FILE")
	if len(code) < 16 || path == "" {
		t.Fatal("a unique challenge and private target file are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	b := NewTelegramBot(c["TELEGRAM_BOT_TOKEN"], nil, nil, nil, nil, &http.Client{Timeout: 12 * time.Second})
	b.PollTimeout = 5 * time.Second
	for ctx.Err() == nil {
		updates, err := b.GetUpdates(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range updates.Messages {
			if !m.Ignore && m.ChatType == "private" && m.UserID == m.ChatID && m.ChatID > 0 && strings.TrimSpace(m.Text) == "/auditcheck "+code {
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal("cannot create private target file")
				}
				err = json.NewEncoder(f).Encode(liveTarget{ChatID: m.ChatID, UserID: m.UserID, Expires: time.Now().Add(time.Hour)})
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("cannot save verified chat")
				}
				t.Log("private recipient verified; Telegram updates left unacknowledged")
				return
			}
		}
		if err := httpx.Wait(ctx, time.Second); err != nil {
			break
		}
	}
	t.Fatal("authorization command not received within the observation window")
}

type unavailableLiveSource struct{}

func (unavailableLiveSource) Name() string { return "Test unavailable source" }
func (unavailableLiveSource) Forecast(context.Context, location.Location, weather.ForecastPeriod) ([]weather.ForecastPoint, error) {
	return nil, &httpx.StatusError{Code: 503}
}

func TestLiveBotScenario(t *testing.T) {
	c := liveSettings(t)
	chatID, err := strconv.ParseInt(os.Getenv("LIVE_SMOKE_CHAT_ID"), 10, 64)
	if err != nil || chatID <= 0 {
		path := os.Getenv("LIVE_SMOKE_TARGET_FILE")
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			t.Fatal("a private authorized target file or LIVE_SMOKE_CHAT_ID is required")
		}
		data, readErr := os.ReadFile(path)
		var target liveTarget
		if readErr != nil || json.Unmarshal(data, &target) != nil || target.ChatID <= 0 || target.ChatID != target.UserID || !target.Expires.After(time.Now()) {
			t.Fatal("invalid or expired live target")
		}
		chatID = target.ChatID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	m := &metrics.Metrics{}
	meteo := weather.Cached(weather.NewOpenMeteoProvider(client), 4, time.Minute, 15*time.Second, m)
	providers := []weather.Provider{meteo}
	if c["OPENWEATHER_API_KEY"] != "" {
		providers = append(providers, weather.Cached(weather.NewOpenWeatherProvider(client, c["OPENWEATHER_API_KEY"]), 4, time.Minute, 15*time.Second, m))
	}
	svc := weather.NewService(providers...)
	repo := &memoryRepo{items: map[int64]location.Location{}}
	makeHandler := func() *TelegramCommandHandler {
		b := NewTelegramBot(c["TELEGRAM_BOT_TOKEN"], repo, location.NewOpenMeteoGeocoder(client), svc, report.NewTelegramRenderer(), client)
		if _, err := b.GetMe(ctx); err != nil {
			t.Fatal(err)
		}
		h := NewTelegramCommandHandler(b)
		h.Cooldown = 0 // Synthetic steps are sequential; rate limiting is checked offline.
		return h
	}
	h := makeHandler()
	nextID := int64(0)
	command := func(text string) {
		t.Helper()
		nextID++
		message := IncomingMessage{UpdateID: nextID, ChatID: chatID, UserID: chatID, ChatType: "private", Text: text, CommandEntity: true}
		if err := h.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	command("/start")
	command("/weather")
	command("/setlocation Москва")
	if len(repo.items) != 0 {
		t.Fatal("location saved before confirmation")
	}
	choice, ok := h.choices[chatID]
	if !ok || len(choice.locations) == 0 {
		t.Fatal("live geocoding returned no candidates")
	}
	command(fmt.Sprintf("/choose %s 1", choice.nonce))
	loc, err := repo.GetByChatID(ctx, chatID)
	if err != nil {
		t.Fatal("confirmed test location not saved")
	}
	result, err := svc.GetTodayWeather(ctx, loc)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range result.Sources {
		t.Logf("live source %s: %s", source.SourceName, source.State)
	}
	command("/weather")
	// Only the failing source is synthetic; the usable data and Telegram sends
	// still come from real services.
	h.bot.weatherService = weather.NewService(meteo, unavailableLiveSource{})
	partial, err := h.bot.weatherService.GetTodayWeather(ctx, loc)
	if err != nil || !partial.Sources[0].IsAvailable || partial.Sources[1].IsAvailable || partial.Rain.Complete {
		t.Fatal("live partial-source scenario failed")
	}
	command("/weather")
	h = makeHandler() // Recreate the handler and check the saved location again.
	command("/location")
	command("/deletelocation")
	command("/confirmdelete " + h.choices[chatID].nonce)
	if _, err := repo.GetByChatID(ctx, chatID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("test location not deleted")
	}
	command("/weather")
	t.Log("live HTTPS scenario completed; test storage empty; Telegram polling and existing data untouched")
}
