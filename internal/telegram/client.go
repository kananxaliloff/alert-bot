package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// maxResponseBytes bounds how much of a response we are willing to read.
const maxResponseBytes = 8 << 20

// Client talks to the Telegram Bot API. It is safe for concurrent use.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL points the client at another API server (tests, local Bot API server).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the HTTP client. Do not set a Timeout on it:
// deadlines are applied per request, long polling needs more than a short one.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient creates a client for the given bot token.
func NewClient(token string, opts ...Option) *Client {
	c := &Client{token: token, baseURL: defaultBaseURL, http: &http.Client{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is an error reported by Telegram itself.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram %s: %d %s (retry after %s)", e.Method, e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// IsBlocked reports whether the user blocked the bot or deleted their account,
// so there is no point sending them anything.
func IsBlocked(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != http.StatusForbidden {
		return false
	}
	d := strings.ToLower(ae.Description)
	return strings.Contains(d, "blocked") || strings.Contains(d, "deactivated") || strings.Contains(d, "chat not found")
}

// RetryAfter reports whether err is a rate limit and how long to wait.
func RetryAfter(err error) (time.Duration, bool) {
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests {
		if ae.RetryAfter <= 0 {
			return time.Second, true
		}
		return ae.RetryAfter, true
	}
	return 0, false
}

// IsNotModified reports Telegram's harmless complaint when an edit changes nothing.
func IsNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(ae.Description), "message is not modified")
}

// redactedError hides the bot token from error text (net/http puts the whole
// request URL, token included, into its errors) while keeping the cause
// available to errors.Is and errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func (c *Client) redact(method string, err error) error {
	msg := strings.ReplaceAll(err.Error(), c.token, "<token>")
	return &redactedError{msg: fmt.Sprintf("telegram %s: %s", method, msg), err: err}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call performs one API request. params is JSON-encoded, out (optional)
// receives the "result" field.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.redact(method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return c.redact(method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return c.redact(method, err)
	}

	var ar apiResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		// Not JSON: a proxy or gateway answered instead of Telegram.
		return fmt.Errorf("telegram %s: unexpected response (HTTP %d, %d bytes)", method, resp.StatusCode, len(raw))
	}
	if !ar.OK {
		code := ar.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			Method: method, Code: code, Description: ar.Description,
			RetryAfter: time.Duration(ar.Parameters.RetryAfter) * time.Second,
		}
	}
	if out != nil && len(ar.Result) > 0 {
		if err := json.Unmarshal(ar.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

// GetMe returns the bot's own account; handy to check the token.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates newer than offset. timeout is how long
// Telegram may hold the request open when there is nothing to report.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	// Leave room for the network round trip on top of Telegram's wait.
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()

	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (Message, error) {
	body := map[string]any{"chat_id": p.ChatID, "text": p.Text}
	if p.ParseMode != "" {
		body["parse_mode"] = p.ParseMode
	}
	if p.ReplyMarkup != nil {
		body["reply_markup"] = p.ReplyMarkup
	}
	if p.DisableLinkPreview {
		body["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	if p.DisableNotification {
		body["disable_notification"] = true
	}
	var m Message
	err := c.call(ctx, "sendMessage", body, &m)
	return m, err
}

// EditMessageText replaces the text and keyboard of a message the bot sent.
func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) error {
	body := map[string]any{"chat_id": p.ChatID, "message_id": p.MessageID, "text": p.Text}
	if p.ParseMode != "" {
		body["parse_mode"] = p.ParseMode
	}
	// Telegram removes the keyboard when reply_markup is omitted; sending an
	// empty keyboard makes that explicit.
	if p.ReplyMarkup != nil {
		body["reply_markup"] = p.ReplyMarkup
	} else {
		body["reply_markup"] = InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	}
	if p.DisableLinkPreview {
		body["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	return c.call(ctx, "editMessageText", body, nil)
}

// DeleteMessage removes a message. Best effort: callers usually ignore the error.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// AnswerCallbackQuery acknowledges a button press, which stops the spinner on
// the button. text, if not empty, is shown to the user.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, showAlert bool) error {
	body := map[string]any{"callback_query_id": id}
	if text != "" {
		body["text"] = text
	}
	if showAlert {
		body["show_alert"] = true
	}
	return c.call(ctx, "answerCallbackQuery", body, nil)
}

// SetMyCommands sets the command menu shown in the chat input.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
