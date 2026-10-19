package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"weather-sum/internal/httpx"
	"weather-sum/internal/location"
	"weather-sum/internal/report"
	"weather-sum/internal/safeerror"
	"weather-sum/internal/storage"
	"weather-sum/internal/weather"
)

type WeatherService interface {
	GetTodayWeather(context.Context, location.Location) (weather.WeatherResult, error)
	GetTomorrowWeather(context.Context, location.Location) (weather.WeatherResult, error)
}
type TelegramBot struct {
	token              string
	locRepo            storage.UserLocationRepository
	geocoder           location.Geocoder
	weatherService     WeatherService
	renderer           report.Renderer
	client, pollClient *http.Client
	PollTimeout        time.Duration
	Username           string
}
type Payload struct {
	ChatID             int64               `json:"chat_id"`
	Text               string              `json:"text"`
	ReplyMarkup        *ReplyMarkup        `json:"reply_markup,omitempty"`
	ParseMode          string              `json:"parse_mode,omitempty"`
	LinkPreviewOptions *LinkPreviewOptions `json:"link_preview_options,omitempty"`
}

func NewTelegramBot(token string, repo storage.UserLocationRepository, g location.Geocoder, w WeatherService, r report.Renderer, c *http.Client) *TelegramBot {
	return &TelegramBot{token: token, locRepo: repo, geocoder: g, weatherService: w, renderer: r, client: c, pollClient: c, PollTimeout: 30 * time.Second}
}
func (b *TelegramBot) SetPollClient(c *http.Client, timeout time.Duration) {
	b.pollClient = c
	b.PollTimeout = timeout
}
func (b *TelegramBot) endpoint(method string) string {
	return "https://api.telegram.org/bot" + b.token + "/" + method
}
func (b *TelegramBot) GetMe(ctx context.Context) (id int64, err error) {
	defer func() { err = safeerror.Wrap("telegram getMe", err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint("getMe"), nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		OK     bool `json:"ok"`
		Result struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if resp.StatusCode != 200 {
		return 0, &httpx.StatusError{Code: resp.StatusCode}
	}
	if err = httpx.Decode(resp.Body, 64<<10, &body); err != nil {
		return 0, err
	}
	if !body.OK || body.Result.ID <= 0 || body.Result.Username == "" {
		return 0, httpx.ErrInvalidResponse
	}
	b.Username = body.Result.Username
	return body.Result.ID, nil
}
func (b *TelegramBot) SendMessage(ctx context.Context, chatID int64, text string) (ok bool, err error) {
	return b.SendMessageWithMarkup(ctx, chatID, text, nil)
}
func (b *TelegramBot) SendMessageWithMarkup(ctx context.Context, chatID int64, text string, markup *ReplyMarkup) (ok bool, err error) {
	defer func() { err = safeerror.Wrap("telegram sendMessage", err) }()
	parts := SplitMessage(text, 3500)
	for i, part := range parts {
		var buttons *ReplyMarkup
		if i == len(parts)-1 {
			buttons = markup
		}
		if err = b.sendPart(ctx, chatID, part, buttons); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SendHTMLMessageWithMarkup keeps complete lines together so tags and entities
// are never split. Renderers escape external text and close tags on each line.
func (b *TelegramBot) SendHTMLMessageWithMarkup(ctx context.Context, chatID int64, text string, markup *ReplyMarkup) (err error) {
	defer func() { err = safeerror.Wrap("telegram sendMessage", err) }()
	parts, err := splitHTMLLines(text, 3500)
	if err != nil {
		return err
	}
	for i, part := range parts {
		payload := Payload{ChatID: chatID, Text: part, ParseMode: "HTML", LinkPreviewOptions: &LinkPreviewOptions{IsDisabled: true}}
		if i == len(parts)-1 {
			payload.ReplyMarkup = markup
		}
		if err = b.post(ctx, "sendMessage", payload); err != nil {
			return err
		}
	}
	return nil
}
func splitHTMLLines(text string, maxUnits int) ([]string, error) {
	var parts []string
	var current strings.Builder
	units := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		n := len(utf16.Encode([]rune(line)))
		if n > maxUnits {
			return nil, fmt.Errorf("formatted message line exceeds limit")
		}
		if units+n > maxUnits {
			parts = append(parts, current.String())
			current.Reset()
			units = 0
		}
		current.WriteString(line)
		units += n
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	if len(parts) == 0 {
		return []string{"Нет данных"}, nil
	}
	return parts, nil
}
func (b *TelegramBot) sendPart(ctx context.Context, chatID int64, text string, markup *ReplyMarkup) error {
	return b.post(ctx, "sendMessage", Payload{ChatID: chatID, Text: text, ReplyMarkup: markup})
}
func (b *TelegramBot) AnswerCallback(ctx context.Context, id string) error {
	return safeerror.Wrap("telegram answerCallbackQuery", b.post(ctx, "answerCallbackQuery", struct {
		ID string `json:"callback_query_id"`
	}{id}))
}
func (b *TelegramBot) post(ctx context.Context, method string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(method), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		OK         bool `json:"ok"`
		Code       int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := httpx.Decode(resp.Body, 64<<10, &body)
	if resp.StatusCode != 200 || !body.OK {
		code := body.Code
		if code < 400 {
			code = resp.StatusCode
		}
		if code < 400 {
			code = 400
		}
		return &httpx.StatusError{Code: code, RetryAfter: boundedRetry(body.Parameters.RetryAfter, resp.Header.Get("Retry-After"))}
	}
	return decodeErr
}
func boundedRetry(seconds int, header string) time.Duration {
	if seconds == 0 {
		seconds, _ = strconv.Atoi(header)
	}
	seconds = max(0, min(seconds, 3600))
	return time.Duration(seconds) * time.Second
}
func (b *TelegramBot) GetUpdates(ctx context.Context, offset int64) (updates UpdatesResponse, err error) {
	defer func() { err = safeerror.Wrap("telegram getUpdates", err) }()
	q := url.Values{"offset": {strconv.FormatInt(offset, 10)}, "timeout": {strconv.Itoa(int(b.PollTimeout.Seconds()))}, "limit": {"100"}, "allowed_updates": {"[\"message\",\"callback_query\"]"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint("getUpdates")+"?"+q.Encode(), nil)
	if err != nil {
		return updates, err
	}
	resp, err := b.pollClient.Do(req)
	if err != nil {
		return updates, err
	}
	defer resp.Body.Close()
	var body struct {
		OK         bool `json:"ok"`
		Code       int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
		Result []struct {
			ID       int64 `json:"update_id"`
			Callback *struct {
				ID   string `json:"id"`
				Data string `json:"data"`
				From struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Message *struct {
					Chat struct {
						ID   int64  `json:"id"`
						Type string `json:"type"`
					} `json:"chat"`
				} `json:"message"`
			} `json:"callback_query"`

			Message *struct {
				From struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				Text     string `json:"text"`
				Entities []struct {
					Type   string `json:"type"`
					Offset int    `json:"offset"`
					Length int    `json:"length"`
				} `json:"entities"`
			} `json:"message"`
		} `json:"result"`
	}
	decodeErr := httpx.Decode(resp.Body, 1<<20, &body)
	if resp.StatusCode != 200 || !body.OK {
		code := body.Code
		if code < 400 {
			code = resp.StatusCode
		}
		if code < 400 {
			code = 400
		}
		return updates, &httpx.StatusError{Code: code, RetryAfter: boundedRetry(body.Parameters.RetryAfter, resp.Header.Get("Retry-After"))}
	}
	if decodeErr != nil {
		return updates, decodeErr
	}
	if len(body.Result) > 100 {
		return updates, httpx.ErrInvalidResponse
	}
	for _, u := range body.Result {
		if u.ID < 0 {
			return UpdatesResponse{}, httpx.ErrInvalidResponse
		}
		m := IncomingMessage{UpdateID: u.ID, Ignore: true}
		if u.Message != nil {
			r := u.Message
			m.ChatID = r.Chat.ID
			m.UserID = r.From.ID
			m.ChatType = r.Chat.Type
			m.Text = r.Text
			m.Ignore = r.Text == "" || r.Chat.ID == 0 || len(r.Text) > 16384
			for _, e := range r.Entities {
				if e.Offset == 0 && e.Length > 0 && e.Type == "bot_command" {
					m.CommandEntity = true
					break
				}
			}
		}
		if c := u.Callback; c != nil && c.Message != nil {
			m.ChatID, m.UserID, m.ChatType = c.Message.Chat.ID, c.From.ID, c.Message.Chat.Type
			m.CallbackID, m.CallbackData = c.ID, c.Data
			m.Ignore = c.ID == "" || len(c.ID) > 256 || c.Data == "" || len(c.Data) > 64 || m.ChatID == 0
		}
		updates.Messages = append(updates.Messages, m)
		updates.LastUpdateNumber = max(updates.LastUpdateNumber, u.ID)
	}
	return updates, nil
}
func (b *TelegramBot) RegisterLocation(ctx context.Context, city string, chatID int64) (location.Location, error) {
	loc, err := b.geocoder.Search(ctx, city)
	if err != nil {
		return location.Location{}, err
	}
	err = b.locRepo.Save(ctx, chatID, loc)
	return loc, err
}
func (b *TelegramBot) GetLocation(ctx context.Context, chatID int64) (location.Location, error) {
	return b.locRepo.GetByChatID(ctx, chatID)
}

// SplitMessage counts UTF-16 units, including astral Unicode characters.
func SplitMessage(text string, maxUnits int) []string {
	if maxUnits < 2 {
		maxUnits = 2
	}
	var out []string
	var b strings.Builder
	units := 0
	for _, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > maxUnits {
			out = append(out, b.String())
			b.Reset()
			units = 0
		}
		b.WriteRune(r)
		units += n
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{"Нет данных"}
	}
	return out
}
