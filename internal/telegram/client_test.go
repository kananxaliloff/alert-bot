package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:SECRET-token-value"

// fakeAPI starts a server that records the last request and answers with reply.
type fakeAPI struct {
	srv    *httptest.Server
	path   string
	body   map[string]any
	status int
	reply  string
}

func newFakeAPI(t *testing.T, reply string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: 200, reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		f.body = map[string]any{}
		_ = json.Unmarshal(raw, &f.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *Client { return NewClient(testToken, WithBaseURL(f.srv.URL)) }

func TestGetMe(t *testing.T) {
	f := newFakeAPI(t, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Alerts","username":"alerts_bot"}}`)
	me, err := f.client().GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.ID != 42 || me.Username != "alerts_bot" || !me.IsBot {
		t.Errorf("me = %+v", me)
	}
	if f.path != "/bot"+testToken+"/getMe" {
		t.Errorf("path = %q", f.path)
	}
}

func TestGetUpdates(t *testing.T) {
	f := newFakeAPI(t, `{"ok":true,"result":[
		{"update_id":10,"message":{"message_id":1,"from":{"id":7,"first_name":"Ann"},"chat":{"id":7,"type":"private"},"date":1,"text":"/start"}},
		{"update_id":11,"callback_query":{"id":"cb1","from":{"id":7,"first_name":"Ann"},"message":{"message_id":5,"chat":{"id":7,"type":"private"},"date":1},"data":"a:pl"}}
	]}`)
	ups, err := f.client().GetUpdates(context.Background(), 10, 25*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 {
		t.Fatalf("got %d updates", len(ups))
	}
	if ups[0].Message == nil || ups[0].Message.Text != "/start" || ups[0].Message.From.ID != 7 || ups[0].Message.Chat.Type != "private" {
		t.Errorf("message update = %+v", ups[0])
	}
	cb := ups[1].CallbackQuery
	if cb == nil || cb.ID != "cb1" || cb.Data != "a:pl" || cb.Message.MessageID != 5 || cb.From.ID != 7 {
		t.Errorf("callback update = %+v", ups[1])
	}
	if f.body["offset"] != float64(10) || f.body["timeout"] != float64(25) {
		t.Errorf("request body = %v", f.body)
	}
	allowed, _ := f.body["allowed_updates"].([]any)
	if len(allowed) != 2 {
		t.Errorf("allowed_updates = %v", f.body["allowed_updates"])
	}
}

func TestSendMessage(t *testing.T) {
	f := newFakeAPI(t, `{"ok":true,"result":{"message_id":99,"chat":{"id":7,"type":"private"},"date":1,"text":"hi"}}`)
	kb := Keyboard(Row(Button("Yes", "y"), Button("No", "n")))
	m, err := f.client().SendMessage(context.Background(), SendMessageParams{
		ChatID: 7, Text: "hi <b>there</b>", ParseMode: "HTML", ReplyMarkup: kb, DisableLinkPreview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageID != 99 {
		t.Errorf("message id = %d", m.MessageID)
	}
	if f.path != "/bot"+testToken+"/sendMessage" {
		t.Errorf("path = %q", f.path)
	}
	if f.body["chat_id"] != float64(7) || f.body["text"] != "hi <b>there</b>" || f.body["parse_mode"] != "HTML" {
		t.Errorf("body = %v", f.body)
	}
	rm, _ := f.body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	if len(rows) != 1 || len(rows[0].([]any)) != 2 {
		t.Errorf("reply_markup = %v", f.body["reply_markup"])
	}
	lp, _ := f.body["link_preview_options"].(map[string]any)
	if lp["is_disabled"] != true {
		t.Errorf("link preview not disabled: %v", f.body)
	}
}

func TestEditMessageText_ClearsKeyboardWhenNil(t *testing.T) {
	f := newFakeAPI(t, `{"ok":true,"result":true}`)
	err := f.client().EditMessageText(context.Background(), EditMessageTextParams{ChatID: 7, MessageID: 5, Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	rm, ok := f.body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing: %v", f.body)
	}
	if rows, _ := rm["inline_keyboard"].([]any); len(rows) != 0 {
		t.Errorf("keyboard should be empty, got %v", rm)
	}
}

func TestAnswerCallbackQueryAndDeleteAndCommands(t *testing.T) {
	f := newFakeAPI(t, `{"ok":true,"result":true}`)
	c := f.client()
	ctx := context.Background()

	if err := c.AnswerCallbackQuery(ctx, "cb1", "Saved", true); err != nil {
		t.Fatal(err)
	}
	if f.body["callback_query_id"] != "cb1" || f.body["text"] != "Saved" || f.body["show_alert"] != true {
		t.Errorf("answerCallbackQuery body = %v", f.body)
	}
	if err := c.DeleteMessage(ctx, 7, 5); err != nil {
		t.Fatal(err)
	}
	if f.path != "/bot"+testToken+"/deleteMessage" || f.body["message_id"] != float64(5) {
		t.Errorf("deleteMessage: %s %v", f.path, f.body)
	}
	if err := c.SetMyCommands(ctx, []BotCommand{{Command: "start", Description: "Begin"}}); err != nil {
		t.Fatal(err)
	}
	cmds, _ := f.body["commands"].([]any)
	if len(cmds) != 1 {
		t.Errorf("setMyCommands body = %v", f.body)
	}
}

func TestAPIErrors(t *testing.T) {
	t.Run("blocked", func(t *testing.T) {
		f := newFakeAPI(t, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
		f.status = 403
		_, err := f.client().SendMessage(context.Background(), SendMessageParams{ChatID: 1, Text: "x"})
		if !IsBlocked(err) {
			t.Fatalf("IsBlocked = false for %v", err)
		}
		if _, limited := RetryAfter(err); limited {
			t.Error("403 is not a rate limit")
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		f := newFakeAPI(t, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`)
		f.status = 429
		_, err := f.client().SendMessage(context.Background(), SendMessageParams{ChatID: 1, Text: "x"})
		d, limited := RetryAfter(err)
		if !limited || d != 7*time.Second {
			t.Fatalf("RetryAfter = %v, %v for %v", d, limited, err)
		}
		if IsBlocked(err) {
			t.Error("429 is not a block")
		}
	})
	t.Run("not modified", func(t *testing.T) {
		f := newFakeAPI(t, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same"}`)
		f.status = 400
		err := f.client().EditMessageText(context.Background(), EditMessageTextParams{ChatID: 1, MessageID: 2, Text: "x"})
		if !IsNotModified(err) {
			t.Fatalf("IsNotModified = false for %v", err)
		}
	})
	t.Run("other 400 is not 'not modified'", func(t *testing.T) {
		f := newFakeAPI(t, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`)
		f.status = 400
		err := f.client().EditMessageText(context.Background(), EditMessageTextParams{ChatID: 1, MessageID: 2, Text: "x"})
		if err == nil || IsNotModified(err) {
			t.Fatalf("unexpected: %v", err)
		}
	})
	t.Run("gateway html", func(t *testing.T) {
		f := newFakeAPI(t, `<html>502 Bad Gateway</html>`)
		f.status = 502
		_, err := f.client().GetMe(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNetworkErrorNeverContainsToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more: connection refused, error text would contain the URL

	c := NewClient(testToken, WithBaseURL(url))
	_, err := c.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "<token>") {
		t.Errorf("expected the token to be replaced, got: %v", err)
	}
}

func TestContextCancelStillWorksThroughRedaction(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewClient(testToken, WithBaseURL(srv.URL)).GetMe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded) = false for %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestAPIErrorText(t *testing.T) {
	e := &APIError{Method: "sendMessage", Code: 429, Description: "Too Many Requests", RetryAfter: 3 * time.Second}
	if !strings.Contains(e.Error(), "retry after 3s") {
		t.Errorf("error text = %q", e.Error())
	}
}
