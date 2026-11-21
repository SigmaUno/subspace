package hillclimb

import (
	"net/url"
	"strings"
)

func (r *GitRunner) BranchURL(repo, branch string) string {
	u, err := url.Parse(r.Repos[repo])
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if path == "" {
		return ""
	}
	tree := "/src/branch/"
	if u.Hostname() == "github.com" {
		tree = "/tree/"
	}
	out := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/" + path + tree + branch}
	return out.String()
}
