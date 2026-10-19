package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"weather-sum/internal/weather"
)

func TestWeatherDetailsUseShownForecastWithoutNewRequests(t *testing.T) {
	h, repo, w, sent := testHandler(t)
	ctx := context.Background()
	repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
	h.Cooldown = time.Minute
	if err := h.Handle(ctx, msg(1, "/weather")); err != nil {
		t.Fatal(err)
	}
	summary := lastPayload(sent)
	if summary.ParseMode != "HTML" || summary.LinkPreviewOptions == nil || !summary.LinkPreviewOptions.IsDisabled || strings.Contains(summary.Text, "Источники:") {
		t.Fatal("invalid compact payload", summary)
	}
	rows := summary.ReplyMarkup.InlineKeyboard
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][0].Text != "Обновить" || rows[0][1].Text != "Подробнее" {
		t.Fatal("missing forecast actions", rows)
	}
	data := rows[0][1].CallbackData
	if err := h.Handle(ctx, callback(2, data)); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 || !strings.Contains(lastPayload(sent).Text, "Источники:") {
		t.Fatal("details refetched weather or lost diagnostics")
	}
	// Details are cheap and remain available during the weather cooldown.
	if err := h.Handle(ctx, callback(3, data)); err != nil || w.calls != 1 {
		t.Fatal("details throttled", err)
	}
	h.Cooldown = 0
	if err := h.Handle(ctx, callback(4, "weather")); err != nil || w.calls != 2 {
		t.Fatal("refresh did not run", err)
	}
	if strings.Contains(lastPayload(sent).Text, "Источники:") {
		t.Fatal("refresh returned details")
	}
	_ = h.Handle(ctx, callback(5, data))
	if !strings.Contains(lastPayload(sent).Text, "устарел") {
		t.Fatal("superseded details remained active")
	}
}
func TestWeatherDetailsOwnershipExpiryAndRestart(t *testing.T) {
	h, repo, w, sent := testHandler(t)
	ctx := context.Background()
	repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
	_ = h.Handle(ctx, msg(1, "/weather"))
	data := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][1].CallbackData
	foreign := callback(2, data)
	foreign.ChatID, foreign.UserID = 8, 8
	_ = h.Handle(ctx, foreign)
	if strings.Contains(lastPayload(sent).Text, "Источники:") {
		t.Fatal("foreign details disclosed")
	}
	view := h.forecasts[7]
	h.now = func() time.Time { return view.expires.Add(time.Second) }
	_ = h.Handle(ctx, callback(3, data))
	if !strings.Contains(lastPayload(sent).Text, "устарел") || len(h.forecasts) != 0 || w.calls != 1 {
		t.Fatal("expired details used")
	}
	h = NewTelegramCommandHandler(h.bot)
	_ = h.Handle(ctx, callback(4, data))
	if !strings.Contains(lastPayload(sent).Text, "устарел") {
		t.Fatal("restart lost recovery action")
	}
}
func TestChangingOrDeletingLocationClearsWeatherDetails(t *testing.T) {
	for _, action := range []string{"change", "delete"} {
		h, repo, _, sent := testHandler(t)
		ctx := context.Background()
		repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
		_ = h.Handle(ctx, msg(1, "/weather"))
		data := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][1].CallbackData
		if action == "change" {
			_ = h.Handle(ctx, msg(2, "/setlocation Other"))
			_ = h.Handle(ctx, callback(3, lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData))
		} else {
			_ = h.Handle(ctx, msg(2, "/deletelocation"))
			_ = h.Handle(ctx, callback(3, lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData))
		}
		_ = h.Handle(ctx, callback(4, data))
		if len(h.forecasts) != 0 || !strings.Contains(lastPayload(sent).Text, "устарел") {
			t.Fatal("old location details retained", action)
		}
	}
}
func TestHTMLMessageSplitsWithoutBreakingMarkup(t *testing.T) {
	h, _, _, sent := testHandler(t)
	line := `<i><a href="https://example.org/">Источник &amp; данные 😀</a></i>` + "\n"
	text := strings.Repeat(line, 100)
	markup := &ReplyMarkup{InlineKeyboard: [][]InlineButton{{{"Обновить", "weather"}}}}
	if err := h.bot.SendHTMLMessageWithMarkup(context.Background(), 7, text, markup); err != nil {
		t.Fatal(err)
	}
	if len(*sent) < 2 {
		t.Fatal("long details not split")
	}
	var joined strings.Builder
	for i, part := range *sent {
		joined.WriteString(part.Text)
		if len(utf16.Encode([]rune(part.Text))) > 3500 || strings.Count(part.Text, "<i>") != strings.Count(part.Text, "</i>") || part.ParseMode != "HTML" {
			t.Fatal("broken markup", part)
		}
		if (part.ReplyMarkup != nil) != (i == len(*sent)-1) {
			t.Fatal("keyboard attached to wrong chunk")
		}
	}
	if joined.String() != text {
		t.Fatal("message corrupted")
	}
}

func TestTomorrowCommandDetailsAndRefreshKeepSelectedDay(t *testing.T) {
	h, repo, w, sent := testHandler(t)
	ctx := context.Background()
	repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
	if err := h.Handle(ctx, msg(1, "/tomorrow@TestBot")); err != nil {
		t.Fatal(err)
	}
	summary := lastPayload(sent)
	if !strings.Contains(summary.Text, "· завтра,") || w.calls != 0 || w.tomorrowCalls != 1 {
		t.Fatal("wrong forecast requested", summary, w)
	}
	row := summary.ReplyMarkup.InlineKeyboard[0]
	if row[0].CallbackData != "tomorrow" || !strings.HasPrefix(row[1].CallbackData, "details:tomorrow:") || len(row[1].CallbackData) > 64 {
		t.Fatal("tomorrow actions lost day", row)
	}
	oldDetails := row[1].CallbackData
	if err := h.Handle(ctx, callback(2, oldDetails)); err != nil {
		t.Fatal(err)
	}
	if w.tomorrowCalls != 1 || !strings.Contains(lastPayload(sent).Text, "с 00:00") || lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData != "tomorrow" {
		t.Fatal("details changed day or requested new data")
	}
	if err := h.Handle(ctx, callback(3, "tomorrow")); err != nil || w.tomorrowCalls != 2 || w.calls != 0 {
		t.Fatal("refresh switched to today", err)
	}
	_ = h.Handle(ctx, callback(4, oldDetails))
	if !strings.Contains(lastPayload(sent).Text, "устарел") || lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData != "tomorrow" {
		t.Fatal("stale tomorrow button lost recovery day")
	}
	// Recovery must also work after a process restart without any saved snapshot.
	h = NewTelegramCommandHandler(h.bot)
	_ = h.Handle(ctx, callback(5, oldDetails))
	if lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData != "tomorrow" {
		t.Fatal("restart switched recovery to today")
	}
}
func TestTomorrowMissingLocationNoDataAndCooldown(t *testing.T) {
	h, repo, w, sent := testHandler(t)
	ctx := context.Background()
	_ = h.Handle(ctx, msg(1, "/tomorrow"))
	if w.tomorrowCalls != 0 || !strings.Contains(lastPayload(sent).Text, "Сначала выберите") {
		t.Fatal("tomorrow bypassed location selection")
	}
	repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
	w.tomorrowError = weather.ErrNoData
	if err := h.Handle(ctx, msg(2, "/tomorrow")); err != nil || !strings.Contains(lastPayload(sent).Text, "На завтра пока нет данных") {
		t.Fatal("no-data guidance lost", err)
	}
	w.tomorrowError = nil
	h = NewTelegramCommandHandler(h.bot)
	h.Cooldown = time.Minute
	m := msg(3, "/tomorrow")
	if err := h.Handle(ctx, m); err != nil {
		t.Fatal(err)
	}
	calls := w.tomorrowCalls
	_ = h.Handle(ctx, msg(4, "/tomorrow"))
	if w.tomorrowCalls != calls {
		t.Fatal("tomorrow not rate limited")
	}
	if err := h.Handle(ctx, m); err != nil || w.tomorrowCalls != calls+1 {
		t.Fatal("retry throttled", err)
	}
}
