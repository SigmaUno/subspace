package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func join(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestExitCodes(t *testing.T) {
	e := servertest.New(t)
	worker := e.Register("wally", "worker", "claude-code")
	revoked := e.Register("gone", "worker", "codex")
	looped := e.Register("loopy", "worker", "opencode")
	if code, _ := e.REST("POST", "/api/v1/agents/gone/revoke", map[string]any{"reason": "test"}); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	for i := 0; i < 8; i++ {
		e.Call(looped, "add_note", map[string]any{"body": "same"})
	}
	ownerAsAgent := servertest.OwnerToken

	tests := []struct {
		name  string
		url   string
		token string
		want  int
		out   string
	}{
		{"ok", e.TS.URL, worker, exitOK, "ready"},
		{"mcp url accepted", e.TS.URL + "/mcp", worker, exitOK, "ready"},
		{"bad token", e.TS.URL, "ssp_nope", exitAuth, ""},
		{"revoked token", e.TS.URL, revoked, exitAuth, ""},
		{"owner token on mcp", e.TS.URL, ownerAsAgent, exitAuth, ""},
		{"agent paused", e.TS.URL, looped, exitPaused, "not ready: paused"},
		{"unreachable", "http://127.0.0.1:1", worker, exitUnreachable, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := join(t, nil, "-url", tc.url, "-token", tc.token)
			if code != tc.want {
				t.Fatalf("exit %d want %d\nstdout:\n%s\nstderr:\n%s", code, tc.want, stdout, stderr)
			}
			if !strings.Contains(stdout, tc.out) {
				t.Fatalf("stdout missing %q:\n%s", tc.out, stdout)
			}
			if strings.Contains(stdout+stderr, tc.token) {
				t.Fatalf("token leaked:\n%s\n%s", stdout, stderr)
			}
		})
	}

	t.Run("workspace paused", func(t *testing.T) {
		if code, _ := e.REST("POST", "/api/v1/pause-all", map[string]any{"reason": "freeze"}); code != 200 {
			t.Fatalf("pause-all: %d", code)
		}
		code, stdout, _ := join(t, nil, "-url", e.TS.URL, "-token", worker)
		if code != exitPaused || !strings.Contains(stdout, "workspace:    paused") {
			t.Fatalf("exit %d\n%s", code, stdout)
		}
		if code, _ := e.REST("POST", "/api/v1/resume-all", map[string]any{}); code != 200 {
			t.Fatalf("resume-all: %d", code)
		}
		if code, _, _ := join(t, nil, "-url", e.TS.URL, "-token", worker); code != exitOK {
			t.Fatalf("exit %d after resume", code)
		}
	})
}

func TestReport(t *testing.T) {
	e := servertest.New(t)
	leader := e.Register("rex", "leader", "other")
	worker := e.Register("wally", "worker", "claude-code")

	_, out, _ := join(t, nil, "-url", e.TS.URL, "-token", worker)
	for _, want := range []string{"agent:        wally", "role:         worker", "claim_task", "submit", "ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("worker report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "propose_plan") {
		t.Fatalf("worker report lists leader tools:\n%s", out)
	}
	if !strings.Contains(out, "notify:       push stream available") {
		t.Fatalf("report does not show the push stream:\n%s", out)
	}
	_, out, _ = join(t, nil, "-url", e.TS.URL, "-token", leader)
	if !strings.Contains(out, "propose_plan") || strings.Contains(out, "claim_task") {
		t.Fatalf("leader report has wrong tools:\n%s", out)
	}
}

func TestTokenSources(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("wally", "worker", "codex")
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		env  map[string]string
		want int
	}{
		{"env", map[string]string{"SUBSPACE_URL": e.TS.URL, "SUBSPACE_TOKEN": tok}, exitOK},
		{"file", map[string]string{"SUBSPACE_URL": e.TS.URL, "SUBSPACE_TOKEN_FILE": file}, exitOK},
		{"file wins over env", map[string]string{"SUBSPACE_URL": e.TS.URL, "SUBSPACE_TOKEN_FILE": file, "SUBSPACE_TOKEN": "ssp_wrong"}, exitOK},
		{"missing token", map[string]string{"SUBSPACE_URL": e.TS.URL}, exitUsage},
		{"missing url", map[string]string{"SUBSPACE_TOKEN": tok}, exitUsage},
		{"unreadable file", map[string]string{"SUBSPACE_URL": e.TS.URL, "SUBSPACE_TOKEN_FILE": file + ".missing"}, exitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := join(t, tc.env)
			if code != tc.want {
				t.Fatalf("exit %d want %d\n%s\n%s", code, tc.want, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, tok) {
				t.Fatalf("token leaked:\n%s\n%s", stdout, stderr)
			}
		})
	}
}

func TestPrintConfig(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("wally", "worker", "claude-code")
	mcpURL := e.TS.URL + "/mcp"

	tests := []struct {
		kind string
		want []string
	}{
		{"claude-code", []string{"claude mcp add --transport http subspace " + mcpURL, "Authorization: Bearer %s"}},
		{"codex", []string{"[mcp_servers.subspace]", `url = "` + mcpURL + `"`, `http_headers = { Authorization = "Bearer %s" }`}},
		{"opencode", []string{`"type":"remote"`, `"url":"` + mcpURL + `"`, `"Authorization":"Bearer %s"`}},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			code, stdout, stderr := join(t, nil, "-url", e.TS.URL, "-token", tok, "-kind", tc.kind, "-print-config")
			if code != exitOK {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			if strings.Contains(stdout+stderr, tok) {
				t.Fatalf("token printed without -show-token:\n%s", stdout)
			}
			for _, w := range tc.want {
				if !strings.Contains(stdout, strings.Replace(w, "%s", redacted, 1)) {
					t.Fatalf("redacted config missing %q:\n%s", w, stdout)
				}
			}

			code, stdout, _ = join(t, nil, "-url", e.TS.URL, "-token", tok, "-kind", tc.kind, "-print-config", "-show-token")
			if code != exitOK {
				t.Fatalf("exit %d with -show-token", code)
			}
			for _, w := range tc.want {
				if !strings.Contains(stdout, strings.Replace(w, "%s", tok, 1)) {
					t.Fatalf("config missing %q:\n%s", w, stdout)
				}
			}
		})
	}

	t.Run("requires kind", func(t *testing.T) {
		if code, _, _ := join(t, nil, "-url", e.TS.URL, "-token", tok, "-print-config"); code != exitUsage {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("unknown kind", func(t *testing.T) {
		if code, _, _ := join(t, nil, "-url", e.TS.URL, "-token", tok, "-kind", "vim"); code != exitUsage {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("no config on auth failure", func(t *testing.T) {
		code, stdout, _ := join(t, nil, "-url", e.TS.URL, "-token", "ssp_bad", "-kind", "codex", "-print-config", "-show-token")
		if code != exitAuth || strings.Contains(stdout, "ssp_bad") {
			t.Fatalf("exit %d\n%s", code, stdout)
		}
	})
}

func TestReportPushStreamUnavailable(t *testing.T) {
	t.Setenv("SUBSPACE_MCP_PUSH", "off")
	e := servertest.New(t)
	worker := e.Register("wally", "worker", "claude-code")
	code, out, _ := join(t, nil, "-url", e.TS.URL, "-token", worker)
	if code != exitOK || !strings.Contains(out, "notify:       push stream unavailable") || !strings.Contains(out, "ready") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
