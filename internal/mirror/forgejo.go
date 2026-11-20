package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	baseLabel   = "subspace"
	statusScope = "status/"
	maxPages    = 20
	pageSize    = 50
	maxErrBody  = 512
)

var statusColors = map[string]string{
	"backlog":           "#cccccc",
	"ready":             "#0075ca",
	"assigned":          "#5319e7",
	"in_progress":       "#fbca04",
	"blocked":           "#d93f0b",
	"submitted":         "#1d76db",
	"in_review":         "#0e8a16",
	"changes_requested": "#e99695",
	"approved":          "#2cbe4e",
	"done":              "#6f42c1",
	"needs_owner":       "#b60205",
	"cancelled":         "#7f8c8d",
}

// ForgejoConfig configures a ForgejoMirror.
type ForgejoConfig struct {
	BaseURL string // e.g. https://git.example.com
	Token   string // API token with issue write access
	Owner   string // repository owner
	Repo    string // repository name
	Client  *http.Client
}

// ForgejoMirror mirrors tasks as issues of one Forgejo repository.
type ForgejoMirror struct {
	base   string
	token  string
	owner  string
	repo   string
	client *http.Client

	mu     sync.Mutex // serializes writes so concurrent upserts cannot duplicate
	labels map[string]int64
}

var _ IssueMirror = (*ForgejoMirror)(nil)

// NewForgejo returns a Forgejo issues mirror.
func NewForgejo(cfg ForgejoConfig) (*ForgejoMirror, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("mirror: invalid forgejo base url")
	}
	if cfg.Token == "" || cfg.Owner == "" || cfg.Repo == "" {
		return nil, errors.New("mirror: forgejo token, owner and repo are required")
	}
	c := cfg.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return &ForgejoMirror{
		base:   strings.TrimRight(cfg.BaseURL, "/"),
		token:  cfg.Token,
		owner:  cfg.Owner,
		repo:   cfg.Repo,
		client: c,
	}, nil
}

// Marker returns the hidden marker embedded in the issue body of a task.
func Marker(taskID string) string {
	return "<!-- subspace:task:" + taskID + " -->"
}

// RenderBody renders the markdown issue body for a task.
func RenderBody(t TaskView) string {
	var b strings.Builder
	b.WriteString(Marker(t.ID))
	b.WriteString("\n> **Managed by Subspace, read-only.** Edits made here are overwritten; change the task in Subspace.\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Task | `%s` |\n| Status | `%s` |\n", t.ID, t.Status)
	if t.BriefTitle != "" {
		fmt.Fprintf(&b, "| Brief | %s |\n", escapeCell(t.BriefTitle))
	}
	if t.Assignee != "" {
		fmt.Fprintf(&b, "| Assignee | `%s` |\n", t.Assignee)
	}
	if t.PRURL != "" {
		fmt.Fprintf(&b, "| Pull request | %s |\n", t.PRURL)
	}
	if len(t.Acceptance) > 0 {
		b.WriteString("\n### Acceptance criteria\n\n")
		for _, a := range t.Acceptance {
			fmt.Fprintf(&b, "- [ ] %s\n", strings.ReplaceAll(strings.TrimSpace(a), "\n", " "))
		}
	}
	return b.String()
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

type issueLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type issue struct {
	Number  int64        `json:"number"`
	HTMLURL string       `json:"html_url"`
	Body    string       `json:"body"`
	State   string       `json:"state"`
	Labels  []issueLabel `json:"labels"`
}

func isClosedStatus(s string) bool { return s == "done" || s == "cancelled" }

// Upsert implements IssueMirror.
func (m *ForgejoMirror) Upsert(ctx context.Context, t TaskView) (string, error) {
	if t.ID == "" || t.Title == "" {
		return "", errors.New("mirror: task id and title are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	statusID, err := m.labelID(ctx, statusScope+t.Status, statusColors[t.Status])
	if err != nil {
		return "", err
	}
	baseID, err := m.labelID(ctx, baseLabel, "#4c1d95")
	if err != nil {
		return "", err
	}
	state := "open"
	if isClosedStatus(t.Status) {
		state = "closed"
	}

	existing, err := m.find(ctx, t.ID)
	if err != nil {
		return "", err
	}
	if existing == nil {
		var created issue
		err := m.do(ctx, http.MethodPost, m.repoPath("/issues"), map[string]any{
			"title":  t.Title,
			"body":   RenderBody(t),
			"labels": []int64{baseID, statusID},
		}, &created)
		if err != nil {
			return "", err
		}
		if state == "closed" {
			if err := m.setState(ctx, created.Number, state); err != nil {
				return "", err
			}
		}
		return created.HTMLURL, nil
	}

	var updated issue
	err = m.do(ctx, http.MethodPatch, m.repoPath("/issues/"+strconv.FormatInt(existing.Number, 10)), map[string]any{
		"title": t.Title,
		"body":  RenderBody(t),
		"state": state,
	}, &updated)
	if err != nil {
		return "", err
	}
	ids := []int64{baseID, statusID}
	for _, l := range existing.Labels {
		if l.Name != baseLabel && !strings.HasPrefix(l.Name, statusScope) {
			ids = append(ids, l.ID)
		}
	}
	err = m.do(ctx, http.MethodPut, m.repoPath("/issues/"+strconv.FormatInt(existing.Number, 10)+"/labels"),
		map[string]any{"labels": ids}, nil)
	if err != nil {
		return "", err
	}
	if updated.HTMLURL != "" {
		return updated.HTMLURL, nil
	}
	return existing.HTMLURL, nil
}

// Close implements IssueMirror.
func (m *ForgejoMirror) Close(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("mirror: task id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, err := m.find(ctx, taskID)
	if err != nil || existing == nil {
		return err
	}
	if existing.State == "closed" {
		return nil
	}
	return m.setState(ctx, existing.Number, "closed")
}

func (m *ForgejoMirror) setState(ctx context.Context, number int64, state string) error {
	return m.do(ctx, http.MethodPatch, m.repoPath("/issues/"+strconv.FormatInt(number, 10)),
		map[string]any{"state": state}, nil)
}

// find searches issues (any state) for the hidden marker of the task.
// The server-side search is only a pre-filter; the exact marker is checked here.
func (m *ForgejoMirror) find(ctx context.Context, taskID string) (*issue, error) {
	marker := Marker(taskID)
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("type", "issues")
		q.Set("state", "all")
		q.Set("q", "subspace:task:"+taskID)
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		var found []issue
		if err := m.do(ctx, http.MethodGet, m.repoPath("/issues")+"?"+q.Encode(), nil, &found); err != nil {
			return nil, err
		}
		for i := range found {
			if strings.Contains(found[i].Body, marker) {
				return &found[i], nil
			}
		}
		if len(found) < pageSize {
			return nil, nil
		}
	}
	return nil, nil
}

// labelID returns the id of a repository label, creating it if missing.
func (m *ForgejoMirror) labelID(ctx context.Context, name, color string) (int64, error) {
	if m.labels == nil {
		m.labels = map[string]int64{}
		for page := 1; page <= maxPages; page++ {
			var ls []issueLabel
			path := m.repoPath("/labels") + "?limit=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
			if err := m.do(ctx, http.MethodGet, path, nil, &ls); err != nil {
				m.labels = nil
				return 0, err
			}
			for _, l := range ls {
				m.labels[l.Name] = l.ID
			}
			if len(ls) < pageSize {
				break
			}
		}
	}
	if id, ok := m.labels[name]; ok {
		return id, nil
	}
	if color == "" {
		color = "#cccccc"
	}
	var l issueLabel
	if err := m.do(ctx, http.MethodPost, m.repoPath("/labels"), map[string]any{"name": name, "color": color}, &l); err != nil {
		return 0, err
	}
	m.labels[name] = l.ID
	return l.ID, nil
}

func (m *ForgejoMirror) repoPath(suffix string) string {
	return "/api/v1/repos/" + url.PathEscape(m.owner) + "/" + url.PathEscape(m.repo) + suffix
}

func (m *ForgejoMirror) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+m.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mirror: forgejo %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return fmt.Errorf("mirror: forgejo %s %s: status %d: %s", method, req.URL.Path, resp.StatusCode,
			strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("mirror: forgejo %s %s: decode: %w", method, req.URL.Path, err)
	}
	return nil
}
