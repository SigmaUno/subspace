// Package forgejo implements githost.Host for Forgejo (API v1) using net/http only.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
)

// Client talks to a Forgejo instance with a token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

var _ githost.Host = (*Client)(nil)

// New returns a client for baseURL (e.g. https://forge.example.com). A nil httpClient uses a 15s-timeout default.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

type apiPR struct {
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p apiPR) toPR(repo string) githost.PR {
	return githost.PR{
		Repo: repo, Number: p.Number, Title: p.Title, Body: p.Body, State: p.State, Merged: p.Merged,
		HeadBranch: p.Head.Ref, HeadSHA: p.Head.SHA, BaseBranch: p.Base.Ref, URL: p.HTMLURL,
	}
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && owner != "" && name != "" && !strings.Contains(name, "/") && owner != ".." && name != ".." && owner != "." && name != "."
}

// GetPR fetches a pull request. repo is "owner/name".
func (c *Client) GetPR(ctx context.Context, repo string, number int64) (githost.PR, error) {
	if !validRepo(repo) || number <= 0 {
		return githost.PR{}, fmt.Errorf("forgejo: invalid repo %q or number %d", repo, number)
	}
	owner, name, _ := strings.Cut(repo, "/")
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d", c.baseURL, url.PathEscape(owner), url.PathEscape(name), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githost.PR{}, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return githost.PR{}, fmt.Errorf("forgejo: get pr: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return githost.PR{}, fmt.Errorf("%w: %s#%d", githost.ErrNotFound, repo, number)
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return githost.PR{}, fmt.Errorf("forgejo: get pr %s#%d: unexpected status %d", repo, number, resp.StatusCode)
	}
	var p apiPR
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&p); err != nil {
		return githost.PR{}, fmt.Errorf("forgejo: decode pr: %w", err)
	}
	return p.toPR(repo), nil
}

// GetPRDiff fetches the unified diff of a pull request, at most 8 MiB.
func (c *Client) GetPRDiff(ctx context.Context, repo string, number int64) (string, error) {
	if !validRepo(repo) || number <= 0 {
		return "", fmt.Errorf("forgejo: invalid repo %q or number %d", repo, number)
	}
	owner, name, _ := strings.Cut(repo, "/")
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d.diff", c.baseURL, url.PathEscape(owner), url.PathEscape(name), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("forgejo: get pr diff: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%w: %s#%d", githost.ErrNotFound, repo, number)
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("forgejo: get pr diff %s#%d: unexpected status %d", repo, number, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("forgejo: read pr diff: %w", err)
	}
	return string(b), nil
}

func (c *Client) PostStatus(ctx context.Context, repo, sha string, status githost.CommitStatus) error {
	if !validRepo(repo) {
		return fmt.Errorf("forgejo: invalid repo %q", repo)
	}
	if err := status.Validate(sha); err != nil {
		return err
	}
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	owner, name, _ := strings.Cut(repo, "/")
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/statuses/%s", c.baseURL, url.PathEscape(owner), url.PathEscape(name), sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("forgejo: post status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s@%s", githost.ErrNotFound, repo, sha)
	case resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK:
		return fmt.Errorf("forgejo: post status %s@%s: %w", repo, sha, githost.NewStatusError(resp))
	}
	return nil
}

func (c *Client) ParsePRURL(prURL string) (string, int64, error) { return githost.ParsePRURL(prURL) }

func (c *Client) VerifyWebhook(secret string, body []byte, signatureHeader string) bool {
	return githost.VerifyWebhook(secret, body, signatureHeader)
}

func (c *Client) ValidateBranch(taskID, slug, branch string) error {
	return githost.ValidateBranch(taskID, slug, branch)
}
