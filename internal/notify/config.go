package notify

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	EnvWebhookURL   = "SUBSPACE_NOTIFY_WEBHOOK_URL"
	EnvNtfyURL      = "SUBSPACE_NOTIFY_NTFY_URL"
	EnvSlackURL     = "SUBSPACE_NOTIFY_SLACK_URL"
	EnvSigningKey   = "SUBSPACE_NOTIFY_SECRET"
	EnvAllowPrivate = "SUBSPACE_NOTIFY_ALLOW_PRIVATE"
)

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
	return cfg, err
}
