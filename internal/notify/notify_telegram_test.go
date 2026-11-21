package notify

import (
	"slices"
	"strings"
	"testing"
)

func TestConfigFromEnvTelegramAndSMTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		sinks   []string
		wantErr bool
		hidden  string
	}{
		{name: "telegram", env: map[string]string{EnvTelegramBot: testBotToken, EnvTelegramChatID: "-100"}, sinks: []string{"telegram"}},
		{name: "telegram token only", env: map[string]string{EnvTelegramBot: testBotToken}, wantErr: true},
		{name: "telegram malformed token", env: map[string]string{EnvTelegramBot: "1/../x", EnvTelegramChatID: "-100"}, wantErr: true, hidden: "1/../x"},
		{
			name: "smtp", sinks: []string{"smtp"},
			env: map[string]string{EnvSMTPURL: "smtp://127.0.0.1:587", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test, b@x.test", EnvAllowPrivate: "1"},
		},
		{name: "smtp without recipients", env: map[string]string{EnvSMTPURL: "smtp://mail.example.com", EnvSMTPFrom: "bot@sub.test"}, wantErr: true},
		{name: "smtp private without opt-in", env: map[string]string{EnvSMTPURL: "smtp://10.0.0.5", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test"}, wantErr: true},
		{
			name:    "smtp credentials with starttls disabled",
			env:     map[string]string{EnvSMTPURL: "smtp://bot:hunter2@mail.example.com", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test", EnvSMTPSTARTTLS: "off"},
			wantErr: true, hidden: "hunter2",
		},
		{
			name: "smtp url with password is not echoed", wantErr: true, hidden: "hunter2",
			env: map[string]string{EnvSMTPURL: "smtp://bot:hunter2@10.0.0.5", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(func(k string) string { return tc.env[k] })
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tc.hidden != "" && strings.Contains(err.Error(), tc.hidden) {
					t.Fatalf("error leaks secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := New(cfg).SinkNames(); !slices.Equal(got, tc.sinks) {
				t.Fatalf("sinks %v want %v", got, tc.sinks)
			}
		})
	}
}
