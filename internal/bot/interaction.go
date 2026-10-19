package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"weather-sum/internal/storage"
)

func mainMenu() *ReplyMarkup {
	return &ReplyMarkup{Keyboard: [][]KeyboardButton{{{"🌤 Погода"}, {"📍 Выбрать город"}}, {{"📌 Мой город"}, {"Помощь"}}}, ResizeKeyboard: true, IsPersistent: true}
}
func (h *TelegramCommandHandler) buttons(ctx context.Context, id int64, text string, rows ...[]InlineButton) error {
	_, err := h.bot.SendMessageWithMarkup(ctx, id, text, &ReplyMarkup{InlineKeyboard: rows})
	return err
}
func (h *TelegramCommandHandler) cityPrompt(ctx context.Context, id int64, text string) error {
	return h.buttons(ctx, id, text, []InlineButton{{"Отмена", "cancel"}})
}
func (h *TelegramCommandHandler) missingLocation(ctx context.Context, id int64) error {
	return h.buttons(ctx, id, "Сначала выберите город.", []InlineButton{{"📍 Выбрать город", "setlocation"}})
}
func (h *TelegramCommandHandler) expired(ctx context.Context, id int64) error {
	return h.buttons(ctx, id, "Выбор недействителен или истёк. Начните заново.", []InlineButton{{"📍 Выбрать город", "setlocation"}})
}
func (h *TelegramCommandHandler) clearChoice(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.choices, id)
}

// Sessions are bounded and short lived, just like city confirmations.
func (h *TelegramCommandHandler) newSession(m IncomingMessage, waiting, deleting bool) (choice, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return choice{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for id, c := range h.choices {
		if !c.expires.After(now) {
			delete(h.choices, id)
		}
	}
	if _, exists := h.choices[m.ChatID]; !exists && len(h.choices) >= 1024 {
		return choice{}, errors.New("Бот занят. Попробуйте позже.")
	}
	c := choice{updateID: m.UpdateID, nonce: hex.EncodeToString(nonce), userID: m.UserID, expires: now.Add(5 * time.Minute), waiting: waiting, deleting: deleting}
	h.choices[m.ChatID] = c
	return c, nil
}
func (h *TelegramCommandHandler) awaitCity(m IncomingMessage) error {
	_, err := h.newSession(m, true, false)
	return err
}
func (h *TelegramCommandHandler) action(m IncomingMessage) (string, []string) {
	if m.CallbackID != "" {
		parts := strings.Split(m.CallbackData, ":")
		switch parts[0] {
		case "choose":
			if len(parts) == 3 {
				return "/choose", parts[1:]
			}
		case "details":
			if len(parts) == 2 || len(parts) == 3 && parts[1] == "tomorrow" {
				return "/weatherdetails", parts[1:]
			}
		case "delete":
			if len(parts) == 2 {
				return "/confirmdelete", parts[1:]
			}
		case "setlocation", "weather", "tomorrow", "location", "deletelocation", "cancel":
			if len(parts) == 1 {
				return "/" + parts[0], nil
			}
		}
		return "/expired", nil
	}
	switch strings.TrimSpace(m.Text) {
	case "🌤 Погода":
		return "/weather", nil
	case "📍 Выбрать город":
		return "/setlocation", nil
	case "📌 Мой город":
		return "/location", nil
	case "Помощь":
		return "/help", nil
	case "Отмена":
		return "/cancel", nil
	}
	if m.CommandEntity {
		return recognize(m.Text, true, h.bot.Username)
	}
	h.mu.Lock()
	c, ok := h.choices[m.ChatID]
	h.mu.Unlock()
	if ok && c.userID == m.UserID && (c.waiting || c.updateID == m.UpdateID && len(c.locations) > 0) {
		if !c.expires.After(h.now()) {
			return "/expired", nil
		}
		return "/setlocation", []string{strings.TrimSpace(m.Text)}
	}
	return "/unknown", nil
}
func (h *TelegramCommandHandler) requestDelete(ctx context.Context, m IncomingMessage) error {
	l, err := h.bot.GetLocation(ctx, m.ChatID)
	if errors.Is(err, storage.ErrNotFound) {
		h.clearChoice(m.ChatID)
		return h.missingLocation(ctx, m.ChatID)
	}
	if err != nil {
		return err
	}
	c, err := h.newSession(m, false, true)
	if err != nil {
		return err
	}
	return h.buttons(ctx, m.ChatID, "Удалить город «"+l.Name+"»?", []InlineButton{{"Удалить", "delete:" + c.nonce}, {"Отмена", "cancel"}})
}
func (h *TelegramCommandHandler) deleteLocation(ctx context.Context, m IncomingMessage, args []string) error {
	h.mu.Lock()
	c, ok := h.choices[m.ChatID]
	h.mu.Unlock()
	if len(args) != 1 || !ok || !c.deleting || c.userID != m.UserID || args[0] != c.nonce || !c.expires.After(h.now()) {
		return h.expired(ctx, m.ChatID)
	}
	if err := h.bot.locRepo.Delete(ctx, m.ChatID); err != nil {
		return err
	}
	h.clearForecast(m.ChatID)
	if h.OnForget != nil {
		if err := h.OnForget(ctx, m.ChatID, m.UpdateID); err != nil {
			return err
		}
	}
	if err := h.reply(ctx, m.ChatID, "Сохранённая локация удалена. Копии исчезнут по сроку хранения резервных копий оператора."); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.choices, m.ChatID)
	delete(h.last, cooldownKey{m.ChatID, "/weather"})
	delete(h.last, cooldownKey{m.ChatID, "/tomorrow"})
	delete(h.last, cooldownKey{m.ChatID, "/setlocation"})
	h.mu.Unlock()
	return nil
}
