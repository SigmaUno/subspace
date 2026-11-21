package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Style string

const (
	StyleGitHub  Style = "github"
	StyleForgejo Style = "forgejo"
)

type Commenter interface {
	CreateComment(ctx context.Context, repo string, number int64, body string) (int64, error)
	UpdateComment(ctx context.Context, repo string, id int64, body string) error
}

type CommenterConfig struct {
	Style      Style
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

type restCommenter struct {
	style Style
	api   string
	token string
	http  *http.Client
}

func NewCommenter(cfg CommenterConfig) Commenter {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &restCommenter{style: cfg.Style, api: strings.TrimRight(cfg.APIURL, "/"), token: cfg.Token, http: hc}
}

func (c *restCommenter) CreateComment(ctx context.Context, repo string, number int64, body string) (int64, error) {
	owner, name, ok := splitRepo(repo)
	if !ok || number <= 0 {
		return 0, fmt.Errorf("githost: invalid repo %q or number %d", repo, number)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.api, url.PathEscape(owner), url.PathEscape(name), number)
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, endpoint, body, &out); err != nil {
		return 0, err
	}
	if out.ID <= 0 {
		return 0, errors.New("githost: host returned no comment id")
	}
	return out.ID, nil
}

func (c *restCommenter) UpdateComment(ctx context.Context, repo string, id int64, body string) error {
	owner, name, ok := splitRepo(repo)
	if !ok || id <= 0 {
		return fmt.Errorf("githost: invalid repo %q or comment id %d", repo, id)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d", c.api, url.PathEscape(owner), url.PathEscape(name), id)
	return c.do(ctx, http.MethodPatch, endpoint, body, nil)
}

func (c *restCommenter) do(ctx context.Context, method, endpoint, body string, out any) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.style == StyleGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "subspace")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if c.token != "" {
		if c.style == StyleGitHub {
			req.Header.Set("Authorization", "Bearer "+c.token)
		} else {
			req.Header.Set("Authorization", "token "+c.token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("githost: %s comment: %w", strings.ToLower(method), err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("githost: read response: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return statusError(resp, time.Now())
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("githost: decode response: %w", err)
	}
	return nil
}

func statusError(resp *http.Response, now time.Time) error {
	e := NewStatusError(resp)
	if !e.Limited {
		return e
	}
	if e.RetryAfter <= 0 {
		if !e.ResetAt.IsZero() {
			e.RetryAfter = max(e.ResetAt.Sub(now), time.Second)
		} else {
			e.RetryAfter = time.Second
		}
	}
	return e
}

func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	ok = ok && repoRe.MatchString(owner) && repoRe.MatchString(name) && owner != "." && owner != ".." && name != "." && name != ".."
	return owner, name, ok
}
