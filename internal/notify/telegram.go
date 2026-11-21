package notify

import (
	"encoding/json"
	"net/http"
	"strings"
)

const (
	telegramAPI     = "https://api.telegram.org"
	telegramMaxText = 1000
)

var mdv2Escaper = strings.NewReplacer(
	"\\", "\\\\", "_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "~", "\\~",
	"`", "\\`", ">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-", "=", "\\=", "|", "\\|", "{", "\\{",
	"}", "\\}", ".", "\\.", "!", "\\!",
)

func mdv2Escape(s string) string { return mdv2Escaper.Replace(s) }

func NewTelegram(base, token, chatID string, c *http.Client) Sink {
	return &httpSink{name: "telegram", url: base + "/bot" + token + "/sendMessage", client: c, build: func(n Notification) ([]byte, http.Header, error) {
		body := map[string]any{
			"chat_id":                  chatID,
			"text":                     "*" + mdv2Escape(truncate(n.Title, maxTitle)) + "*\n" + mdv2Escape(truncate(n.Text, telegramMaxText)),
			"parse_mode":               "MarkdownV2",
			"disable_web_page_preview": true,
		}
		if len(n.Actions) > 0 {
			row := make([]map[string]string, 0, len(n.Actions))
			for _, a := range n.Actions[:min(len(n.Actions), maxActions)] {
				row = append(row, map[string]string{"text": a.Label, "url": a.URL})
			}
			body["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{row}}
		}
		b, err := json.Marshal(body)
		return b, http.Header{"Content-Type": {"application/json"}}, err
	}}
}
