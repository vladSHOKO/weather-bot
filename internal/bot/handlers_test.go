package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/report"
	"weather-sum/internal/storage"
	"weather-sum/internal/weather"
)

type memoryRepo struct {
	mu    sync.Mutex
	items map[int64]location.Location
	reads int
}

func (r *memoryRepo) Save(_ context.Context, id int64, l location.Location) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[id] = l
	return nil
}
func (r *memoryRepo) GetByChatID(_ context.Context, id int64) (location.Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	l, ok := r.items[id]
	if !ok {
		return l, storage.ErrNotFound
	}
	return l, nil
}
func (r *memoryRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return nil
}

type cityGeocoder struct{}

func (cityGeocoder) Search(context.Context, string) (location.Location, error) {
	return location.Location{Name: "Test", Latitude: 1, Longitude: 1, Timezone: "UTC"}, nil
}
func (g cityGeocoder) Candidates(ctx context.Context, s string) ([]location.Location, error) {
	l, _ := g.Search(ctx, s)
	other := l
	other.Region = "Other"
	return []location.Location{l, other}, nil
}

type fakeWeather struct {
	calls         int
	tomorrowCalls int
	tomorrowError error
	fail          bool
}

func (w *fakeWeather) GetTodayWeather(context.Context, location.Location) (weather.WeatherResult, error) {
	w.calls++
	if w.fail {
		return weather.WeatherResult{}, &httpx.StatusError{Code: 503}
	}
	return weather.WeatherResult{}, nil
}
func (w *fakeWeather) GetTomorrowWeather(_ context.Context, loc location.Location) (weather.WeatherResult, error) {
	w.tomorrowCalls++
	if w.tomorrowError != nil {
		return weather.WeatherResult{}, w.tomorrowError
	}
	if w.fail {
		return weather.WeatherResult{}, &httpx.StatusError{Code: 503}
	}
	zone, _ := time.LoadLocation(loc.Timezone)
	now := time.Now().In(zone)
	from := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, zone)
	return weather.WeatherResult{Day: weather.Tomorrow, Location: loc, GeneratedAt: now, Period: weather.ForecastPeriod{From: from, To: from.AddDate(0, 0, 1)}}, nil
}
func testHandler(t *testing.T) (*TelegramCommandHandler, *memoryRepo, *fakeWeather, *[]Payload) {
	t.Helper()
	r := &memoryRepo{items: map[int64]location.Location{}}
	w := &fakeWeather{}
	sent := &[]Payload{}
	c := &http.Client{Transport: securityTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/answerCallbackQuery") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		}
		var p Payload
		_ = json.NewDecoder(req.Body).Decode(&p)
		*sent = append(*sent, p)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	b := NewTelegramBot("synthetic", r, cityGeocoder{}, w, report.NewTelegramRenderer(), c)
	b.Username = "TestBot"
	h := NewTelegramCommandHandler(b)
	h.Cooldown = 0
	return h, r, w, sent
}
func msg(id int64, text string) IncomingMessage {
	return IncomingMessage{UpdateID: id, ChatID: 7, UserID: 7, ChatType: "private", Text: text, CommandEntity: true}
}
func TestCityConfirmationOwnershipAndDeletion(t *testing.T) {
	h, r, _, sent := testHandler(t)
	ctx := context.Background()
	if err := h.Handle(ctx, msg(1, "/setlocation Test")); err != nil {
		t.Fatal(err)
	}
	if len(r.items) != 0 {
		t.Fatal("city saved before confirmation")
	}
	c := h.choices[7]
	if len(c.locations) != 2 || !strings.Contains((*sent)[0].ReplyMarkup.InlineKeyboard[1][0].Text, "Other") {
		t.Fatal("missing candidates")
	}
	foreign := msg(2, "/choose "+c.nonce+" 1")
	foreign.ChatID = 8
	foreign.UserID = 8
	_ = h.Handle(ctx, foreign)
	if len(r.items) != 0 {
		t.Fatal("foreign confirmation succeeded")
	}
	if err := h.Handle(ctx, msg(3, "/choose "+c.nonce+" 1")); err != nil {
		t.Fatal(err)
	}
	if len(r.items) != 1 {
		t.Fatal("confirmation failed")
	}
	called := false
	h.OnForget = func(context.Context, int64, int64) error { called = true; return nil }
	if err := h.Handle(ctx, msg(4, "/deletelocation")); err != nil || len(r.items) != 1 || called {
		t.Fatalf("premature delete: %v", err)
	}
	if err := h.Handle(ctx, callback(5, "delete:"+h.choices[7].nonce)); err != nil || len(r.items) != 0 || !called {
		t.Fatalf("delete: %v", err)
	}
	_ = h.Handle(ctx, msg(5, "/weather"))
	if !strings.Contains((*sent)[len(*sent)-1].Text, "Сначала") {
		t.Fatal("missing location guidance")
	}
}
func TestExpiredChoiceAndGroupPolicy(t *testing.T) {
	h, r, w, _ := testHandler(t)
	ctx := context.Background()
	_ = h.Handle(ctx, msg(1, "/setlocation Test"))
	c := h.choices[7]
	h.now = func() time.Time { return c.expires.Add(time.Second) }
	_ = h.Handle(ctx, msg(2, "/choose "+c.nonce+" 1"))
	if len(r.items) != 0 {
		t.Fatal("expired choice used")
	}
	m := msg(3, "/weather")
	m.ChatType = "supergroup"
	m.ChatID = -1
	_ = h.Handle(ctx, m)
	if r.reads != 0 || w.calls != 0 {
		t.Fatal("group accessed location")
	}
}
func TestCommandsAndRetryCooldown(t *testing.T) {
	for _, tc := range []struct{ text, username, want string }{{"/weather@TestBot", "TestBot", "/weather"}, {"/weather@OtherBot", "TestBot", "ignore"}, {"/help", "TestBot", "/help"}} {
		got, _ := recognize(tc.text, true, tc.username)
		if got != tc.want {
			t.Fatal(got)
		}
	}
	h, r, w, _ := testHandler(t)
	l, _ := cityGeocoder{}.Search(context.Background(), "")
	r.items[7] = l
	h.Cooldown = time.Minute
	w.fail = true
	m := msg(100, "/weather")
	_ = h.Handle(context.Background(), m)
	w.fail = false
	if err := h.Handle(context.Background(), m); err != nil || w.calls != 2 {
		t.Fatal("retry throttled", err, w.calls)
	}
	_ = h.Handle(context.Background(), msg(101, "/weather"))
	if w.calls != 2 {
		t.Fatal("new request not throttled")
	}
}
func TestBatchContinuesAfterPermanentFailure(t *testing.T) {
	h, _, _, _ := testHandler(t)
	calls := 0
	h.bot.client = &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":403}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	err := h.SolveHandler(context.Background(), UpdatesResponse{Messages: []IncomingMessage{msg(1, "/start"), msg(2, "/help")}})
	var status *httpx.StatusError
	if !errors.As(err, &status) || status.Code != 403 || calls != 2 {
		t.Fatal("batch blocked", err, calls)
	}
}
func TestSplitUnicodeAndTelegramRetryAfter(t *testing.T) {
	text := strings.Repeat("а😀", 4000)
	parts := SplitMessage(text, 3500)
	if strings.Join(parts, "") != text {
		t.Fatal("text corrupted")
	}
	for _, p := range parts {
		if len(utf16.Encode([]rune(p))) > 3500 {
			t.Fatal("UTF-16 limit exceeded")
		}
	}
	h, _, _, _ := testHandler(t)
	h.bot.client = &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":429,"parameters":{"retry_after":12}}`))}, nil
	})}
	_, err := h.bot.SendMessage(context.Background(), 7, "test")
	var status *httpx.StatusError
	if !errors.As(err, &status) || status.RetryAfter != 12*time.Second {
		t.Fatal(err)
	}
}
func TestUpdateParsingKeepsUnsupportedIDs(t *testing.T) {
	h, _, _, _ := testHandler(t)
	h.bot.pollClient = &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"update_id":100},{"update_id":101,"message":{"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"/help@TestBot","entities":[{"type":"bot_command","offset":0,"length":13}]}}]}`))}, nil
	})}
	u, err := h.bot.GetUpdates(context.Background(), 0)
	if err != nil || len(u.Messages) != 2 || !u.Messages[0].Ignore || !u.Messages[1].CommandEntity || u.LastUpdateNumber != 101 {
		t.Fatalf("%+v %v", u, err)
	}
}
