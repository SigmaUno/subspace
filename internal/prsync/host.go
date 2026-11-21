package prsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/scope"
)

const (
	globalKey = "global"
	pageSize  = 50

	maxCommitPages = 20
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

	authors []string
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
	body, err := netguard.ReadBody(resp.Body, 8<<20)
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
	diff        *scope.Diff
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
		Ref string `json:"ref"`
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
	SHA    string `json:"sha"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Commit struct {
		Author struct {
			Email string `json:"email"`
		} `json:"author"`
	} `json:"commit"`
}

type apiFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
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

func (f *fetcher) fetch(ctx context.Context, h *host, repo string, number int64, prev *Status) (pull, error) {
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

	if out.headSHA == "" {
		return out, nil
	}
	if prev != nil && prev.Diff != nil && prev.Diff.HeadSHA == out.headSHA {
		d := *prev.Diff
		d.HeadRef = p.Head.Ref
		out.diff = &d
		return out, nil
	}
	d := scope.Diff{HeadSHA: out.headSHA, HeadRef: p.Head.Ref}
	if err := f.files(ctx, h, prURL, page, &d); err != nil {
		return pull{}, err
	}
	commits, complete, err := f.commits(ctx, h, prURL, page)
	if err != nil {
		return pull{}, err
	}
	d.Authors = authors(commits)
	out.diff = &d
	if prev != nil && prev.HeadSHA != "" && prev.HeadSHA != out.headSHA && complete {
		out.forcePushed = !slices.ContainsFunc(commits, func(c apiCommit) bool { return c.SHA == prev.HeadSHA })
	}
	return out, nil
}

func pageURL(base, page string, n int) string { return base + page + "&page=" + strconv.Itoa(n) }

func (f *fetcher) files(ctx context.Context, h *host, prURL, page string, d *scope.Diff) error {
	seen := map[string]bool{}
	for n := 1; ; n++ {
		var batch []apiFile
		if err := f.get(ctx, h, pageURL(prURL+"/files", page, n), &batch); err != nil {
			return err
		}
		fresh := false
		for _, af := range batch {
			if af.Filename == "" || seen[af.Filename] {
				continue
			}
			fresh = true
			if len(d.Files) == scope.MaxFiles {
				d.Truncated = true
				return nil
			}
			seen[af.Filename] = true
			d.Files = append(d.Files, af.Filename)
			d.Additions += af.Additions
			d.Deletions += af.Deletions
			if af.PreviousFilename != "" && !seen[af.PreviousFilename] && len(d.Files) < scope.MaxFiles {
				seen[af.PreviousFilename] = true
				d.Files = append(d.Files, af.PreviousFilename)
			}
		}
		if len(batch) < pageSize || !fresh {
			return nil
		}
	}
}

func (f *fetcher) commits(ctx context.Context, h *host, prURL, page string) ([]apiCommit, bool, error) {
	var all []apiCommit
	for n := 1; n <= maxCommitPages; n++ {
		var batch []apiCommit
		if err := f.get(ctx, h, pageURL(prURL+"/commits", page, n), &batch); err != nil {
			return nil, false, err
		}
		all = append(all, batch...)
		if len(batch) < pageSize {
			return all, true, nil
		}
	}
	return all, false, nil
}

func authors(commits []apiCommit) []string {
	var out []string
	for _, c := range commits {
		id := c.Author.Login
		if id == "" {
			id = c.Commit.Author.Email
		}
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
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
