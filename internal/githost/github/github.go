package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultAPIURL = "https://api.github.com"

// Config configures a Client.
type Config struct {
	APIURL        string       // default https://api.github.com
	Token         string       // optional bearer token for the REST API
	WebhookSecret string       // shared secret for X-Hub-Signature-256
	HTTPClient    *http.Client // default: 15s timeout
}

// Client is the GitHub implementation of Host.
type Client struct {
	apiURL string
	token  string
	secret []byte
	http   *http.Client
}

var _ Host = (*Client)(nil)

func New(cfg Config) *Client {
	api := strings.TrimRight(cfg.APIURL, "/")
	if api == "" {
		api = defaultAPIURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{apiURL: api, token: cfg.Token, secret: []byte(cfg.WebhookSecret), http: hc}
}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParsePRURL parses https://github.com/<owner>/<repo>/pull/<n>.
func (c *Client) ParsePRURL(raw string) (PRRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" || u.User != nil {
		return PRRef{}, fmt.Errorf("%w: %q", ErrInvalidPRURL, raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// allow trailing sub-pages such as /files or /commits
	if len(parts) < 4 || parts[2] != "pull" {
		return PRRef{}, fmt.Errorf("%w: %q", ErrInvalidPRURL, raw)
	}
	if len(parts) > 4 && parts[4] != "files" && parts[4] != "commits" && parts[4] != "checks" {
		return PRRef{}, fmt.Errorf("%w: %q", ErrInvalidPRURL, raw)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 || !repoPart.MatchString(parts[0]) || !repoPart.MatchString(parts[1]) ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return PRRef{}, fmt.Errorf("%w: %q", ErrInvalidPRURL, raw)
	}
	return PRRef{Repo: parts[0] + "/" + parts[1], Number: n}, nil
}

var branchRe = regexp.MustCompile(`^sub/t-[0-9]+-[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateBranch checks the `sub/<task-id>-<slug>` convention (e.g. sub/t-3-add-login).
func (c *Client) ValidateBranch(branch string) error {
	if len(branch) > 100 || !branchRe.MatchString(branch) {
		return fmt.Errorf("%w: %q must match sub/<task-id>-<slug>", ErrInvalidBranch, branch)
	}
	return nil
}

func headerGet(h map[string][]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// VerifyWebhook checks the X-Hub-Signature-256 header (constant-time).
func (c *Client) VerifyWebhook(header map[string][]string, body []byte) error {
	if len(c.secret) == 0 {
		return fmt.Errorf("%w: no webhook secret configured", ErrInvalidSignature)
	}
	sig := headerGet(header, "X-Hub-Signature-256")
	hexSig, ok := strings.CutPrefix(sig, "sha256=")
	if !ok {
		return ErrInvalidSignature
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, c.secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

type ghPR struct {
	HTMLURL   string     `json:"html_url"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	Merged    bool       `json:"merged"`
	MergedAt  *time.Time `json:"merged_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	MergedBy *struct {
		Login string `json:"login"`
	} `json:"merged_by"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p *ghPR) toPR(repo string) *PR {
	pr := &PR{
		Ref:       PRRef{Repo: repo, Number: p.Number},
		URL:       p.HTMLURL,
		Title:     p.Title,
		Body:      p.Body,
		State:     p.State,
		Merged:    p.Merged || p.MergedAt != nil,
		Draft:     p.Draft,
		Branch:    p.Head.Ref,
		BaseRef:   p.Base.Ref,
		Author:    p.User.Login,
		MergedAt:  p.MergedAt,
		UpdatedAt: p.UpdatedAt,
	}
	if pr.Merged {
		pr.State = "merged"
	}
	if p.MergedBy != nil {
		pr.MergedBy = p.MergedBy.Login
	}
	return pr
}

// GetPR fetches a pull request via GET /repos/{owner}/{repo}/pulls/{n}.
func (c *Client) GetPR(ctx context.Context, ref PRRef) (*PR, error) {
	body, err := c.getPull(ctx, ref, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var p ghPR
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("github: decode pull: %w", err)
	}
	return p.toPR(ref.Repo), nil
}

// GetPRDiff fetches the unified diff of a pull request.
func (c *Client) GetPRDiff(ctx context.Context, ref PRRef) (string, error) {
	body, err := c.getPull(ctx, ref, "application/vnd.github.diff")
	return string(body), err
}

func (c *Client) getPull(ctx context.Context, ref PRRef, accept string) ([]byte, error) {
	owner, name, ok := strings.Cut(ref.Repo, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) || ref.Number <= 0 {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidPRURL, ref)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiURL, owner, name, ref.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "subspace")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s#%d", ErrNotFound, ref.Repo, ref.Number)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("github: GET pull %s#%d: status %d", ref.Repo, ref.Number, resp.StatusCode)
	}
	return body, nil
}

type ghWebhook struct {
	Action      string `json:"action"`
	PullRequest *ghPR  `json:"pull_request"`
	Repository  struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ParseWebhook turns a pull_request closed+merged delivery into an Event.
// It does not verify the signature; call VerifyWebhook first. Other
// deliveries return ErrIgnoredEvent.
func (c *Client) ParseWebhook(header map[string][]string, body []byte) (*Event, error) {
	if ev := headerGet(header, "X-GitHub-Event"); ev != "pull_request" {
		return nil, fmt.Errorf("%w: event %q", ErrIgnoredEvent, ev)
	}
	var w ghWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("github: decode webhook: %w", err)
	}
	if w.PullRequest == nil {
		return nil, fmt.Errorf("github: webhook without pull_request")
	}
	if w.Action != "closed" || (!w.PullRequest.Merged && w.PullRequest.MergedAt == nil) {
		return nil, fmt.Errorf("%w: pull_request %q", ErrIgnoredEvent, w.Action)
	}
	pr := w.PullRequest.toPR(w.Repository.FullName)
	return &Event{
		Kind:     EventPRMerged,
		Repo:     pr.Ref.Repo,
		PR:       pr.Ref,
		URL:      pr.URL,
		Branch:   pr.Branch,
		MergedBy: pr.MergedBy,
		MergedAt: pr.MergedAt,
	}, nil
}
