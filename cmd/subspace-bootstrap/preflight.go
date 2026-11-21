package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/SigmaUno/subspace/internal/about"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/doctor"
	"github.com/SigmaUno/subspace/internal/notify"
)

const (
	statusGo   = "GO"
	statusWarn = "WARN"
	statusNoGo = "NO-GO"

	backupWarnAge = 26 * time.Hour
	backupMaxAge  = 72 * time.Hour
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type verdict struct {
	Verdict string  `json:"verdict"`
	NoGo    int     `json:"no_go"`
	Warn    int     `json:"warn"`
	Checks  []check `json:"checks"`
}

type preflight struct {
	o          *client.Owner
	base       string
	token      string
	skipNotify bool
	hc         *http.Client
	doctor     doctor.Env
	now        func() time.Time
	checks     []check
}

func (p *preflight) add(name, status, format string, args ...any) {
	p.checks = append(p.checks, check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
}

func (p *preflight) run(ctx context.Context) verdict {
	p.runDoctor(ctx)
	secure := p.transport()
	status, detail := tokenStrength(p.token)
	p.add("owner_token", status, "%s", detail)
	if !secure {
		p.add("server", statusNoGo, "skipped: the owner token is not sent over an insecure transport")
	} else if p.server(ctx) {
		p.publicURL(ctx)
		p.notify(ctx)
	}
	v := verdict{Verdict: statusGo, Checks: p.checks}
	for _, c := range p.checks {
		switch c.Status {
		case statusNoGo:
			v.NoGo++
			v.Verdict = statusNoGo
		case statusWarn:
			v.Warn++
		}
	}
	return v
}

func (p *preflight) runDoctor(ctx context.Context) {
	r := doctor.Run(ctx, p.doctor)
	ok := 0
	for _, res := range r.Results {
		switch res.Severity {
		case doctor.Error:
			p.add("doctor."+res.Check, statusNoGo, "%s", withFix(res))
		case doctor.Warn:
			p.add("doctor."+res.Check, statusWarn, "%s", withFix(res))
		default:
			ok++
		}
	}
	p.add("doctor", statusFor(r.Errors > 0, r.Warnings > 0), "%d ok, %d warnings, %d errors", ok, r.Warnings, r.Errors)
}

func withFix(r doctor.Result) string {
	if r.Fix == "" {
		return r.Message
	}
	return r.Message + " (fix: " + r.Fix + ")"
}

func statusFor(noGo, warn bool) string {
	switch {
	case noGo:
		return statusNoGo
	case warn:
		return statusWarn
	}
	return statusGo
}

func (p *preflight) transport() bool {
	u, err := url.Parse(p.base)
	switch {
	case err != nil || u.Host == "":
		p.add("transport", statusNoGo, "invalid server URL")
	case u.Scheme == "https":
		p.add("transport", statusGo, "owner API over HTTPS")
		return true
	case isLoopback(u.Hostname()):
		p.add("transport", statusWarn, "plain HTTP to %s; only acceptable on loopback or an SSH tunnel", u.Host)
		return true
	default:
		p.add("transport", statusNoGo, "owner token would cross the network over plain HTTP to %s", u.Host)
	}
	return false
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func tokenStrength(tok string) (string, string) {
	n := len(tok)
	if n < 24 {
		return statusNoGo, fmt.Sprintf("owner token has %d characters; the server needs at least 24", n)
	}
	var lower, upper, digit, other bool
	distinct := map[rune]bool{}
	for _, r := range tok {
		distinct[r] = true
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	pool := 0
	for _, c := range []struct {
		on   bool
		size int
	}{{lower, 26}, {upper, 26}, {digit, 10}, {other, 32}} {
		if c.on {
			pool += c.size
		}
	}
	bits := int(float64(n) * math.Log2(float64(pool)))
	switch {
	case len(distinct) < 10:
		return statusNoGo, fmt.Sprintf("owner token uses only %d distinct characters", len(distinct))
	case bits < 128:
		return statusWarn, fmt.Sprintf("owner token has at most %d bits; use 32 random bytes (openssl rand -base64 32)", bits)
	}
	return statusGo, fmt.Sprintf("%d characters, at most %d bits", n, bits)
}

func (p *preflight) server(ctx context.Context) bool {
	h, err := p.o.Health(ctx)
	if err != nil {
		p.add("server", statusNoGo, "not reachable: %v", err)
		return false
	}
	p.add("server", statusFor(false, h.Paused), "version %s, paused=%t", h.Version, h.Paused)
	var me struct {
		Scope string `json:"scope"`
	}
	if err := p.o.Do(ctx, "me", "GET", "/api/v1/me", nil, &me); err != nil {
		p.add("owner_auth", statusNoGo, "owner token rejected: %v", err)
		return false
	}
	if me.Scope != "owner" {
		p.add("owner_auth", statusNoGo, "token has scope %s, owner is required", me.Scope)
		return false
	}
	p.add("owner_auth", statusGo, "owner scope")
	var info about.Info
	if err := p.o.Do(ctx, "about", "GET", "/api/v1/about", nil, &info); err != nil {
		p.add("database", statusNoGo, "about: %v", err)
		return true
	}
	switch info.Backend {
	case "postgres":
		p.add("database", statusGo, "postgres")
	case "sqlite":
		size := "unknown size"
		if info.DBBytes != nil {
			size = fmt.Sprintf("%d bytes", *info.DBBytes)
		}
		p.add("database", statusGo, "sqlite (%s); single instance, use Postgres for high availability", size)
	default:
		p.add("database", statusWarn, "unknown backend %q", info.Backend)
	}
	b := info.Backups
	switch {
	case b.Count == 0 || b.LatestAt == nil:
		p.add("backups", statusNoGo, "no backups yet; run subspacectl backup and check SUBSPACE_BACKUP_INTERVAL")
	case !b.LatestVerified:
		p.add("backups", statusNoGo, "latest backup failed its integrity check")
	default:
		age := p.now().Sub(*b.LatestAt).Round(time.Minute)
		status := statusFor(age > backupMaxAge, age > backupWarnAge)
		p.add("backups", status, "%d backups, latest verified, %s old", b.Count, age)
	}
	return true
}

func (p *preflight) publicURL(ctx context.Context) {
	var doc struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	}
	if err := p.o.Do(ctx, "openapi", "GET", "/api/v1/openapi.json", nil, &doc); err != nil || len(doc.Servers) == 0 {
		p.add("public_url", statusWarn, "could not read the public URL from the OpenAPI document")
		return
	}
	pub := doc.Servers[0].URL
	u, err := url.Parse(pub)
	if err != nil || u.Host == "" {
		p.add("public_url", statusNoGo, "SUBSPACE_PUBLIC_URL %q is not a URL", pub)
		return
	}
	if u.Scheme != "https" {
		if isLoopback(u.Hostname()) {
			p.add("public_url", statusWarn, "%s is plain HTTP on loopback; remote agents cannot join", pub)
		} else {
			p.add("public_url", statusNoGo, "%s is plain HTTP; agent tokens would travel unencrypted", pub)
		}
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(pub, "/")+"/healthz", nil)
	if err != nil {
		p.add("public_url", statusNoGo, "%s: %v", pub, err)
		return
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		p.add("public_url", statusNoGo, "%s/healthz not reachable: %v", pub, err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.add("public_url", statusNoGo, "%s/healthz answered %d", pub, resp.StatusCode)
		return
	}
	p.add("public_url", statusGo, "%s serves /healthz over HTTPS", pub)
}

func (p *preflight) notify(ctx context.Context) {
	if p.skipNotify {
		p.add("notifications", statusWarn, "test skipped")
		return
	}
	var out struct {
		Results []notify.SinkResult `json:"results"`
	}
	if err := p.o.Do(ctx, "notify-test", "POST", "/api/v1/notify/test", struct{}{}, &out); err != nil {
		p.add("notifications", statusNoGo, "test failed: %v", err)
		return
	}
	if len(out.Results) == 0 {
		p.add("notifications", statusWarn, "no notification sink configured; questions and alerts only show in the UI")
		return
	}
	var failed []string
	for _, r := range out.Results {
		if !r.OK {
			failed = append(failed, r.Sink+": "+r.Error)
		}
	}
	if len(failed) > 0 {
		p.add("notifications", statusNoGo, "%d of %d sinks failed: %s", len(failed), len(out.Results), strings.Join(failed, "; "))
		return
	}
	p.add("notifications", statusGo, "test sent through %d sinks", len(out.Results))
}

func (v verdict) write(w io.Writer, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	for _, c := range v.Checks {
		if _, err := fmt.Fprintf(w, "%-6s %-28s %s\n", c.Status, c.Name, c.Detail); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%s: %d blocking, %d warnings\n", v.Verdict, v.NoGo, v.Warn)
	return err
}
