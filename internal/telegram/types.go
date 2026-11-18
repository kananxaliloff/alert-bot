// Package telegram is a small client for the Telegram Bot API, built on the
// standard library only. It covers what this bot needs: long polling,
// sending and editing messages, inline keyboards and callback answers.
package telegram

// Update is one incoming event.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a chat message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text,omitempty"`
}

// User is a Telegram account.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// Chat is a conversation. For private chats the chat id equals the user id.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private, group, supergroup, channel
}

// CallbackQuery is a press on an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// InlineKeyboardMarkup is a grid of buttons attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton is one button. CallbackData is limited to 64 bytes by Telegram.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// BotCommand is one entry of the command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SendMessageParams are the arguments of SendMessage.
type SendMessageParams struct {
	ChatID              int64
	Text                string
	ParseMode           string // "HTML" or ""
	ReplyMarkup         *InlineKeyboardMarkup
	DisableLinkPreview  bool
	DisableNotification bool
}

// EditMessageTextParams are the arguments of EditMessageText.
type EditMessageTextParams struct {
	ChatID             int64
	MessageID          int64
	Text               string
	ParseMode          string
	ReplyMarkup        *InlineKeyboardMarkup // nil removes the keyboard
	DisableLinkPreview bool
}

// Button builds a callback button.
func Button(text, data string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: data}
}

// Keyboard builds a keyboard from rows of buttons.
func Keyboard(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// Row is a convenience for one row of buttons.
func Row(buttons ...InlineKeyboardButton) []InlineKeyboardButton { return buttons }
