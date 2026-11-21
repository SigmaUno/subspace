package githost

import (
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

type ProbeConfig struct {
	Style      Style
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

type ProbeResult struct {
	Reachable           bool
	Status              int
	TokenAccepted       bool
	TokenHint           string
	HostDefaultBranch   string
	DefaultBranchExists bool
	Error               string
}

type Prober struct {
	cfg ProbeConfig
	api string
	hc  *http.Client
}

func NewProber(cfg ProbeConfig) *Prober {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Prober{cfg: cfg, api: strings.TrimRight(cfg.APIURL, "/"), hc: hc}
}

type probedRepo struct {
	DefaultBranch string `json:"default_branch"`
	Permissions   *struct {
		Push  bool `json:"push"`
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func (p *Prober) Probe(ctx context.Context, repo, branch string) ProbeResult {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return ProbeResult{Error: "invalid repository name"}
	}
	base := fmt.Sprintf("%s/repos/%s/%s", p.api, url.PathEscape(owner), url.PathEscape(name))
	status, header, body, err := p.get(ctx, base)
	if err != nil {
		return ProbeResult{Error: "host is not reachable: " + failure(err)}
	}
	res := ProbeResult{Reachable: true, Status: status, TokenHint: p.hint(status, header, body)}
	switch status {
	case http.StatusOK:
		res.TokenAccepted = p.cfg.Token != ""
		var r probedRepo
		if json.Unmarshal(body, &r) == nil {
			res.HostDefaultBranch = r.DefaultBranch
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		res.Error = fmt.Sprintf("host refused access (status %d)", status)
		return res
	case http.StatusNotFound:
		res.Error = "repository not found, or the token cannot see it"
		return res
	default:
		res.Error = fmt.Sprintf("unexpected status %d from host", status)
		return res
	}
	if branch == "" {
		return res
	}
	segments := strings.Split(branch, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	bs, _, _, err := p.get(ctx, base+"/branches/"+strings.Join(segments, "/"))
	switch {
	case err != nil:
		res.Error = "host is not reachable: " + failure(err)
	case bs == http.StatusOK:
		res.DefaultBranchExists = true
	case bs != http.StatusNotFound:
		res.Error = fmt.Sprintf("unexpected status %d while looking up the default branch", bs)
	}
	return res
}

func (p *Prober) hint(status int, header http.Header, body []byte) string {
	if p.cfg.Token == "" {
		return "no token configured: only public repositories are readable and no statuses or comments can be written"
	}
	switch status {
	case http.StatusUnauthorized:
		return "the host rejected the token"
	case http.StatusForbidden, http.StatusNotFound:
		return "the token may lack access to this repository"
	case http.StatusOK:
	default:
		return ""
	}
	var r probedRepo
	_ = json.Unmarshal(body, &r)
	scopes := ""
	if s := strings.TrimSpace(header.Get("X-OAuth-Scopes")); s != "" {
		scopes = " (token scopes: " + s + ")"
	}
	switch {
	case r.Permissions == nil:
		return "token accepted; its write access could not be determined" + scopes
	case r.Permissions.Push:
		return "token has write access" + scopes
	default:
		return "token is read-only; commit statuses, review comments and merges need write access" + scopes
	}
}

func (p *Prober) get(ctx context.Context, endpoint string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	if p.cfg.Style == StyleGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "subspace")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if p.cfg.Token != "" {
		if p.cfg.Style == StyleGitHub {
			req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
		} else {
			req.Header.Set("Authorization", "token "+p.cfg.Token)
		}
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, body, err
}

func failure(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}
