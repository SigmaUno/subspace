// Package githost abstracts the git host (Forgejo, GitHub) that holds repos, branches and PRs.
package githost

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrNotFound      = errors.New("githost: not found")
	ErrInvalidPRURL  = errors.New("githost: invalid pull request url")
	ErrInvalidBranch = errors.New("githost: invalid branch name")
)

// PR is the subset of pull request data Subspace cares about.
type PR struct {
	Repo       string // owner/name
	Number     int64
	Title      string
	State      string // open | closed
	Merged     bool
	HeadBranch string
	BaseBranch string
	URL        string
}

// Host is a git host client.
type Host interface {
	GetPR(ctx context.Context, repo string, number int64) (PR, error)
	ParsePRURL(prURL string) (repo string, number int64, err error)
	VerifyWebhook(secret string, body []byte, signatureHeader string) bool
	ValidateBranch(taskID, slug, branch string) error
}

var (
	taskIDRe = regexp.MustCompile(`^[Tt]-[0-9]+$`)
	slugRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// ParsePRURL extracts "owner/name" and the PR number from a URL such as
// https://forge.example.com/owner/name/pulls/12 (GitHub style /pull/12 is accepted too).
func ParsePRURL(prURL string) (string, int64, error) {
	u, err := url.Parse(strings.TrimSpace(prURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", 0, fmt.Errorf("%w: %q", ErrInvalidPRURL, prURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || (parts[2] != "pulls" && parts[2] != "pull") ||
		!repoRe.MatchString(parts[0]) || !repoRe.MatchString(parts[1]) {
		return "", 0, fmt.Errorf("%w: %q", ErrInvalidPRURL, prURL)
	}
	if len(parts) > 4 && parts[4] != "files" && parts[4] != "commits" {
		return "", 0, fmt.Errorf("%w: %q", ErrInvalidPRURL, prURL)
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("%w: %q", ErrInvalidPRURL, prURL)
	}
	return parts[0] + "/" + parts[1], n, nil
}

// VerifyWebhook checks an HMAC-SHA256 hex signature of body. An optional "sha256=" prefix is accepted.
// An empty secret or signature never verifies.
func VerifyWebhook(secret string, body []byte, signatureHeader string) bool {
	sig := strings.TrimPrefix(strings.TrimSpace(signatureHeader), "sha256=")
	if secret == "" || sig == "" {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ValidateBranch enforces the branch convention sub/<task-id>-<slug>, e.g. sub/t-3-add-login.
func ValidateBranch(taskID, slug, branch string) error {
	if !taskIDRe.MatchString(taskID) {
		return fmt.Errorf("%w: bad task id %q", ErrInvalidBranch, taskID)
	}
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("%w: bad slug %q", ErrInvalidBranch, slug)
	}
	want := "sub/" + strings.ToLower(taskID) + "-" + slug
	if branch != want {
		return fmt.Errorf("%w: got %q, want %q", ErrInvalidBranch, branch, want)
	}
	return nil
}

// PullRequestEvent is a parsed pull_request webhook.
type PullRequestEvent struct {
	Action     string // opened, closed, reopened, synchronized, ...
	Repo       string
	Number     int64
	Title      string
	State      string
	Merged     bool
	HeadBranch string
	BaseBranch string
	URL        string
	Sender     string
}

// IsMerged reports whether the event means the PR was merged.
func (e PullRequestEvent) IsMerged() bool { return e.Action == "closed" && e.Merged }
