package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

const testSecret = "test-secret-not-real"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

type recorder struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (r *recorder) fn(_ context.Context, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return r.err
}

func newServer(t *testing.T, rec *recorder) http.Handler {
	t.Helper()
	h, err := NewHandler(testSecret, rec.fn)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func post(h http.Handler, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var deliverySeq atomic.Int64

func nextDelivery() string { return fmt.Sprintf("d-%d", deliverySeq.Add(1)) }

func ghHeaders(body []byte, event string) map[string]string {
	return map[string]string{"X-Hub-Signature-256": "sha256=" + sign(testSecret, body), "X-GitHub-Event": event, "X-GitHub-Delivery": nextDelivery()}
}

func fgHeaders(name string, body []byte, event string) map[string]string {
	return map[string]string{"X-" + name + "-Signature": sign(testSecret, body), "X-" + name + "-Event": event, "X-" + name + "-Delivery": nextDelivery()}
}

func TestEvents(t *testing.T) {
	gh := "https://github.com/SigmaUno/foo/pull/7"
	fg := "https://git.example.com/SigmaUno/bar/pulls/12"
	tests := []struct {
		name    string
		file    string
		headers func(body []byte) map[string]string
		want    *Event
		status  int
	}{
		{"github opened", "github_pr_opened.json", func(b []byte) map[string]string { return ghHeaders(b, "pull_request") },
			&Event{ProviderGitHub, "SigmaUno/foo", 7, "sub/t-3-add-login", false, false, gh}, 200},
		{"github synchronize", "github_pr_synchronize.json", func(b []byte) map[string]string { return ghHeaders(b, "pull_request") },
			&Event{ProviderGitHub, "SigmaUno/foo", 7, "sub/t-3-add-login", false, false, gh}, 200},
		{"github merged", "github_pr_merged.json", func(b []byte) map[string]string { return ghHeaders(b, "pull_request") },
			&Event{ProviderGitHub, "SigmaUno/foo", 7, "sub/t-3-add-login", true, false, gh}, 200},
		{"github closed unmerged", "github_pr_closed.json", func(b []byte) map[string]string { return ghHeaders(b, "pull_request") },
			&Event{ProviderGitHub, "SigmaUno/foo", 8, "sub/t-4-wip", false, true, "https://github.com/SigmaUno/foo/pull/8"}, 200},
		{"github labeled ignored", "github_pr_labeled.json", func(b []byte) map[string]string { return ghHeaders(b, "pull_request") }, nil, 202},
		{"github ping ignored", "github_ping.json", func(b []byte) map[string]string { return ghHeaders(b, "ping") }, nil, 202},
		{"github other event ignored", "github_pr_opened.json", func(b []byte) map[string]string { return ghHeaders(b, "issues") }, nil, 202},
		{"forgejo opened", "forgejo_pr_opened.json", func(b []byte) map[string]string {
			return fgHeaders("Forgejo", b, "pull_request")
		}, &Event{ProviderForgejo, "SigmaUno/bar", 12, "sub/t-5-api", false, false, fg}, 200},
		{"gitea synchronized", "forgejo_pr_synchronized.json", func(b []byte) map[string]string {
			return fgHeaders("Gitea", b, "pull_request_sync")
		}, &Event{ProviderForgejo, "SigmaUno/bar", 12, "sub/t-5-api", false, false, fg}, 200},
		{"forgejo merged", "forgejo_pr_merged.json", func(b []byte) map[string]string {
			return fgHeaders("Forgejo", b, "pull_request")
		}, &Event{ProviderForgejo, "SigmaUno/bar", 12, "sub/t-5-api", true, false, fg}, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			body := fixture(t, tc.file)
			w := post(newServer(t, rec), body, tc.headers(body))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.want == nil {
				if len(rec.events) != 0 {
					t.Fatalf("unexpected events: %+v", rec.events)
				}
				return
			}
			if len(rec.events) != 1 || rec.events[0] != *tc.want {
				t.Fatalf("events = %+v, want %+v", rec.events, *tc.want)
			}
		})
	}
}

func TestBadSignature(t *testing.T) {
	body := fixture(t, "github_pr_opened.json")
	tests := map[string]map[string]string{
		"missing":          {"X-GitHub-Event": "pull_request"},
		"wrong secret":     {"X-Hub-Signature-256": "sha256=" + sign("other", body), "X-GitHub-Event": "pull_request"},
		"not hex":          {"X-Hub-Signature-256": "sha256=zzzz", "X-GitHub-Event": "pull_request"},
		"truncated":        {"X-Hub-Signature-256": "sha256=" + sign(testSecret, body)[:20], "X-GitHub-Event": "pull_request"},
		"forgejo wrong":    {"X-Forgejo-Signature": sign("other", body), "X-Forgejo-Event": "pull_request"},
		"gitea wrong":      {"X-Gitea-Signature": sign("other", body), "X-Gitea-Event": "pull_request"},
		"tampered body":    {"X-Hub-Signature-256": "sha256=" + sign(testSecret, append([]byte("x"), body...)), "X-GitHub-Event": "pull_request"},
		"unsigned ignored": {"X-GitHub-Event": "ping"},
	}
	for name, hdr := range tests {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			w := post(newServer(t, rec), body, hdr)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if len(rec.events) != 0 {
				t.Fatal("callback must not run")
			}
		})
	}
}

func TestReplay(t *testing.T) {
	rec := &recorder{}
	h := newServer(t, rec)
	body := fixture(t, "github_pr_opened.json")
	hdr := ghHeaders(body, "pull_request")
	if w := post(h, body, hdr); w.Code != http.StatusOK {
		t.Fatalf("first status = %d", w.Code)
	}
	if w := post(h, body, hdr); w.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, want 202", w.Code)
	}
	if len(rec.events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.events))
	}
	if w := post(h, body, ghHeaders(body, "pull_request")); w.Code != http.StatusOK || len(rec.events) != 2 {
		t.Fatalf("new delivery id must be processed: %d, %d events", w.Code, len(rec.events))
	}
}

func TestFailedDeliveryCanBeRetried(t *testing.T) {
	rec := &recorder{err: errors.New("boom")}
	h := newServer(t, rec)
	body := fixture(t, "github_pr_opened.json")
	hdr := ghHeaders(body, "pull_request")
	if w := post(h, body, hdr); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	rec.err = nil
	if w := post(h, body, hdr); w.Code != http.StatusOK {
		t.Fatalf("retry status = %d", w.Code)
	}
}

func TestMissingDeliveryID(t *testing.T) {
	rec := &recorder{}
	body := fixture(t, "github_pr_opened.json")
	hdr := ghHeaders(body, "pull_request")
	delete(hdr, "X-GitHub-Delivery")
	if w := post(newServer(t, rec), body, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(rec.events) != 0 {
		t.Fatal("callback must not run")
	}
}

func TestBodyLimit(t *testing.T) {
	rec := &recorder{}
	body := bytes.Repeat([]byte("a"), MaxBodyBytes+1)
	w := post(newServer(t, rec), body, ghHeaders(body, "pull_request"))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if len(rec.events) != 0 {
		t.Fatal("callback must not run")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newServer(t, &recorder{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestBadJSONSigned(t *testing.T) {
	body := []byte("{not json")
	w := post(newServer(t, &recorder{}), body, ghHeaders(body, "pull_request"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCallbackError(t *testing.T) {
	rec := &recorder{err: errors.New("boom")}
	body := fixture(t, "github_pr_merged.json")
	w := post(newServer(t, rec), body, ghHeaders(body, "pull_request"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("boom")) {
		t.Fatal("callback error leaked to client")
	}
}

func TestNewHandlerValidation(t *testing.T) {
	if _, err := NewHandler("", func(context.Context, Event) error { return nil }); err == nil {
		t.Fatal("empty secret accepted")
	}
	if _, err := NewHandler("s", nil); err == nil {
		t.Fatal("nil callback accepted")
	}
}
