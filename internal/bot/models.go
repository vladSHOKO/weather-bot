package bot

type UpdatesResponse struct {
	Messages         []IncomingMessage
	LastUpdateNumber int64
}
type IncomingMessage struct {
	CallbackID    string `json:"callback_id,omitempty"`
	CallbackData  string `json:"callback_data,omitempty"`
	UpdateID      int64  `json:"update_id"`
	ChatID        int64  `json:"chat_id"`
	UserID        int64  `json:"user_id"`
	ChatType      string `json:"chat_type"`
	Text          string `json:"text"`
	CommandEntity bool   `json:"command_entity"`
	Ignore        bool   `json:"ignore"`
}

// ReplyMarkup describes either a persistent menu or buttons under a message.
type ReplyMarkup struct {
	Keyboard       [][]KeyboardButton `json:"keyboard,omitempty"`
	ResizeKeyboard bool               `json:"resize_keyboard,omitempty"`
	IsPersistent   bool               `json:"is_persistent,omitempty"`
	InlineKeyboard [][]InlineButton   `json:"inline_keyboard,omitempty"`
}
type KeyboardButton struct {
	Text string `json:"text"`
}
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type LinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}
