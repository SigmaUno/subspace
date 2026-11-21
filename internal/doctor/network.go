package doctor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/prsync"
)

const (
	probeTimeout   = 5 * time.Second
	skewWarn       = 10 * time.Second
	skewError      = 2 * time.Minute
	telegramHost   = "api.telegram.org"
	maxProbeBytes  = 1 << 20
	maxVersionRune = 80
)

func fetch(ctx context.Context, e Env, method, target, authorization string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.HTTP.Do(req) //nolint:gosec // operator-configured URL
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelBody{resp.Body, cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBytes))
	_ = resp.Body.Close()
}

func checkTimeSkew(ctx context.Context, e Env) []Result {
	const id = "time.skew"
	u, err := url.Parse(getenv(e, "SUBSPACE_PUBLIC_URL", "http://localhost:8080"))
	if err != nil || u.Host == "" || isLocal(u.Hostname()) {
		return result(Info, id, "public URL is local, clock skew not checked", "")
	}
	resp, err := fetch(ctx, e, http.MethodHead, u.Scheme+"://"+u.Host+"/healthz", "")
	if err != nil {
		return result(Info, id, "public URL not reachable from here, clock skew not checked: %v", "", err)
	}
	closeBody(resp)
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return result(Info, id, "public URL sent no usable Date header, clock skew not checked", "")
	}
	skew := e.Now().Sub(date).Abs().Round(time.Second)
	const fix = "enable NTP (timedatectl set-ntp true) on this host and on the host serving the public URL"
	switch {
	case skew >= skewError:
		return result(Error, id, "clock differs by %s from the public URL", fix, skew)
	case skew >= skewWarn:
		return result(Warn, id, "clock differs by %s from the public URL", fix, skew)
	}
	return result(OK, id, "clock within %s of the public URL", "", skew)
}

func checkHC(ctx context.Context, e Env) []Result {
	const id = "integration.hc"
	if getenv(e, "SUBSPACE_SPAWNER", "noop") != "hc" {
		return result(Info, id, "hc spawner not selected", "")
	}
	if host := e.Getenv("SUBSPACE_HC_HOST"); host != "" {
		if _, err := e.LookPath("ssh"); err != nil {
			return result(Error, id, "hc runs over ssh but ssh is not on PATH", "install an OpenSSH client")
		}
		return result(Info, id, "hc runs on a remote host over ssh, not probed", "")
	}
	bin := getenv(e, "SUBSPACE_HC_BIN", "hc")
	path, err := e.LookPath(bin)
	if err != nil {
		return result(Error, id, "hc binary %q not found on PATH", "install hc or set SUBSPACE_HC_BIN to its path", bin)
	}
	tctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := e.Output(tctx, path, "--version")
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if r := []rune(version); len(r) > maxVersionRune {
		version = string(r[:maxVersionRune])
	}
	if err != nil {
		return result(Warn, id, "%s --version failed: %v", "check that the hc binary runs", bin, err)
	}
	return result(OK, id, "hc found at %s: %s", "", path, version)
}

func checkForgejoMirror(ctx context.Context, e Env) []Result {
	const id = "integration.forgejo_mirror"
	base := strings.TrimRight(e.Getenv("SUBSPACE_MIRROR_FORGEJO_URL"), "/")
	token := e.Getenv("SUBSPACE_MIRROR_FORGEJO_TOKEN")
	rawMap := e.Getenv("SUBSPACE_MIRROR_FORGEJO_REPO_MAP")
	if base == "" && token == "" && rawMap == "" {
		return result(Info, id, "Forgejo issue mirror not configured", "")
	}
	if base == "" || token == "" || rawMap == "" {
		return result(Error, id, "SUBSPACE_MIRROR_FORGEJO_URL, _TOKEN and _REPO_MAP must all be set", "set all three or none")
	}
	var repos []string
	for _, entry := range strings.Split(rawMap, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		_, dst, ok := strings.Cut(entry, "=")
		if owner, repo, ok2 := strings.Cut(strings.TrimSpace(dst), "/"); !ok || !ok2 || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return result(Error, id, "SUBSPACE_MIRROR_FORGEJO_REPO_MAP entry %q must look like task-repo=owner/repo", "", entry)
		}
		repos = append(repos, strings.TrimSpace(dst))
	}
	if r := probeAuth(ctx, e, id, base+"/api/v1/user", "token "+token); r != nil {
		return r
	}
	var out []Result
	for _, repo := range repos {
		out = append(out, forgejoRepoAccess(ctx, e, id, base, token, repo)...)
	}
	if len(out) == 0 {
		return result(OK, id, "token accepted by %s", "", base)
	}
	return out
}

func probeAuth(ctx context.Context, e Env, id, target, authorization string) []Result {
	resp, err := fetch(ctx, e, http.MethodGet, target, authorization)
	if err != nil {
		return result(Warn, id, "%s not reachable: %v", "check the URL and the network from this host", hostOf(target), err)
	}
	defer closeBody(resp)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return result(Error, id, "token rejected by %s (HTTP 401)", "create a new token and update the configuration", hostOf(target))
	case resp.StatusCode >= 500:
		return result(Warn, id, "%s answered HTTP %d", "", hostOf(target), resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return result(Warn, id, "%s answered HTTP %d for the token check", "check the token's scopes", hostOf(target), resp.StatusCode)
	}
	return nil
}

func hostOf(target string) string {
	if u, err := url.Parse(target); err == nil {
		return u.Host
	}
	return "host"
}

func forgejoRepoAccess(ctx context.Context, e Env, id, base, token, repo string) []Result {
	resp, err := fetch(ctx, e, http.MethodGet, base+"/api/v1/repos/"+repo, "token "+token)
	if err != nil {
		return result(Warn, id, "cannot reach %s: %v", "", hostOf(base), err)
	}
	defer closeBody(resp)
	if resp.StatusCode == http.StatusNotFound {
		return result(Error, id, "repository %s not found or not visible to the token", "check SUBSPACE_MIRROR_FORGEJO_REPO_MAP and the token's repository scope", repo)
	}
	if resp.StatusCode != http.StatusOK {
		return result(Warn, id, "repository %s answered HTTP %d", "check the token's repository scope", repo, resp.StatusCode)
	}
	var body struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxProbeBytes)).Decode(&body); err == nil && !body.Permissions.Push {
		return result(Warn, id, "token cannot write to %s, mirrored issues cannot be created", "grant the token write access to issues of that repository", repo)
	}
	return nil
}

func checkPRSyncHosts(ctx context.Context, e Env) []Result {
	const id = "integration.prsync"
	path := e.Getenv("SUBSPACE_PRSYNC_CONFIG")
	if path == "" {
		return result(Info, id, "PR sync not configured", "")
	}
	cfg, err := prsync.LoadConfig(path, "")
	if err != nil {
		return result(Error, id, "%v", "fix the JSON file named by SUBSPACE_PRSYNC_CONFIG", err)
	}
	names := make([]string, 0, len(cfg.Hosts))
	for name := range cfg.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Result
	for _, name := range names {
		out = append(out, prsyncHost(ctx, e, id+"."+name, cfg.Hosts[name])...)
	}
	return out
}

func prsyncHost(ctx context.Context, e Env, id string, h prsync.HostConfig) []Result {
	token := h.Token
	if h.TokenEnv != "" {
		token = e.Getenv(h.TokenEnv)
	}
	if token == "" {
		return result(Warn, id, "no token configured for %s, requests are unauthenticated and heavily rate limited",
			"set the host's token_env variable", h.APIURL)
	}
	if h.Kind == prsync.KindForgejo {
		if r := probeAuth(ctx, e, id, h.APIURL+"/user", "token "+token); r != nil {
			return r
		}
		resp, err := fetch(ctx, e, http.MethodGet, h.APIURL+"/user/repos?limit=1", "token "+token)
		if err != nil {
			return result(Warn, id, "%s not reachable: %v", "", hostOf(h.APIURL), err)
		}
		defer closeBody(resp)
		if resp.StatusCode == http.StatusForbidden {
			return result(Warn, id, "token for %s lacks repository read scope", "grant read:repository to the token", hostOf(h.APIURL))
		}
		return result(OK, id, "token accepted by %s", "", hostOf(h.APIURL))
	}
	resp, err := fetch(ctx, e, http.MethodGet, h.APIURL+"/user", "Bearer "+token)
	if err != nil {
		return result(Warn, id, "%s not reachable: %v", "check the URL and the network from this host", hostOf(h.APIURL), err)
	}
	defer closeBody(resp)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return result(Error, id, "token rejected by %s (HTTP 401)", "create a new token and update the configuration", hostOf(h.APIURL))
	case resp.StatusCode != http.StatusOK:
		return result(Warn, id, "%s answered HTTP %d for the token check", "check the token's permissions", hostOf(h.APIURL), resp.StatusCode)
	}
	scopes := strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
	if scopes == "" {
		return result(OK, id, "token accepted by %s, fine-grained tokens do not report scopes", "", hostOf(h.APIURL))
	}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s == "repo" || s == "public_repo" {
			return result(OK, id, "token accepted by %s with scope %s", "", hostOf(h.APIURL), s)
		}
	}
	return result(Warn, id, "token for %s has scopes %q but not repo", "grant the repo (or public_repo) scope", hostOf(h.APIURL), scopes)
}

func checkNotifyDNS(ctx context.Context, e Env) []Result {
	const id = "integration.notify_dns"
	var hosts []string
	if raw := strings.TrimSpace(e.Getenv("SUBSPACE_NOTIFY_SMTP_URL")); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
		}
	}
	if e.Getenv("SUBSPACE_NOTIFY_TELEGRAM_BOT_TOKEN") != "" {
		hosts = append(hosts, telegramHost)
	}
	if len(hosts) == 0 {
		return result(Info, id, "no SMTP or Telegram sink configured", "")
	}
	var out []Result
	for _, host := range hosts {
		if _, err := netip.ParseAddr(host); err == nil {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, probeTimeout)
		addrs, err := e.LookupHost(lctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			out = append(out, result(Warn, id, "cannot resolve %s, notifications through it cannot be delivered",
				"check DNS and outbound network access from this host", host)...)
		}
	}
	if len(out) == 0 {
		return result(OK, id, "notification hosts resolve", "")
	}
	return out
}
