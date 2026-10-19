package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"weather-sum/internal/location"
)

func callback(id int64, data string) IncomingMessage {
	m := msg(id, "")
	m.CommandEntity = false
	m.CallbackID = "callback-test"
	m.CallbackData = data
	return m
}
func plain(id int64, text string) IncomingMessage {
	m := msg(id, text)
	m.CommandEntity = false
	return m
}
func lastPayload(sent *[]Payload) Payload { return (*sent)[len(*sent)-1] }

func TestMenuAndCityDialogue(t *testing.T) {
	h, repo, w, sent := testHandler(t)
	ctx := context.Background()
	handle := func(m IncomingMessage) {
		t.Helper()
		if err := h.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	handle(msg(1, "/start"))
	if (*sent)[0].ReplyMarkup == nil || len((*sent)[0].ReplyMarkup.Keyboard) == 0 {
		t.Fatal("missing persistent menu")
	}
	handle(plain(2, "📍 Выбрать город"))
	if !h.choices[7].waiting || !strings.Contains(lastPayload(sent).Text, "Напишите") {
		t.Fatal("not waiting for city")
	}
	handle(plain(3, "Я"))
	if !h.choices[7].waiting || len(repo.items) != 0 {
		t.Fatal("invalid city accepted")
	}
	handle(plain(4, "Test"))
	p := lastPayload(sent)
	if len(p.ReplyMarkup.InlineKeyboard) != 4 || strings.Contains(p.Text, "/choose") {
		t.Fatal("missing clean candidate buttons")
	}
	data := p.ReplyMarkup.InlineKeyboard[1][0].CallbackData
	if len(data) > 64 {
		t.Fatal("callback too long")
	}
	handle(callback(5, data))
	if len(repo.items) != 1 || !strings.Contains(repo.items[7].Name, "Other") {
		t.Fatal("wrong city saved")
	}
	handle(callback(6, "weather"))
	if w.calls != 1 {
		t.Fatal("weather button failed")
	}
	handle(plain(7, "📌 Мой город"))
	if !strings.Contains(lastPayload(sent).Text, "Текущая локация") {
		t.Fatal("location menu failed")
	}
}

type missingCityGeocoder struct{ cityGeocoder }

func (missingCityGeocoder) Candidates(context.Context, string) ([]location.Location, error) {
	return nil, location.ErrLocationNotFound
}
func TestMissingCityCanBeCorrectedWithoutCommand(t *testing.T) {
	h, _, _, sent := testHandler(t)
	ctx := context.Background()
	h.bot.geocoder = missingCityGeocoder{}
	_ = h.Handle(ctx, msg(1, "/setlocation"))
	_ = h.Handle(ctx, plain(2, "Неизвестный"))
	if !h.choices[7].waiting || !strings.Contains(lastPayload(sent).Text, "не найден") {
		t.Fatal("dialogue lost")
	}
	h.bot.geocoder = cityGeocoder{}
	if err := h.Handle(ctx, plain(3, "Test")); err != nil || len(h.choices[7].locations) == 0 {
		t.Fatal("correction failed", err)
	}
}
func TestCancelNavigationAndStaleButtons(t *testing.T) {
	for _, action := range []IncomingMessage{callback(3, "cancel"), plain(3, "🌤 Погода"), plain(3, "📌 Мой город"), plain(3, "Помощь"), msg(3, "/cancel")} {
		h, repo, _, sent := testHandler(t)
		ctx := context.Background()
		_ = h.Handle(ctx, msg(1, "/setlocation Test"))
		data := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData
		_ = h.Handle(ctx, action)
		_ = h.Handle(ctx, callback(4, data))
		if len(repo.items) != 0 || !strings.Contains(lastPayload(sent).Text, "истёк") {
			t.Fatal("stale choice accepted")
		}
	}
	h, repo, _, sent := testHandler(t)
	ctx := context.Background()
	_ = h.Handle(ctx, msg(1, "/setlocation Test"))
	old := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData
	_ = h.Handle(ctx, callback(2, "setlocation"))
	_ = h.Handle(ctx, plain(3, "Other"))
	current := h.choices[7]
	_ = h.Handle(ctx, callback(4, old))
	if h.choices[7].nonce != current.nonce || len(repo.items) != 0 {
		t.Fatal("old button affected current choice")
	}
	h.now = func() time.Time { return current.expires.Add(time.Second) }
	_ = h.Handle(ctx, callback(5, "choose:"+current.nonce+":1"))
	if len(repo.items) != 0 {
		t.Fatal("expired callback accepted")
	}
}
func TestDeleteConfirmationOwnershipAndCancellation(t *testing.T) {
	h, repo, _, sent := testHandler(t)
	ctx := context.Background()
	repo.items[7], _ = cityGeocoder{}.Search(ctx, "Test")
	_ = h.Handle(ctx, callback(1, "deletelocation"))
	data := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData
	foreign := callback(2, data)
	foreign.ChatID, foreign.UserID = 8, 8
	_ = h.Handle(ctx, foreign)
	if len(repo.items) != 1 {
		t.Fatal("foreign delete")
	}
	_ = h.Handle(ctx, callback(3, "cancel"))
	_ = h.Handle(ctx, callback(4, data))
	if len(repo.items) != 1 {
		t.Fatal("cancelled delete")
	}
	_ = h.Handle(ctx, callback(5, "deletelocation"))
	data = lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData
	_ = h.Handle(ctx, callback(6, data))
	if len(repo.items) != 0 {
		t.Fatal("delete not performed")
	}
}
func TestCallbackParsingAndAcknowledgement(t *testing.T) {
	h, _, _, _ := testHandler(t)
	h.bot.pollClient = &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Query().Get("allowed_updates"), "callback_query") {
			t.Fatal("callback subscription missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"update_id":1,"callback_query":{"id":"cb1","from":{"id":7},"message":{"chat":{"id":7,"type":"private"}},"data":"setlocation"}},{"update_id":2,"callback_query":{"id":"cb2","from":{"id":7},"data":"weather"}}]}`))}, nil
	})}
	updates, err := h.bot.GetUpdates(context.Background(), 0)
	if err != nil || len(updates.Messages) != 2 || updates.Messages[0].Ignore || !updates.Messages[1].Ignore {
		t.Fatalf("parse: %+v %v", updates, err)
	}
	m := updates.Messages[0]
	encoded, _ := json.Marshal(m)
	var restored IncomingMessage
	_ = json.Unmarshal(encoded, &restored)
	if restored.CallbackID != "cb1" || restored.CallbackData != "setlocation" {
		t.Fatal("queue roundtrip lost callback")
	}
	acknowledged := false
	h.bot.client = &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			acknowledged = p["callback_query_id"] == "cb1"
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	if err := h.Handle(context.Background(), restored); err != nil || !acknowledged || !h.choices[7].waiting {
		t.Fatal("acknowledgement blocked action", err)
	}
}
func TestCityTextRetryAfterReplyFailure(t *testing.T) {
	h, _, _, _ := testHandler(t)
	ctx := context.Background()
	_ = h.Handle(ctx, msg(1, "/setlocation"))
	original := h.bot.client
	h.bot.client = &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("temporary send error") })}
	m := plain(2, "Test")
	if err := h.Handle(ctx, m); err == nil {
		t.Fatal("expected send failure")
	}
	h.bot.client = original
	if err := h.Handle(ctx, m); err != nil || len(h.choices[7].locations) == 0 {
		t.Fatal("plain city retry failed", err)
	}
}

func TestDialogueCooldownAllowsFirstForecast(t *testing.T) {
	h, _, w, sent := testHandler(t)
	ctx := context.Background()
	h.Cooldown = time.Minute
	for _, m := range []IncomingMessage{msg(1, "/setlocation"), plain(2, "Test")} {
		if err := h.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	data := lastPayload(sent).ReplyMarkup.InlineKeyboard[0][0].CallbackData
	_ = h.Handle(ctx, callback(3, data))
	_ = h.Handle(ctx, callback(4, "weather"))
	if w.calls != 1 {
		t.Fatal("city search throttled first forecast")
	}
	_ = h.Handle(ctx, callback(5, "weather"))
	if w.calls != 1 {
		t.Fatal("forecast rate limit lost")
	}
}
