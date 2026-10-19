package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/metrics"
	"weather-sum/internal/storage"
	"weather-sum/internal/weather"
)

const helpMessage = "Это бот сводки погоды.\n/setlocation — выбрать локацию\n/weather — прогноз до полуночи\n/tomorrow — прогноз на завтра\n/location — текущая локация\n/deletelocation — удалить локацию\n/cancel — отменить действие\n/help — помощь\nХранятся город и координаты, связанные с вашим чатом. История команд удаляется после обработки. Резервные копии хранятся по политике оператора."

type cooldownKey struct {
	chatID  int64
	command string
}
type cooldownEntry struct {
	at       time.Time
	updateID int64
}

type choice struct {
	updateID  int64
	waiting   bool
	deleting  bool
	nonce     string
	userID    int64
	locations []location.Location
	expires   time.Time
}
type TelegramCommandHandler struct {
	bot       *TelegramBot
	mu        sync.Mutex
	choices   map[int64]choice
	forecasts map[int64]forecastView
	last      map[cooldownKey]cooldownEntry
	Cooldown  time.Duration
	Metrics   *metrics.Metrics
	OnForget  func(context.Context, int64, int64) error
	now       func() time.Time
}

func NewTelegramCommandHandler(b *TelegramBot) *TelegramCommandHandler {
	return &TelegramCommandHandler{bot: b, choices: map[int64]choice{}, forecasts: map[int64]forecastView{}, last: map[cooldownKey]cooldownEntry{}, Cooldown: 2 * time.Second, Metrics: &metrics.Metrics{}, now: time.Now}
}
func (h *TelegramCommandHandler) SolveHandler(ctx context.Context, updates UpdatesResponse) error {
	var first error
	for _, m := range updates.Messages {
		if err := h.Handle(ctx, m); err != nil && first == nil {
			first = err
		}
	}
	return first
}
func (h *TelegramCommandHandler) reply(ctx context.Context, id int64, text string) error {
	_, err := h.bot.SendMessageWithMarkup(ctx, id, text, mainMenu())
	return err
}
func (h *TelegramCommandHandler) Handle(ctx context.Context, m IncomingMessage) error {
	if m.Ignore {
		return nil
	}
	if m.ChatType != "private" {
		return h.reply(ctx, m.ChatID, "Бот поддерживает только личные чаты. Откройте личный диалог с ботом.")
	}
	if m.UserID == 0 || m.UserID != m.ChatID {
		return nil
	}
	if m.CallbackID != "" {
		// Expired Telegram callback IDs must not prevent queued actions from running.
		if err := h.bot.AnswerCallback(ctx, m.CallbackID); err != nil {
			var status *httpx.StatusError
			if !errors.As(err, &status) || status.Code != 400 {
				return err
			}
		}
	}
	command, args := h.action(m)
	if command == "ignore" {
		return nil
	}
	if command == "/weather" || command == "/tomorrow" {
		h.clearChoice(m.ChatID)
	}
	// Enforce cooldown for expensive commands, without retaining an unbounded ID map.
	if command == "/weather" || command == "/tomorrow" || command == "/setlocation" && len(args) > 0 {
		h.mu.Lock()
		now := h.now()
		key := cooldownKey{m.ChatID, command}
		last := h.last[key]
		if h.Cooldown > 0 && last.updateID != m.UpdateID && now.Sub(last.at) < h.Cooldown {
			h.mu.Unlock()
			h.Metrics.RateLimited.Add(1)
			return h.reply(ctx, m.ChatID, "Слишком частые запросы. Подождите несколько секунд.")
		}
		if len(h.last) >= 1024 {
			for id, t := range h.last {
				if now.Sub(t.at) >= h.Cooldown {
					delete(h.last, id)
				}
			}
			if len(h.last) >= 1024 {
				h.mu.Unlock()
				h.Metrics.RateLimited.Add(1)
				return h.reply(ctx, m.ChatID, "Бот занят. Попробуйте позже.")
			}
		}
		h.last[key] = cooldownEntry{at: now, updateID: m.UpdateID}
		h.mu.Unlock()
	}
	switch command {
	case "/start":
		h.clearChoice(m.ChatID)
		if err := h.reply(ctx, m.ChatID, "Я помогу узнать погоду на сегодня и завтра. Выберите город, чтобы начать."); err != nil {
			return err
		}
		return h.buttons(ctx, m.ChatID, "Что хотите сделать?", []InlineButton{{"📍 Выбрать город", "setlocation"}})
	case "/help":
		h.clearChoice(m.ChatID)
		return h.reply(ctx, m.ChatID, helpMessage)
	case "/cancel":
		h.clearChoice(m.ChatID)
		return h.reply(ctx, m.ChatID, "Действие отменено. Выберите действие в меню.")
	case "/expired":
		return h.expired(ctx, m.ChatID)
	case "/confirmdelete":
		return h.deleteLocation(ctx, m, args)
	case "/setlocation":
		return h.setLocation(ctx, m, strings.Join(args, " "))
	case "/choose":
		return h.choose(ctx, m, args)
	case "/weatherdetails":
		return h.weatherDetails(ctx, m.ChatID, args)
	case "/tomorrow":
		return h.handleWeather(ctx, m.ChatID, weather.Tomorrow)
	case "/weather":
		h.clearChoice(m.ChatID)
		return h.HandleWeather(ctx, m.ChatID)
	case "/location":
		h.clearChoice(m.ChatID)
		l, err := h.bot.GetLocation(ctx, m.ChatID)
		if errors.Is(err, storage.ErrNotFound) {
			return h.missingLocation(ctx, m.ChatID)
		}
		if err != nil {
			return err
		}
		return h.buttons(ctx, m.ChatID, "Текущая локация: "+l.Name+" ("+l.Timezone+")", []InlineButton{{"🌤 Показать погоду", "weather"}}, []InlineButton{{"📍 Изменить город", "setlocation"}}, []InlineButton{{"Удалить город", "deletelocation"}})
	case "/deletelocation":
		return h.requestDelete(ctx, m)
	default:
		return h.reply(ctx, m.ChatID, "Выберите действие в меню. Чтобы изменить город, нажмите «📍 Выбрать город».")
	}
}
func recognize(text string, entity bool, username string) (string, []string) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !entity {
		return "/help", nil
	}
	word := fields[0]
	parts := strings.SplitN(word, "@", 2)
	if len(parts) == 2 && !strings.EqualFold(parts[1], username) {
		return "ignore", nil
	}
	return strings.ToLower(parts[0]), fields[1:]
}
func (h *TelegramCommandHandler) setLocation(ctx context.Context, m IncomingMessage, city string) error {
	if err := h.awaitCity(m); err != nil {
		return h.reply(ctx, m.ChatID, err.Error())
	}
	if city == "" {
		return h.cityPrompt(ctx, m.ChatID, "Напишите название города.")
	}
	if len([]rune(strings.TrimSpace(city))) < 2 || len(city) > 256 {
		return h.cityPrompt(ctx, m.ChatID, "Введите название города от 2 символов (до 256 байт).")
	}
	var list []location.Location
	var err error
	if g, ok := h.bot.geocoder.(location.CandidateGeocoder); ok {
		list, err = g.Candidates(ctx, city)
	} else {
		var l location.Location
		l, err = h.bot.geocoder.Search(ctx, city)
		if err == nil {
			list = []location.Location{l}
		}
	}
	if errors.Is(err, location.ErrLocationNotFound) || err == nil && len(list) == 0 {
		return h.cityPrompt(ctx, m.ChatID, "Город не найден. Уточните название или добавьте регион.")
	}
	if err != nil {
		return err
	}
	if len(list) > 5 {
		list = list[:5]
	}
	for _, l := range list {
		if err := l.Validate(); err != nil {
			return err
		}
	}
	c, err := h.newSession(m, false, false)
	if err != nil {
		return err
	}
	c.locations = list
	h.mu.Lock()
	h.choices[m.ChatID] = c
	h.mu.Unlock()
	rows := make([][]InlineButton, 0, len(list)+2)
	for i, l := range list {
		rows = append(rows, []InlineButton{{l.Label(), "choose:" + c.nonce + ":" + strconv.Itoa(i+1)}})
	}
	rows = append(rows, []InlineButton{{"Ввести другой город", "setlocation"}}, []InlineButton{{"Отмена", "cancel"}})
	return h.buttons(ctx, m.ChatID, "Какой город вы имели в виду? Подтвердите в течение 5 минут.", rows...)
}
func (h *TelegramCommandHandler) choose(ctx context.Context, m IncomingMessage, args []string) error {
	if len(args) != 2 {
		return h.expired(ctx, m.ChatID)
	}
	h.mu.Lock()
	c, ok := h.choices[m.ChatID]
	h.mu.Unlock()
	n, err := strconv.Atoi(args[1])
	if !ok || c.waiting || c.deleting || c.userID != m.UserID || args[0] != c.nonce || !c.expires.After(h.now()) || err != nil || n < 1 || n > len(c.locations) {
		return h.expired(ctx, m.ChatID)
	}
	l := c.locations[n-1]
	l.Name = l.Label()
	if len(l.Name) > 256 {
		l.Name = c.locations[n-1].Name
	}
	if err := h.bot.locRepo.Save(ctx, m.ChatID, l); err != nil {
		return err
	}
	h.clearForecast(m.ChatID)
	if err := h.buttons(ctx, m.ChatID, "Локация сохранена: "+l.Name+".", []InlineButton{{"🌤 Показать погоду", "weather"}}, []InlineButton{{"📍 Изменить город", "setlocation"}}); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.choices, m.ChatID)
	h.mu.Unlock()
	return nil
}
func (h *TelegramCommandHandler) HandleWeather(ctx context.Context, chatID int64) error {
	return h.handleWeather(ctx, chatID, weather.Today)
}
func (h *TelegramCommandHandler) handleWeather(ctx context.Context, chatID int64, day weather.ForecastDay) error {
	l, err := h.bot.GetLocation(ctx, chatID)
	if errors.Is(err, storage.ErrNotFound) {
		return h.missingLocation(ctx, chatID)
	}
	if err != nil {
		return err
	}
	var result weather.WeatherResult
	if day == weather.Tomorrow {
		result, err = h.bot.weatherService.GetTomorrowWeather(ctx, l)
	} else {
		result, err = h.bot.weatherService.GetTodayWeather(ctx, l)
	}
	if day == weather.Tomorrow && errors.Is(err, weather.ErrNoData) {
		return h.buttons(ctx, chatID, "На завтра пока нет данных. Попробуйте позже.", []InlineButton{{"Обновить", "tomorrow"}})
	}
	if err != nil {
		return err
	}
	return h.sendForecast(ctx, chatID, result)
}
func (h *TelegramCommandHandler) NotifyFailure(ctx context.Context, m IncomingMessage) error {
	if m.ChatType != "private" || m.Ignore {
		return nil
	}
	return h.reply(ctx, m.ChatID, "Сервис временно недоступен. Попробуйте позже.")
}
