package doctor

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const (
	varOwner        = "SUBSPACE_OWNER_TOKEN"
	varOwnerFile    = "SUBSPACE_OWNER_TOKEN_FILE"
	minTokenLen     = 24
	goodTokenLen    = 32
	minTokenVariety = 8
)

func getenv(e Env, key, def string) string {
	if v := e.Getenv(key); v != "" {
		return v
	}
	return def
}

func ownerTokenFile(e Env) (string, error) {
	path := e.Getenv(varOwnerFile)
	if path == "" {
		return "", errors.New("no token file configured")
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	return strings.TrimSpace(string(b)), err
}

func ownerToken(e Env) (token, source string, err error) {
	if e.Getenv(varOwnerFile) != "" {
		token, err = ownerTokenFile(e)
		return token, varOwnerFile, err
	}
	if tok := e.Getenv(varOwner); tok != "" {
		return tok, varOwner, nil
	}
	return "", "", nil
}

func checkOwnerToken(_ context.Context, e Env) []Result {
	const id = "config.owner_token"
	tok, src, err := ownerToken(e)
	switch {
	case err != nil:
		return result(Error, id, "cannot read %s: %v", "check the path and permissions of the token file", src, err)
	case tok == "":
		return result(Error, id, "no owner token configured, the server refuses to start",
			"set "+varOwnerFile+" (preferred) or "+varOwner+" to at least 24 random characters")
	case len(tok) < minTokenLen:
		return result(Error, id, "owner token from %s has %d characters, the minimum is %d",
			"generate one with: openssl rand -base64 32", src, len(tok), minTokenLen)
	}
	var out []Result
	if len(tok) < goodTokenLen {
		out = append(out, result(Warn, id, "owner token from %s has only %d characters", "use at least 32 random characters", src, len(tok))...)
	}
	if variety(tok) < minTokenVariety {
		out = append(out, result(Warn, id, "owner token from %s uses only %d distinct characters", "generate one with: openssl rand -base64 32", src, variety(tok))...)
	}
	if src == varOwnerFile {
		if fi, err := os.Stat(e.Getenv(varOwnerFile)); err == nil && fi.Mode().Perm()&0o077 != 0 {
			out = append(out, result(Warn, id, "token file is accessible by group or others (mode %04o)",
				"chmod 600 "+e.Getenv(varOwnerFile), fi.Mode().Perm())...)
		}
	}
	if len(out) == 0 {
		return result(OK, id, "owner token from %s, %d characters", "", src, len(tok))
	}
	return out
}

func variety(s string) int {
	seen := map[rune]struct{}{}
	for _, r := range s {
		seen[r] = struct{}{}
	}
	return len(seen)
}

func isLocal(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func checkPublicURL(_ context.Context, e Env) []Result {
	const id = "config.public_url"
	raw := getenv(e, "SUBSPACE_PUBLIC_URL", "http://localhost:8080")
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return result(Error, id, "SUBSPACE_PUBLIC_URL is not an absolute URL", "use a value like https://subspace.example.com")
	case u.Scheme != "http" && u.Scheme != "https":
		return result(Error, id, "SUBSPACE_PUBLIC_URL has scheme %q, want http or https", "use a value like https://subspace.example.com", u.Scheme)
	case u.User != nil:
		return result(Error, id, "SUBSPACE_PUBLIC_URL must not contain credentials", "remove the user info from the URL")
	case u.Scheme == "http" && !isLocal(u.Hostname()):
		return result(Warn, id, "public URL %s uses plain http, tokens travel unencrypted", "terminate TLS in front of Subspace and use https://", u.Host)
	}
	return result(OK, id, "public URL %s://%s", "", u.Scheme, u.Host)
}

func checkListenAddr(_ context.Context, e Env) []Result {
	const id = "config.listen_addr"
	addr := getenv(e, "SUBSPACE_ADDR", ":8080")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return result(Error, id, "SUBSPACE_ADDR %q is not host:port", "use a value like 127.0.0.1:8080 or :8080", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return result(Error, id, "SUBSPACE_ADDR port %q is not a number between 1 and 65535", "", port)
	}
	a, perr := netip.ParseAddr(host)
	switch {
	case host == "" || (perr == nil && a.IsUnspecified()):
		if strings.TrimSpace(e.Getenv("SUBSPACE_OWNER_ALLOW_CIDRS")) != "" {
			return result(Info, id, "listens on all interfaces (%s), the owner API is restricted by SUBSPACE_OWNER_ALLOW_CIDRS", "", addr)
		}
		return result(Warn, id, "listens on all interfaces (%s) and SUBSPACE_OWNER_ALLOW_CIDRS is not set",
			"bind 127.0.0.1:<port> behind a reverse proxy, or restrict the network with a firewall or SUBSPACE_OWNER_ALLOW_CIDRS", addr)
	case isLocal(host):
		return result(OK, id, "listens on loopback only (%s)", "", addr)
	}
	return result(Info, id, "listens on %s", "", addr)
}

func checkCapacityFile(_ context.Context, e Env) []Result {
	const id = "config.capacity_file"
	path := e.Getenv("SUBSPACE_CAPACITY_FILE")
	if path == "" {
		return result(Info, id, "SUBSPACE_CAPACITY_FILE not set, built-in capacity defaults apply", "")
	}
	c, err := capacity.Load(path)
	if err != nil {
		return result(Error, id, "%v", "fix the JSON file or unset SUBSPACE_CAPACITY_FILE", err)
	}
	return result(OK, id, "capacity file valid: global max %d, %d limits", "", c.GlobalMaxAgents, len(c.Limits))
}

func checkNotify(_ context.Context, e Env) []Result {
	const id = "config.notify"
	cfg, err := notify.ConfigFromEnv(e.Getenv)
	switch {
	case err != nil:
		return result(Error, id, "%v", "fix the SUBSPACE_NOTIFY_* variables named in the message", err)
	case len(cfg.Sinks) == 0:
		return result(Info, id, "no notification sinks configured", "")
	}
	return result(OK, id, "%d notification sinks configured", "", len(cfg.Sinks))
}

func checkSpawner(_ context.Context, e Env) []Result {
	const id = "config.spawner"
	name := getenv(e, "SUBSPACE_SPAWNER", "noop")
	if _, err := spawner.New(name, e.Getenv); err != nil {
		return result(Error, id, "%v", "fix SUBSPACE_SPAWNER and the SUBSPACE_HC_* / SUBSPACE_OPENCODE_* variables", err)
	}
	if name == "docker" {
		return result(Warn, id, "the docker spawner is a stub, every spawn fails", "use noop, hc or opencode")
	}
	return result(OK, id, "spawner %s", "", name)
}

func checkRateLimits(_ context.Context, e Env) []Result {
	const id = "config.rate_limits"
	var out []Result
	for _, key := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL", "SUBSPACE_RATE_ACTIONLINK"} {
		if v := e.Getenv(key); v != "" && !validRate(v) {
			out = append(out, result(Warn, id, "%s=%q is invalid, the built-in default is used",
				"use <n>/s[:burst], <n>/m[:burst], or 0/off", key, v)...)
		}
	}
	if len(out) == 0 {
		return result(OK, id, "rate limit variables parse", "")
	}
	return out
}

func validRate(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" || v == "off" {
		return true
	}
	rate, burst, hasBurst := strings.Cut(v, ":")
	n, unit, ok := strings.Cut(rate, "/")
	if !ok {
		return false
	}
	per, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
	if err != nil || per <= 0 || math.IsInf(per, 0) || math.IsNaN(per) {
		return false
	}
	if u := strings.TrimSpace(unit); u != "s" && u != "m" {
		return false
	}
	if hasBurst {
		b, err := strconv.Atoi(strings.TrimSpace(burst))
		return err == nil && b >= 1
	}
	return true
}

func checkEnvValues(_ context.Context, e Env) []Result {
	const id = "config.env"
	var out []Result
	for _, key := range []string{"SUBSPACE_LEASE", "SUBSPACE_BACKUP_INTERVAL", "SUBSPACE_SHUTDOWN_TIMEOUT"} {
		if v := e.Getenv(key); v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				out = append(out, result(Error, id, "%s=%q is not a duration", "use Go syntax such as 15m or 24h", key, v)...)
			}
		}
	}
	for _, key := range []string{"SUBSPACE_POISON_EXPIRIES", "SUBSPACE_BACKUP_KEEP"} {
		if v := e.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				out = append(out, result(Error, id, "%s=%q is not a non-negative integer", "", key, v)...)
			}
		}
	}
	if len(out) == 0 {
		return result(OK, id, "durations and counts parse", "")
	}
	return out
}
