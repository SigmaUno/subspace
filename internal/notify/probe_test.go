package notify_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestSendTestNotificationReportsEverySink(t *testing.T) {
	var mu sync.Mutex
	var hook notify.Notification
	var hookSig string
	webhook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hookSig = r.Header.Get(notify.SignatureHeader)
		_ = json.NewDecoder(r.Body).Decode(&hook)
	}))
	defer webhook.Close()
	var slackBody string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		slackBody = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer slack.Close()
	t.Setenv(notify.EnvAllowPrivate, "true")
	t.Setenv(notify.EnvWebhookURL, webhook.URL)
	t.Setenv(notify.EnvSigningKey, "signing-key-for-tests")
	t.Setenv(notify.EnvSlackURL, slack.URL+"/hooks/very-secret-path")
	env := servertest.New(t)

	code, out := env.REST("POST", "/api/v1/notify/test", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("status %d %v", code, out)
	}
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results %v", out)
	}
	byName := map[string]map[string]any{}
	for _, r := range results {
		m := r.(map[string]any)
		byName[m["sink"].(string)] = m
	}
	if byName["webhook"]["ok"] != true {
		t.Fatalf("webhook %v", byName["webhook"])
	}
	if byName["slack"]["ok"] != false || !strings.Contains(byName["slack"]["error"].(string), "400") {
		t.Fatalf("slack %v", byName["slack"])
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "very-secret-path") || strings.Contains(string(raw), "signing-key") {
		t.Fatalf("response leaks configuration: %s", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if hook.Kind != "test" || !strings.HasPrefix(hook.Title, "[TEST]") || hook.ID == "" || len(hook.Actions) != 0 {
		t.Fatalf("webhook got %+v", hook)
	}
	if !strings.HasPrefix(hookSig, "sha256=") {
		t.Fatalf("signature %q", hookSig)
	}
	if !strings.Contains(slackBody, "[TEST]") {
		t.Fatalf("slack got %s", slackBody)
	}

	_, status := env.REST("GET", "/api/v1/notify/status", nil)
	if status["sent"] != float64(0) || status["failed"] != float64(0) {
		t.Fatalf("test sends must not count as event deliveries: %v", status)
	}
}

func TestSendTestNotificationWithoutSinks(t *testing.T) {
	env := servertest.Start(t, server.Config{PublicURL: "http://sub.test"})
	code, out := env.REST("POST", "/api/v1/notify/test", map[string]any{})
	results, ok := out["results"].([]any)
	if code != http.StatusOK || !ok || len(results) != 0 {
		t.Fatalf("got %d %v", code, out)
	}
}

func TestSendTestNotificationRequiresOwner(t *testing.T) {
	env := servertest.New(t)
	agent := env.Register("w1", "worker", "claude-code")
	for token, want := range map[string]int{"": http.StatusUnauthorized, agent: http.StatusForbidden} {
		req, _ := http.NewRequest("POST", env.TS.URL+"/api/v1/notify/test", strings.NewReader("{}"))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("token %q: status %d want %d", token, resp.StatusCode, want)
		}
	}
}
