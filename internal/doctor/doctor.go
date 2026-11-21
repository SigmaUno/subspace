package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Severity string

const (
	OK    Severity = "ok"
	Info  Severity = "info"
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Result struct {
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
}

type Report struct {
	Results  []Result `json:"results"`
	Errors   int      `json:"errors"`
	Warnings int      `json:"warnings"`
}

type Env struct {
	Getenv     func(string) string
	Now        func() time.Time
	HTTP       *http.Client
	LookPath   func(string) (string, error)
	Output     func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupHost func(ctx context.Context, host string) ([]string, error)
	DiskFree   func(dir string) (uint64, error)
}

func SystemEnv() Env {
	return Env{
		Getenv:   os.Getenv,
		Now:      time.Now,
		HTTP:     &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // operator-configured binary
		},
		LookupHost: net.DefaultResolver.LookupHost,
		DiskFree:   diskFree,
	}
}

type check func(ctx context.Context, e Env) []Result

var checks = []check{
	checkEnvValues, checkOwnerToken, checkPublicURL, checkListenAddr, checkCapacityFile, checkNotify, checkSpawner, checkRateLimits,
	checkDatabase, checkDisk, checkBackups,
	checkTimeSkew, checkHC, checkForgejoMirror, checkPRSyncHosts, checkNotifyDNS,
}

func Run(ctx context.Context, e Env) Report {
	scrub := scrubber(e)
	var r Report
	for _, c := range checks {
		for _, res := range c(ctx, e) {
			res.Message, res.Fix = scrub(res.Message), scrub(res.Fix)
			switch res.Severity {
			case Error:
				r.Errors++
			case Warn:
				r.Warnings++
			}
			r.Results = append(r.Results, res)
		}
	}
	return r
}

func (r Report) WriteJSON(w io.Writer) error {
	if r.Results == nil {
		r.Results = []Result{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r Report) WriteText(w io.Writer) error {
	for _, res := range r.Results {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", res.Severity, res.Check, res.Message); err != nil {
			return err
		}
		if res.Fix != "" {
			if _, err := fmt.Fprintf(w, "      fix: %s\n", res.Fix); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "%d errors, %d warnings\n", r.Errors, r.Warnings)
	return err
}

func result(sev Severity, check, msg, fix string, args ...any) []Result {
	return []Result{{Check: check, Severity: sev, Message: fmt.Sprintf(msg, args...), Fix: fix}}
}

var secretEnvs = []string{
	"SUBSPACE_OWNER_TOKEN", "SUBSPACE_ACTION_SECRET", "SUBSPACE_NOTIFY_SECRET", "SUBSPACE_NOTIFY_WEBHOOK_URL",
	"SUBSPACE_NOTIFY_NTFY_URL", "SUBSPACE_NOTIFY_SLACK_URL", "SUBSPACE_NOTIFY_SMTP_URL", "SUBSPACE_NOTIFY_TELEGRAM_BOT_TOKEN",
	"SUBSPACE_MIRROR_FORGEJO_TOKEN", "SUBSPACE_WEBHOOK_SECRET_GITHUB", "SUBSPACE_WEBHOOK_SECRET_FORGEJO",
}

func scrubber(e Env) func(string) string {
	var pairs []string
	add := func(v string) {
		if v = strings.TrimSpace(v); v != "" {
			pairs = append(pairs, v, "***")
		}
	}
	for _, k := range secretEnvs {
		v := e.Getenv(k)
		add(v)
		if u, err := url.Parse(v); err == nil && u.User != nil {
			pw, _ := u.User.Password()
			add(pw)
		}
	}
	if tok, err := ownerTokenFile(e); err == nil {
		add(tok)
	}
	if len(pairs) == 0 {
		return func(s string) string { return s }
	}
	return strings.NewReplacer(pairs...).Replace
}
