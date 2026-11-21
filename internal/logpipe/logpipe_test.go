package logpipe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/logpipe"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const (
	ghToken    = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	agentToken = "ssp_9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3Gf5Ds9Aq2We4"
	pathToken  = "9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3Gf5Ds9Aq2We4Zz"
)

func newPipe(format logpipe.Format) (*logpipe.Pipeline, *syncBuffer) {
	out := &syncBuffer{}
	return logpipe.New(logpipe.Options{Format: format, Out: out, Level: slog.LevelDebug}), out
}

func accessServer(p *logpipe.Pipeline, clock *fakeClock, sample float64, h http.HandlerFunc) http.Handler {
	return logpipe.AccessLog(h, logpipe.AccessOptions{
		Log:    p.Logger(),
		Now:    clock.Now,
		Sample: sample,
		Route: func(r *http.Request) string {
			switch {
			case strings.HasPrefix(r.URL.Path, "/a/"):
				return "GET /a/{token}"
			case r.URL.Path == "/mcp":
				return "/mcp"
			case r.URL.Path == "/healthz":
				return "GET /healthz"
			}
			return ""
		},
		Actor: func(r *http.Request) (string, string) {
			if r.Header.Get("Authorization") == "Bearer "+agentToken {
				return "claude-1", ""
			}
			return "", ""
		},
	})
}

func lastLine(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("not json: %q: %v", lines[len(lines)-1], err)
	}
	return m
}

func TestAccessLogFieldsUseFakeClockAndNeverLeakSecrets(t *testing.T) {
	p, out := newPipe(logpipe.FormatJSON)
	clock := &fakeClock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
	h := accessServer(p, clock, 1, func(w http.ResponseWriter, r *http.Request) {
		clock.Advance(42 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("0123456789"))
	})

	req := httptest.NewRequest(http.MethodGet, "/a/"+pathToken+"?token="+ghToken+"&x=1", strings.NewReader(`{"password":"hunter2-body"}`))
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("Cookie", "session="+ghToken)
	req.Header.Set("X-Request-Id", "req-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	m := lastLine(t, out.String())
	want := map[string]any{
		"msg": "request", "method": "GET", "route": "/a/{token}", "status": float64(201),
		"duration": float64(42 * time.Millisecond), "bytes": float64(10), "request_id": "req-1", "agent": "claude-1",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if rec.Header().Get("X-Request-Id") != "req-1" {
		t.Errorf("response request id = %q", rec.Header().Get("X-Request-Id"))
	}

	all := out.String()
	for _, l := range p.Lines(logpipe.Query{Limit: 10}) {
		b, _ := json.Marshal(l)
		all += string(b)
	}
	for _, secret := range []string{ghToken, agentToken, pathToken, "hunter2-body", "x=1"} {
		if strings.Contains(all, secret) {
			t.Errorf("log output contains %q:\n%s", secret, all)
		}
	}
}

func TestAccessLogGeneratesRequestIDAndMarksUnmatched(t *testing.T) {
	p, out := newPipe(logpipe.FormatJSON)
	clock := &fakeClock{t: time.Unix(0, 0)}
	h := accessServer(p, clock, 1, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	req := httptest.NewRequest(http.MethodGet, "/secret/"+pathToken, nil)
	req.Header.Set("X-Request-Id", "bad id\nforged line")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	m := lastLine(t, out.String())
	id, _ := m["request_id"].(string)
	if id == "" || id == "bad id\nforged line" || rec.Header().Get("X-Request-Id") != id {
		t.Fatalf("request id %q, header %q", id, rec.Header().Get("X-Request-Id"))
	}
	if m["route"] != "unmatched" || strings.Contains(out.String(), pathToken) {
		t.Fatalf("route %v, output %s", m["route"], out.String())
	}
}

func TestAccessLogLevelFollowsStatus(t *testing.T) {
	p, _ := newPipe(logpipe.FormatText)
	clock := &fakeClock{t: time.Unix(0, 0)}
	status := http.StatusOK
	h := accessServer(p, clock, 1, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
	for _, status = range []int{200, 404, 503} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	var levels []string
	for _, l := range p.Lines(logpipe.Query{}) {
		levels = append(levels, l.Level)
	}
	if got := strings.Join(levels, ","); got != "info,info,warn" {
		t.Fatalf("levels = %s", got)
	}
}

func TestSamplingIsDeterministicAndNested(t *testing.T) {
	kept := func(rate float64) map[string]bool {
		m := map[string]bool{}
		for i := range 20000 {
			id := fmt.Sprintf("req-%d", i)
			if logpipe.Sampled(id, rate) {
				m[id] = true
			}
		}
		return m
	}
	a, b := kept(0.25), kept(0.25)
	if len(a) != len(b) {
		t.Fatalf("not deterministic: %d vs %d", len(a), len(b))
	}
	for id := range a {
		if !b[id] {
			t.Fatalf("%s kept once and dropped once", id)
		}
	}
	if share := float64(len(a)) / 20000; share < 0.22 || share > 0.28 {
		t.Fatalf("share = %.3f, want about 0.25", share)
	}
	for id := range kept(0.1) {
		if !a[id] {
			t.Fatalf("%s kept at 0.1 but not at 0.25", id)
		}
	}
	if len(kept(0)) != 0 || len(kept(1)) != 20000 {
		t.Fatalf("rate 0 and 1 must drop and keep everything")
	}
}

func TestSamplingOnlyAppliesToSuccessfulNoise(t *testing.T) {
	p, _ := newPipe(logpipe.FormatText)
	clock := &fakeClock{t: time.Unix(0, 0)}
	status := http.StatusOK
	h := accessServer(p, clock, 0, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
	})
	call := func(path, body string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	heartbeat := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"heartbeat","arguments":{}}}`
	claim := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"claim_task","arguments":{"task_id":"T-1"}}}`
	huge := `{"method":"tools/call","params":{"arguments":{"pad":"` + strings.Repeat("x", 4096) + `"},"name":"heartbeat"}}`

	call("/healthz", "")
	call("/mcp", heartbeat)
	call("/mcp", huge)
	call("/mcp", claim)
	call("/x", "")
	status = http.StatusServiceUnavailable
	call("/healthz", "")

	var got []string
	for _, l := range p.Lines(logpipe.Query{}) {
		got = append(got, l.Attrs["route"]+":"+l.Attrs["status"])
	}
	want := "/mcp:200,/mcp:200,unmatched:200,/healthz:503"
	if strings.Join(got, ",") != want {
		t.Fatalf("logged %v, want %s", got, want)
	}
}

func TestRingKeepsLastLinesAndFilters(t *testing.T) {
	p, _ := newPipe(logpipe.FormatText)
	log := p.Logger()
	base := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= 2500; i++ {
		rec := slog.NewRecord(base.Add(time.Duration(i)*time.Second), slog.LevelInfo, fmt.Sprintf("line %d", i), 0)
		if i%100 == 0 {
			rec.Level = slog.LevelError
		}
		_ = log.Handler().Handle(context.Background(), rec)
	}

	all := p.Lines(logpipe.Query{Min: slog.LevelDebug})
	if len(all) != 2000 || all[0].ID != 501 || all[len(all)-1].ID != 2500 || all[0].Msg != "line 501" {
		t.Fatalf("got %d lines, first %d, last %d", len(all), all[0].ID, all[len(all)-1].ID)
	}
	errs := p.Lines(logpipe.Query{Min: slog.LevelError})
	if len(errs) != 20 || errs[0].Msg != "line 600" {
		t.Fatalf("errors: %d, first %q", len(errs), errs[0].Msg)
	}
	if got := p.Lines(logpipe.Query{SinceID: 2497}); len(got) != 3 || got[0].ID != 2498 {
		t.Fatalf("since id: %+v", got)
	}
	if got := p.Lines(logpipe.Query{SinceTS: base.Add(2498 * time.Second)}); len(got) != 2 || got[0].Msg != "line 2499" {
		t.Fatalf("since ts: %+v", got)
	}
	if got := p.Lines(logpipe.Query{Limit: 2}); len(got) != 2 || got[1].ID != 2500 || got[0].ID != 2499 {
		t.Fatalf("limit: %+v", got)
	}
}

func TestRuntimeLevelGatesRingAndOutput(t *testing.T) {
	p, out := newPipe(logpipe.FormatText)
	log := p.Logger()
	log.Debug("d1")
	p.SetLevel(slog.LevelWarn)
	log.Info("i1")
	log.Warn("w1")
	p.SetLevel(slog.LevelInfo)
	log.Info("i2")

	var msgs []string
	for _, l := range p.Lines(logpipe.Query{Min: slog.LevelDebug}) {
		msgs = append(msgs, l.Msg)
	}
	if got := strings.Join(msgs, ","); got != "d1,w1,i2" {
		t.Fatalf("ring = %s", got)
	}
	if strings.Contains(out.String(), "i1") {
		t.Fatalf("output has a gated line:\n%s", out.String())
	}
}

func TestRingRedactsMessagesAndAttributes(t *testing.T) {
	p, _ := newPipe(logpipe.FormatText)
	log := p.Logger().With("authorization", "Bearer short", slog.Group("req", slog.String("api_key", "k"), slog.String("note", "token="+ghToken)))
	log.WithGroup("g").Error("failed with "+ghToken, "err", fmt.Errorf("dial: Authorization: Bearer %s", agentToken), "db_password", "hunter2", "n", 3)

	lines := p.Lines(logpipe.Query{})
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	b, _ := json.Marshal(lines[0])
	for _, secret := range []string{ghToken, agentToken, "hunter2", "Bearer short", `"k"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("ring line contains %q: %s", secret, b)
		}
	}
	l := lines[0]
	for key, want := range map[string]string{
		"authorization": "****", "req.api_key": "****", "g.db_password": "****", "g.n": "3",
	} {
		if l.Attrs[key] != want {
			t.Errorf("attrs[%s] = %q, want %q", key, l.Attrs[key], want)
		}
	}
	if !strings.HasPrefix(l.Msg, "failed with ghp_****") {
		t.Errorf("msg = %q", l.Msg)
	}
}

func TestWrapKeepsInnerHandlerOutput(t *testing.T) {
	out := &syncBuffer{}
	p := logpipe.Wrap(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}), 0)
	log := p.Logger()
	log.Debug("hidden")
	log.Info("shown", "k", "v")
	if p.Level() != slog.LevelInfo {
		t.Fatalf("level = %v", p.Level())
	}
	if m := lastLine(t, out.String()); m["msg"] != "shown" || m["k"] != "v" {
		t.Fatalf("output %s", out.String())
	}
	if strings.Contains(out.String(), "hidden") || len(p.Lines(logpipe.Query{})) != 1 {
		t.Fatalf("debug line leaked: %s", out.String())
	}
}
