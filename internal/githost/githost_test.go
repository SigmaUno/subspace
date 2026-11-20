package githost

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		repo   string
		number int64
		bad    bool
	}{
		{"forgejo", "https://forge.example.com/SigmaUno/subspace/pulls/12", "SigmaUno/subspace", 12, false},
		{"github style", "https://github.com/SigmaUno/subspace/pull/7", "SigmaUno/subspace", 7, false},
		{"trailing slash", "http://localhost:3000/a/b/pulls/3/", "a/b", 3, false},
		{"files tab", "https://f.example.com/a/b/pulls/3/files", "a/b", 3, false},
		{"issues", "https://f.example.com/a/b/issues/3", "", 0, true},
		{"no number", "https://f.example.com/a/b/pulls/", "", 0, true},
		{"zero", "https://f.example.com/a/b/pulls/0", "", 0, true},
		{"nan", "https://f.example.com/a/b/pulls/x", "", 0, true},
		{"bad scheme", "ftp://f.example.com/a/b/pulls/1", "", 0, true},
		{"empty", "", "", 0, true},
		{"extra segment", "https://f.example.com/a/b/pulls/1/evil", "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, n, err := ParsePRURL(tc.url)
			if tc.bad {
				if !errors.Is(err, ErrInvalidPRURL) {
					t.Fatalf("want ErrInvalidPRURL, got %v", err)
				}
				return
			}
			if err != nil || repo != tc.repo || n != tc.number {
				t.Fatalf("got %q %d %v", repo, n, err)
			}
		})
	}
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))
	tests := []struct {
		name, secret, sig string
		body              []byte
		want              bool
	}{
		{"valid", "s3cret", good, body, true},
		{"valid prefixed", "s3cret", "sha256=" + good, body, true},
		{"wrong secret", "other", good, body, false},
		{"tampered body", "s3cret", good, []byte(`{"a":2}`), false},
		{"empty sig", "s3cret", "", body, false},
		{"empty secret", "", good, body, false},
		{"not hex", "s3cret", "zz", body, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyWebhook(tc.secret, tc.body, tc.sig); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestValidateBranch(t *testing.T) {
	tests := []struct {
		name, id, slug, branch string
		ok                     bool
	}{
		{"ok", "T-3", "add-login", "sub/t-3-add-login", true},
		{"ok lowercase id", "t-12", "x", "sub/t-12-x", true},
		{"wrong prefix", "T-3", "add-login", "feat/t-3-add-login", false},
		{"wrong id", "T-3", "add-login", "sub/t-4-add-login", false},
		{"wrong slug", "T-3", "add-login", "sub/t-3-other", false},
		{"main", "T-3", "add-login", "main", false},
		{"bad task id", "3", "add-login", "sub/3-add-login", false},
		{"bad slug upper", "T-3", "Add", "sub/t-3-Add", false},
		{"bad slug slash", "T-3", "a/b", "sub/t-3-a/b", false},
		{"empty slug", "T-3", "", "sub/t-3-", false},
		{"trailing text", "T-3", "a", "sub/t-3-a-extra", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBranch(tc.id, tc.slug, tc.branch)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidBranch) {
				t.Fatalf("want ErrInvalidBranch, got %v", err)
			}
		})
	}
}

func TestIsMerged(t *testing.T) {
	if !(PullRequestEvent{Action: "closed", Merged: true}).IsMerged() {
		t.Error("closed+merged should be merged")
	}
	if (PullRequestEvent{Action: "closed"}).IsMerged() || (PullRequestEvent{Action: "opened", Merged: true}).IsMerged() {
		t.Error("should not be merged")
	}
}
