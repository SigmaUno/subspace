package prsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/ratelimit"
)

const (
	globalKey = "global"
	pageSize  = 50
)

var errLimited = errors.New("prsync: local rate limit reached")

type hostError struct {
	status     int
	throttled  bool
	retryAfter time.Duration
}

func (e *hostError) Error() string {
	if e.throttled {
		return fmt.Sprintf("host rate limit (status %d)", e.status)
	}
	return fmt.Sprintf("unexpected status %d", e.status)
}

type cached struct {
	etag string
	body []byte
	used time.Time
}

type fetcher struct {
	http   *http.Client
	clock  ratelimit.Clock
	limits *ratelimit.Limiter
	cache  map[string]*cached
}

type host struct {
	name  string
	kind  string
	web   string
	api   string
	token string
}

func hostKey(name string) string { return "host:" + name }

func (f *fetcher) get(ctx context.Context, h *host, endpoint string, v any) error {
	if !f.limits.Allow(hostKey(h.name)) || !f.limits.Allow(globalKey) {
		return errLimited
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if h.kind == KindGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "subspace")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if h.token != "" {
		if h.kind == KindGitHub {
			req.Header.Set("Authorization", "Bearer "+h.token)
		} else {
			req.Header.Set("Authorization", "token "+h.token)
		}
	}
	c := f.cache[endpoint]
	if c != nil {
		req.Header.Set("If-None-Match", c.etag)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", h.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", h.name, err)
	}
	now := f.clock.Now()
	switch {
	case resp.StatusCode == http.StatusNotModified && c != nil:
		c.used = now
		body = c.body
	case resp.StatusCode == http.StatusOK:
		if etag := resp.Header.Get("ETag"); etag != "" {
			f.cache[endpoint] = &cached{etag: etag, body: body, used: now}
		} else {
			delete(f.cache, endpoint)
		}
	case resp.StatusCode == http.StatusNotFound:
		return githost.ErrNotFound
	default:
		return throttle(resp, now)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: decode response: %w", h.name, err)
	}
	return nil
}

func throttle(resp *http.Response, now time.Time) error {
	e := &hostError{status: resp.StatusCode}
	e.throttled = resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
	if !e.throttled {
		return e
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.retryAfter = time.Duration(s) * time.Second
	} else if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		e.retryAfter = time.Unix(r, 0).Sub(now)
	}
	return e
}

func (f *fetcher) prune(before time.Time) {
	for k, c := range f.cache {
		if c.used.Before(before) {
			delete(f.cache, k)
		}
	}
}

type pull struct {
	state       string
	draft       bool
	mergeable   *bool
	headSHA     string
	checks      string
	reviews     []Review
	decision    string
	forcePushed bool
}

type apiPull struct {
	State     string  `json:"state"`
	Title     string  `json:"title"`
	Merged    bool    `json:"merged"`
	MergedAt  *string `json:"merged_at"`
	Draft     bool    `json:"draft"`
	Mergeable *bool   `json:"mergeable"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type apiReview struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State     string `json:"state"`
	Dismissed bool   `json:"dismissed"`
}

type apiCommit struct {
	SHA string `json:"sha"`
}

type apiStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

type apiCheckRuns struct {
	CheckRuns []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
}

func (f *fetcher) fetch(ctx context.Context, h *host, repo string, number int64, prevSHA string) (pull, error) {
	owner, name, _ := strings.Cut(repo, "/")
	base := fmt.Sprintf("%s/repos/%s/%s", h.api, url.PathEscape(owner), url.PathEscape(name))
	prURL := fmt.Sprintf("%s/pulls/%d", base, number)
	page := "?limit=" + strconv.Itoa(pageSize)
	if h.kind == KindGitHub {
		page = "?per_page=" + strconv.Itoa(pageSize)
	}

	var p apiPull
	if err := f.get(ctx, h, prURL, &p); err != nil {
		return pull{}, err
	}
	out := pull{state: "open", draft: p.Draft, mergeable: p.Mergeable, headSHA: p.Head.SHA}
	switch {
	case p.Merged || p.MergedAt != nil:
		out.state = "merged"
	case p.State == "closed":
		out.state = "closed"
	}
	if h.kind == KindForgejo {
		t := strings.ToUpper(strings.TrimSpace(p.Title))
		out.draft = out.draft || strings.HasPrefix(t, "WIP:") || strings.HasPrefix(t, "[WIP]")
	}

	var reviews []apiReview
	if err := f.get(ctx, h, prURL+"/reviews"+page, &reviews); err != nil {
		return pull{}, err
	}
	out.reviews, out.decision = decide(reviews)

	out.checks = "none"
	if out.headSHA != "" {
		var st apiStatus
		if err := f.get(ctx, h, base+"/commits/"+url.PathEscape(out.headSHA)+"/status", &st); err != nil {
			return pull{}, err
		}
		states := []string{statusState(st)}
		if h.kind == KindGitHub {
			var runs apiCheckRuns
			if err := f.get(ctx, h, base+"/commits/"+url.PathEscape(out.headSHA)+"/check-runs"+page, &runs); err != nil {
				return pull{}, err
			}
			for _, r := range runs.CheckRuns {
				states = append(states, runState(r.Status, r.Conclusion))
			}
		}
		out.checks = combine(states)
	}

	if prevSHA != "" && out.headSHA != "" && prevSHA != out.headSHA {
		var commits []apiCommit
		if err := f.get(ctx, h, prURL+"/commits"+page, &commits); err != nil {
			return pull{}, err
		}
		if len(commits) < pageSize && !slices.ContainsFunc(commits, func(c apiCommit) bool { return c.SHA == prevSHA }) {
			out.forcePushed = true
		}
	}
	return out, nil
}

func decide(raw []apiReview) ([]Review, string) {
	latest := map[string]string{}
	for _, r := range raw {
		user := r.User.Login
		if user == "" {
			continue
		}
		switch strings.ToUpper(r.State) {
		case "APPROVED":
			latest[user] = ReviewApproved
		case "CHANGES_REQUESTED", "REQUEST_CHANGES":
			latest[user] = ReviewChangesRequested
		case "DISMISSED":
			delete(latest, user)
		default:
			continue
		}
		if r.Dismissed {
			delete(latest, user)
		}
	}
	out := make([]Review, 0, len(latest))
	decision := DecisionNone
	for user, state := range latest {
		out = append(out, Review{User: user, State: state})
		if state == ReviewChangesRequested {
			decision = ReviewChangesRequested
		} else if decision == DecisionNone {
			decision = ReviewApproved
		}
	}
	slices.SortFunc(out, func(a, b Review) int { return strings.Compare(a.User, b.User) })
	return out, decision
}

func statusState(s apiStatus) string {
	if s.TotalCount == 0 {
		return ChecksNone
	}
	switch s.State {
	case "success", "warning":
		return ChecksSuccess
	case "failure", "error":
		return ChecksFailure
	case "pending":
		return ChecksPending
	}
	return ChecksNone
}

func runState(status, conclusion string) string {
	if status != "completed" {
		return ChecksPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return ChecksSuccess
	}
	return ChecksFailure
}

func combine(states []string) string {
	for _, want := range []string{ChecksFailure, ChecksPending, ChecksSuccess} {
		if slices.Contains(states, want) {
			return want
		}
	}
	return ChecksNone
}
