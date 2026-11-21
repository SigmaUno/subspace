package repos

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/redact"
)

type Health struct {
	Repo                string    `json:"repo"`
	OK                  bool      `json:"ok"`
	Reachable           bool      `json:"reachable"`
	TokenConfigured     bool      `json:"token_configured"`
	TokenHint           string    `json:"token_hint"`
	DefaultBranch       string    `json:"default_branch"`
	DefaultBranchExists bool      `json:"default_branch_exists"`
	HostDefaultBranch   string    `json:"host_default_branch"`
	Error               string    `json:"error"`
	CheckedAt           time.Time `json:"checked_at"`
}

func Check(ctx context.Context, r Repo, hc *http.Client, now time.Time) Health {
	token := r.Token()
	h := Health{Repo: r.Name, TokenConfigured: token != "", DefaultBranch: r.DefaultBranch, CheckedAt: now.UTC()}
	style := githost.StyleForgejo
	if r.Host == HostGitHub {
		style = githost.StyleGitHub
	}
	res := githost.NewProber(githost.ProbeConfig{Style: style, APIURL: r.APIURL(), Token: token, HTTPClient: hc}).Probe(ctx, r.Name, r.DefaultBranch)
	h.Reachable, h.TokenHint, h.HostDefaultBranch, h.DefaultBranchExists = res.Reachable, res.TokenHint, res.HostDefaultBranch, res.DefaultBranchExists
	h.Error = res.Error
	if r.TokenEnv != "" && token == "" {
		h.TokenHint = "environment variable " + r.TokenEnv + " is not set on the server; " + h.TokenHint
	}
	if token != "" {
		h.Error = redact.String(strings.ReplaceAll(h.Error, token, "***"))
		h.TokenHint = strings.ReplaceAll(h.TokenHint, token, "***")
	}
	h.OK = h.Reachable && h.Error == "" && h.DefaultBranchExists
	return h
}
