package notify

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/reload"
)

const (
	EnvWebhookURL     = "SUBSPACE_NOTIFY_WEBHOOK_URL"
	EnvNtfyURL        = "SUBSPACE_NOTIFY_NTFY_URL"
	EnvSlackURL       = "SUBSPACE_NOTIFY_SLACK_URL"
	EnvSigningKey     = "SUBSPACE_NOTIFY_SECRET"
	EnvAllowPrivate   = "SUBSPACE_NOTIFY_ALLOW_PRIVATE"
	EnvTelegramBot    = "SUBSPACE_NOTIFY_TELEGRAM_BOT_TOKEN"
	EnvTelegramChatID = "SUBSPACE_NOTIFY_TELEGRAM_CHAT_ID"
	EnvSMTPURL        = "SUBSPACE_NOTIFY_SMTP_URL"
	EnvSMTPFrom       = "SUBSPACE_NOTIFY_SMTP_FROM"
	EnvSMTPTo         = "SUBSPACE_NOTIFY_SMTP_TO"
	EnvSMTPSTARTTLS   = "SUBSPACE_NOTIFY_SMTP_STARTTLS"
)

var ReloadKeys = []string{
	EnvWebhookURL, EnvNtfyURL, EnvSlackURL, EnvSigningKey, EnvAllowPrivate, EnvTelegramBot, EnvTelegramChatID,
	EnvSMTPURL, EnvSMTPFrom, EnvSMTPTo, EnvSMTPSTARTTLS,
}

func (s *Service) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	cfg, err := ConfigFromEnv(getenv)
	if err != nil {
		return reload.Plan{}, err
	}
	return reload.Plan{Commit: func() { s.ReplaceSinks(cfg.Sinks) }}, nil
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	allow := false
	switch strings.ToLower(strings.TrimSpace(getenv(EnvAllowPrivate))) {
	case "1", "true", "yes":
		allow = true
	}
	client := NewClient(allow, 10*time.Second)
	var cfg Config
	add := func(env string, mk func(url string, c *http.Client) Sink) error {
		u := strings.TrimSpace(getenv(env))
		if u == "" {
			return nil
		}
		if err := CheckURL(u, allow); err != nil {
			return fmt.Errorf("%s: %w", env, err)
		}
		cfg.Sinks = append(cfg.Sinks, mk(u, client))
		return nil
	}
	err := add(EnvWebhookURL, func(u string, c *http.Client) Sink { return NewWebhook(u, getenv(EnvSigningKey), c) })
	if err == nil {
		err = add(EnvNtfyURL, NewNtfy)
	}
	if err == nil {
		err = add(EnvSlackURL, NewSlack)
	}
	if err == nil {
		err = addTelegram(&cfg, getenv, client)
	}
	if err == nil {
		err = addSMTP(&cfg, getenv, allow)
	}
	return cfg, err
}

func addTelegram(cfg *Config, getenv func(string) string, c *http.Client) error {
	token := strings.TrimSpace(getenv(EnvTelegramBot))
	chat := strings.TrimSpace(getenv(EnvTelegramChatID))
	if token == "" && chat == "" {
		return nil
	}
	if token == "" || chat == "" {
		return fmt.Errorf("%s and %s must be set together", EnvTelegramBot, EnvTelegramChatID)
	}
	if strings.ContainsAny(token, "/?#@ \t\r\n") {
		return fmt.Errorf("%s: malformed bot token", EnvTelegramBot)
	}
	cfg.Sinks = append(cfg.Sinks, NewTelegram(telegramAPI, token, chat, c))
	return nil
}

func addSMTP(cfg *Config, getenv func(string) string, allow bool) error {
	raw := strings.TrimSpace(getenv(EnvSMTPURL))
	if raw == "" {
		return nil
	}
	from := strings.TrimSpace(getenv(EnvSMTPFrom))
	var to []string
	for _, t := range strings.Split(getenv(EnvSMTPTo), ",") {
		if t = strings.TrimSpace(t); t != "" {
			to = append(to, t)
		}
	}
	if from == "" || len(to) == 0 {
		return fmt.Errorf("%s and %s are required with %s", EnvSMTPFrom, EnvSMTPTo, EnvSMTPURL)
	}
	s, err := NewSMTP(raw, SMTPOptions{From: from, To: to, STARTTLS: smtpSTARTTLS(getenv), AllowPrivate: allow})
	if err != nil {
		return fmt.Errorf("%s: %w", EnvSMTPURL, err)
	}
	cfg.Sinks = append(cfg.Sinks, s)
	return nil
}

func smtpSTARTTLS(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvSMTPSTARTTLS))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
