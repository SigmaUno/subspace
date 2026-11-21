package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/logpipe"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startLogged(t *testing.T, sample string) (*servertest.Env, *lockedBuffer) {
	t.Helper()
	t.Setenv("SUBSPACE_LOG_SAMPLE", sample)
	out := &lockedBuffer{}
	pipe := logpipe.New(logpipe.Options{Format: logpipe.FormatJSON, Out: out, Level: slog.LevelInfo})
	e := servertest.Start(t, server.Config{
		PublicURL: "http://sub.test", Log: pipe.Logger(), LogPipe: pipe,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	return e, out
}

type logLine struct {
	ID    int64             `json:"id"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs"`
}

func fetchLogs(t *testing.T, e *servertest.Env, query string) []logLine {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/logs"+query, nil)
	if code != 200 {
		t.Fatalf("GET logs%s: %d %v", query, code, out)
	}
	b, _ := json.Marshal(out["lines"])
	var lines []logLine
	if err := json.Unmarshal(b, &lines); err != nil {
		t.Fatal(err)
	}
	return lines
}

func requestLines(lines []logLine, route string) []logLine {
	var out []logLine
	for _, l := range lines {
		if l.Msg == "request" && l.Attrs["route"] == route {
			out = append(out, l)
		}
	}
	return out
}

func TestAccessLogOverHTTPNeverLeaksCredentials(t *testing.T) {
	e, out := startLogged(t, "1")
	agent := e.Register("claude-1", "worker", "claude-code")
	e.Must(agent, "heartbeat", map[string]any{})

	secretQuery := "SECRETQUERYVALUE9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3"
	req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/state?token="+secretQuery, nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	req.Header.Set("X-Request-Id", "it-state-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("X-Request-Id") != "it-state-1" {
		t.Fatalf("request id header = %q", resp.Header.Get("X-Request-Id"))
	}
	pathSecret := "/a/PATHSECRET9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3Gf5Ds9"
	resp, err = http.Get(e.TS.URL + pathSecret)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	lines := fetchLogs(t, e, "")
	state := requestLines(lines, "/api/v1/state")
	if len(state) != 1 {
		t.Fatalf("state request lines: %+v", state)
	}
	a := state[0].Attrs
	if a["method"] != "GET" || a["status"] != "200" || a["scope"] != "owner" || a["request_id"] != "it-state-1" || a["agent"] != "" || a["bytes"] == "0" || a["duration"] == "" {
		t.Fatalf("attrs = %v", a)
	}
	hb := requestLines(lines, "/mcp")
	if len(hb) == 0 || hb[len(hb)-1].Attrs["agent"] != "claude-1" || hb[len(hb)-1].Attrs["scope"] != "" {
		t.Fatalf("mcp lines: %+v", hb)
	}
	if got := requestLines(lines, "/a/"); len(got) != 1 || got[0].Attrs["status"] != "404" {
		t.Fatalf("path-token request: %+v", lines)
	}

	b, _ := json.Marshal(lines)
	for _, secret := range []string{servertest.OwnerToken, agent, secretQuery, "PATHSECRET"} {
		if strings.Contains(out.String(), secret) || strings.Contains(string(b), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

func TestLogSamplingOverHTTP(t *testing.T) {
	e, _ := startLogged(t, "0")
	agent := e.Register("claude-1", "worker", "claude-code")
	for range 5 {
		resp, err := http.Get(e.TS.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	e.Must(agent, "heartbeat", map[string]any{})
	e.Must(agent, "whoami", map[string]any{})
	e.REST("GET", "/api/v1/state", nil)
	e.REST("GET", "/api/v1/nope", nil)

	lines := fetchLogs(t, e, "")
	if got := requestLines(lines, "/healthz"); len(got) != 0 {
		t.Fatalf("health checks logged: %+v", got)
	}
	if got := requestLines(lines, "/api/v1/logs"); len(got) != 0 {
		t.Fatalf("log polls logged: %+v", got)
	}
	if got := requestLines(lines, "/mcp"); len(got) != 1 {
		t.Fatalf("mcp lines = %d, want only whoami (heartbeat is sampled out): %+v", len(got), got)
	}
	if len(requestLines(lines, "/api/v1/state")) != 1 || len(requestLines(lines, "/api/v1/")) != 1 {
		t.Fatalf("always-logged requests missing: %+v", lines)
	}
}

func TestLogLevelControlOverHTTP(t *testing.T) {
	e, out := startLogged(t, "1")

	if code, got := e.REST("GET", "/api/v1/log-level", nil); code != 200 || got["level"] != "info" {
		t.Fatalf("GET log-level: %d %v", code, got)
	}
	if code, got := e.REST("PUT", "/api/v1/log-level", map[string]any{"level": "loud"}); code != 400 {
		t.Fatalf("bad level: %d %v", code, got)
	}
	if code, got := e.REST("PUT", "/api/v1/log-level", map[string]any{}); code != 400 {
		t.Fatalf("empty level: %d %v", code, got)
	}
	if code, got := e.REST("PUT", "/api/v1/log-level", map[string]any{"level": "warn"}); code != 200 || got["level"] != "warn" {
		t.Fatalf("PUT warn: %d %v", code, got)
	}
	e.REST("GET", "/api/v1/state", nil)
	e.REST("GET", "/api/v1/nope", nil)
	before := out.String()
	e.REST("GET", "/api/v1/me", nil)
	if out.String() != before {
		t.Fatalf("info line written at warn level:\n%s", strings.TrimPrefix(out.String(), before))
	}
	if got := requestLines(fetchLogs(t, e, ""), "/api/v1/state"); len(got) != 0 {
		t.Fatalf("info request logged at warn level: %+v", got)
	}

	if code, _ := e.REST("PUT", "/api/v1/log-level", map[string]any{"level": "debug"}); code != 200 {
		t.Fatal("PUT debug")
	}
	e.REST("GET", "/api/v1/me", nil)
	if got := requestLines(fetchLogs(t, e, "?level=info"), "/api/v1/me"); len(got) == 0 {
		t.Fatal("request not logged after level raised to debug")
	}

	_, audit := e.REST("GET", "/api/v1/audit?tool=set_log_level", nil)
	events, _ := audit["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("audit events: %v", audit)
	}
	last := events[1].(map[string]any)
	if last["before"].(map[string]any)["level"] != "warn" || last["after"].(map[string]any)["level"] != "debug" || last["note"] != "token=root" {
		t.Fatalf("audit event: %v", last)
	}
}

func TestLogsFiltersAndAccess(t *testing.T) {
	e, _ := startLogged(t, "1")
	e.REST("GET", "/api/v1/nope", nil)
	e.REST("GET", "/api/v1/state", nil)

	all := fetchLogs(t, e, "")
	if len(all) < 2 {
		t.Fatalf("lines: %+v", all)
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("lines not ascending: %+v", all)
		}
	}
	tail := fetchLogs(t, e, "?since="+jsonNumber(all[len(all)-2].ID))
	if len(tail) < 1 || tail[0].ID != all[len(all)-1].ID {
		t.Fatalf("since id: %+v", tail)
	}
	if got := fetchLogs(t, e, "?since="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); len(got) != 0 {
		t.Fatalf("since future: %+v", got)
	}
	if got := fetchLogs(t, e, "?level=error"); len(got) != 0 {
		t.Fatalf("error lines: %+v", got)
	}
	if got := fetchLogs(t, e, "?limit=1"); len(got) != 1 {
		t.Fatalf("limit: %+v", got)
	}
	for _, q := range []string{"?level=loud", "?since=yesterday", "?limit=0", "?limit=x"} {
		if code, _ := e.REST("GET", "/api/v1/logs"+q, nil); code != 400 {
			t.Errorf("GET logs%s = %d, want 400", q, code)
		}
	}

	_, tok := e.REST("POST", "/api/v1/tokens", map[string]any{"name": "ci", "scope": "viewer"})
	viewer, _ := tok["secret"].(string)
	agent := e.Register("claude-1", "worker", "claude-code")
	for _, c := range []struct{ tok, method, path string }{
		{viewer, "PUT", "/api/v1/log-level"},
		{agent, "GET", "/api/v1/logs"},
		{"", "GET", "/api/v1/logs"},
	} {
		code, _ := e.RESTAs(c.tok, c.method, c.path, map[string]any{"level": "debug"})
		want := 403
		if c.tok == "" {
			want = 401
		}
		if code != want {
			t.Errorf("%s %s with %.8q = %d, want %d", c.method, c.path, c.tok, code, want)
		}
	}
	if code, got := e.RESTAs(viewer, "GET", "/api/v1/log-level", nil); code != 200 || got["level"] != "info" {
		t.Fatalf("viewer read: %d %v", code, got)
	}
	if code, _ := e.RESTAs(viewer, "GET", "/api/v1/logs", nil); code != 200 {
		t.Fatalf("viewer logs: %d", code)
	}
	if _, got := e.REST("GET", "/api/v1/log-level", nil); got["level"] != "info" {
		t.Fatalf("level changed by forbidden callers: %v", got)
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
