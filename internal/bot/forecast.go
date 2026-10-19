package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
	"weather-sum/internal/weather"
)

// Store only rendered details, scoped to the private chat and latest forecast.
// Raw provider points and user messages are not retained here.
type forecastView struct {
	nonce         string
	refreshAction string
	details       string
	expires       time.Time
}

func (h *TelegramCommandHandler) sendForecast(ctx context.Context, chatID int64, result weather.WeatherResult) error {
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes)
	refreshAction := "weather"
	detailsAction := "details:" + nonce
	if result.Day == weather.Tomorrow {
		refreshAction = "tomorrow"
		detailsAction = "details:tomorrow:" + nonce
	}
	view := forecastView{nonce: nonce, refreshAction: refreshAction, details: h.bot.renderer.RenderDetails(result)}
	h.mu.Lock()
	now := h.now()
	view.expires = now.Add(30 * time.Minute)
	if result.Period.To.After(now) && result.Period.To.Before(view.expires) {
		view.expires = result.Period.To
	}
	for id, old := range h.forecasts {
		if !old.expires.After(now) {
			delete(h.forecasts, id)
		}
	}
	_, exists := h.forecasts[chatID]
	available := exists || len(h.forecasts) < 1024
	if available {
		h.forecasts[chatID] = view
	}
	h.mu.Unlock()
	row := []InlineButton{{"Обновить", refreshAction}}
	if available {
		row = append(row, InlineButton{"Подробнее", detailsAction})
	}
	return h.bot.SendHTMLMessageWithMarkup(ctx, chatID, h.bot.renderer.Render(result), &ReplyMarkup{InlineKeyboard: [][]InlineButton{row}})
}
func (h *TelegramCommandHandler) weatherDetails(ctx context.Context, chatID int64, args []string) error {
	refreshAction, nonce := "weather", ""
	if len(args) == 1 {
		nonce = args[0]
	}
	if len(args) == 2 && args[0] == "tomorrow" {
		refreshAction, nonce = "tomorrow", args[1]
	}
	h.mu.Lock()
	view, ok := h.forecasts[chatID]
	if ok && !view.expires.After(h.now()) {
		delete(h.forecasts, chatID)
		ok = false
	}
	h.mu.Unlock()
	if !ok || nonce == "" || nonce != view.nonce || refreshAction != view.refreshAction {
		return h.buttons(ctx, chatID, "Этот прогноз устарел. Обновите погоду.", []InlineButton{{"Обновить", refreshAction}})
	}
	return h.bot.SendHTMLMessageWithMarkup(ctx, chatID, view.details, &ReplyMarkup{InlineKeyboard: [][]InlineButton{{{"Обновить", refreshAction}}}})
}
func (h *TelegramCommandHandler) clearForecast(chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.forecasts, chatID)
}
