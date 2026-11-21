package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestParsePRURL(t *testing.T) {
	c := New(Config{})
	ok := map[string]PRRef{
		"https://github.com/SigmaUno/subspace/pull/12":       {"SigmaUno/subspace", 12},
		"https://github.com/SigmaUno/subspace/pull/12/files": {"SigmaUno/subspace", 12},
		"https://github.com/a/b.c/pull/1/":                   {"a/b.c", 1},
	}
	for in, want := range ok {
		got, err := c.ParsePRURL(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{
		"", "http://github.com/a/b/pull/1", "https://gitlab.com/a/b/pull/1",
		"https://github.com.evil.com/a/b/pull/1", "https://github.com/a/b/issues/1",
		"https://github.com/a/b/pull/x", "https://github.com/a/b/pull/0",
		"https://github.com/a/b/pull/1/evil", "https://github.com/../b/pull/1",
		"https://user@github.com/a/b/pull/1",
	} {
		if _, err := c.ParsePRURL(in); !errors.Is(err, ErrInvalidPRURL) {
			t.Errorf("%q: expected ErrInvalidPRURL, got %v", in, err)
		}
	}
}

func TestValidateBranch(t *testing.T) {
	c := New(Config{})
	for _, b := range []string{"sub/t-3-add-login", "sub/t-12-x", "sub/t-1-a-b-c9"} {
		if err := c.ValidateBranch(b); err != nil {
			t.Errorf("%s: %v", b, err)
		}
	}
	for _, b := range []string{"", "main", "sub/", "sub/t-3", "sub/t-3-", "sub/3-add", "sub/t-3-Add", "sub/t-3-a--b", "feature/t-3-x", "sub/t-3-a/b", "sub/t-3-a b", "sub/t-3-../x"} {
		if err := c.ValidateBranch(b); !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("%q: expected ErrInvalidBranch, got %v", b, err)
		}
	}
}

func TestVerifyWebhook(t *testing.T) {
	c := New(Config{WebhookSecret: "s3cret"})
	body := []byte(`{"a":1}`)
	good := map[string][]string{"x-hub-signature-256": {sign("s3cret", body)}}
	if err := c.VerifyWebhook(good, body); err != nil {
		t.Fatal(err)
	}
	bad := []map[string][]string{
		nil,
		{"X-Hub-Signature-256": {sign("other", body)}},
		{"X-Hub-Signature-256": {"sha256=zz"}},
		{"X-Hub-Signature-256": {hex.EncodeToString([]byte("x"))}},
		{"X-Hub-Signature-256": {sign("s3cret", []byte("tampered"))}},
	}
	for i, h := range bad {
		if err := c.VerifyWebhook(h, body); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("case %d: got %v", i, err)
		}
	}
	if err := New(Config{}).VerifyWebhook(good, body); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("empty secret must fail, got %v", err)
	}
}

const prJSON = `{"html_url":"https://github.com/o/r/pull/7","number":7,"title":"Add login","state":"closed",
"draft":false,"merged":true,"merged_at":"2026-11-20T21:04:05Z","updated_at":"2026-11-20T21:04:05Z",
"user":{"login":"bot"},"merged_by":{"login":"owner"},"head":{"ref":"sub/t-3-add-login"},"base":{"ref":"main"}}`

func TestGetPR(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch r.URL.Path {
		case "/repos/o/r/pulls/7":
			_, _ = w.Write([]byte(prJSON))
		case "/repos/o/r/pulls/8":
			w.WriteHeader(404)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := New(Config{APIURL: srv.URL + "/", Token: "tok"})

	pr, err := c.GetPR(context.Background(), PRRef{"o/r", 7})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotPath != "/repos/o/r/pulls/7" {
		t.Errorf("auth=%q path=%q", gotAuth, gotPath)
	}
	if pr.State != "merged" || !pr.Merged || pr.Branch != "sub/t-3-add-login" || pr.BaseRef != "main" ||
		pr.MergedBy != "owner" || pr.Author != "bot" || pr.Title != "Add login" || pr.MergedAt == nil {
		t.Errorf("unexpected pr: %+v", pr)
	}
	if _, err := c.GetPR(context.Background(), PRRef{"o/r", 8}); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := c.GetPR(context.Background(), PRRef{"o/r", 9}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("want generic error, got %v", err)
	}
	if _, err := c.GetPR(context.Background(), PRRef{"bad", 1}); !errors.Is(err, ErrInvalidPRURL) {
		t.Errorf("want invalid ref, got %v", err)
	}
}

func TestParseWebhook(t *testing.T) {
	c := New(Config{})
	h := map[string][]string{"X-GitHub-Event": {"pull_request"}}
	merged := []byte(`{"action":"closed","repository":{"full_name":"o/r"},"pull_request":` + prJSON + `}`)
	ev, err := c.ParseWebhook(h, merged)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventPRMerged || ev.Repo != "o/r" || ev.PR != (PRRef{"o/r", 7}) ||
		ev.Branch != "sub/t-3-add-login" || ev.MergedBy != "owner" || ev.MergedAt == nil ||
		ev.URL != "https://github.com/o/r/pull/7" {
		t.Errorf("unexpected event: %+v", ev)
	}

	closedUnmerged := []byte(`{"action":"closed","repository":{"full_name":"o/r"},"pull_request":{"number":7,"merged":false,"head":{"ref":"x"}}}`)
	opened := []byte(`{"action":"opened","repository":{"full_name":"o/r"},"pull_request":` + prJSON + `}`)
	for name, body := range map[string][]byte{"unmerged": closedUnmerged, "opened": opened} {
		if _, err := c.ParseWebhook(h, body); !errors.Is(err, ErrIgnoredEvent) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := c.ParseWebhook(map[string][]string{"X-GitHub-Event": {"push"}}, merged); !errors.Is(err, ErrIgnoredEvent) {
		t.Errorf("push: got %v", err)
	}
	if _, err := c.ParseWebhook(h, []byte("{")); err == nil {
		t.Error("expected decode error")
	}
}
