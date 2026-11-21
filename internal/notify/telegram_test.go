package notify

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

const testBotToken = "123456:fake-token-for-tests"

func TestMarkdownV2Escaping(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":           "plain text",
		"Task T-7 is blocked":  "Task T\\-7 is blocked",
		"a_b*c[d](e)~f`g":      "a\\_b\\*c\\[d\\]\\(e\\)\\~f\\`g",
		"x>y#z+w-v=u|t{s}r.q!": "x\\>y\\#z\\+w\\-v\\=u\\|t\\{s\\}r\\.q\\!",
		`back\slash`:           `back\\slash`,
	} {
		if got := mdv2Escape(in); got != want {
			t.Errorf("mdv2Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

type telegramCall struct {
	path string
	body map[string]any
}

func fakeTelegram(t *testing.T, status int) (*httptest.Server, *[]telegramCall) {
	t.Helper()
	var calls []telegramCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, telegramCall{path: r.URL.Path, body: body})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestTelegramSendsMarkdownWithInlineKeyboard(t *testing.T) {
	srv, calls := fakeTelegram(t, http.StatusOK)
	sink := NewTelegram(srv.URL, testBotToken, "-100200", NewClient(true, 5*time.Second))
	n := Notification{
		ID: "P-1", Kind: "plan", Title: "Plan P-1 awaits approval", Text: "Agent rex proposed P-1 (brief B-1) with 3 tasks",
		Actions: []Action{{Label: "Approve", URL: "https://sub.test/a/tok1"}, {Label: "Reject", URL: "https://sub.test/a/tok2"}},
	}
	if err := sink.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %d", len(*calls))
	}
	c := (*calls)[0]
	if c.path != "/bot"+testBotToken+"/sendMessage" {
		t.Fatalf("path %s", c.path)
	}
	if c.body["chat_id"] != "-100200" || c.body["parse_mode"] != "MarkdownV2" {
		t.Fatalf("body %v", c.body)
	}
	wantText := "*Plan P\\-1 awaits approval*\nAgent rex proposed P\\-1 \\(brief B\\-1\\) with 3 tasks"
	if c.body["text"] != wantText {
		t.Fatalf("text %q want %q", c.body["text"], wantText)
	}
	markup, _ := c.body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("keyboard %v", c.body["reply_markup"])
	}
	buttons, _ := rows[0].([]any)
	first, _ := buttons[0].(map[string]any)
	second, _ := buttons[1].(map[string]any)
	if len(buttons) != 2 || first["text"] != "Approve" || first["url"] != "https://sub.test/a/tok1" ||
		second["text"] != "Reject" || second["url"] != "https://sub.test/a/tok2" {
		t.Fatalf("buttons %v", buttons)
	}
}

func TestTelegramWithoutActionsHasNoKeyboard(t *testing.T) {
	srv, calls := fakeTelegram(t, http.StatusOK)
	sink := NewTelegram(srv.URL, testBotToken, "@ops", NewClient(true, 5*time.Second))
	if err := sink.Send(context.Background(), Notification{ID: "T-1", Kind: "blocked", Title: "Task T-1 is blocked"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*calls)[0].body["reply_markup"]; ok {
		t.Fatalf("unexpected keyboard %v", (*calls)[0].body)
	}
}

func TestTelegramErrorsNeverCarryTheToken(t *testing.T) {
	srv, _ := fakeTelegram(t, http.StatusTooManyRequests)
	sink := NewTelegram(srv.URL, testBotToken, "-1", NewClient(true, 5*time.Second))
	err := sink.Send(context.Background(), Notification{ID: "T-1", Title: "x"})
	if err == nil || strings.Contains(err.Error(), testBotToken) {
		t.Fatalf("status error %v", err)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	err = NewTelegram(deadURL, testBotToken, "-1", NewClient(true, time.Second)).Send(context.Background(), Notification{ID: "T-1", Title: "x"})
	if err == nil || strings.Contains(err.Error(), testBotToken) {
		t.Fatalf("transport error leaks token: %v", err)
	}
	if errors.As(err, new(permanentError)) {
		t.Fatalf("transport error must be retryable: %v", err)
	}
}
