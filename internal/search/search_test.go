package search_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/search"
	"github.com/SigmaUno/subspace/internal/server"
)

const ownerTok = "owner-token-for-search-tests-0123"

type env struct {
	t  *testing.T
	ts *httptest.Server
	s  *server.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{DBPath: filepath.Join(t.TempDir(), "s.db"), OwnerToken: ownerTok,
		PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, s: s}
}

func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) search(query string) (int, []map[string]any) {
	e.t.Helper()
	code, out := e.do(http.MethodGet, "/api/v1/search/?"+query, ownerTok, nil)
	var rs []map[string]any
	for _, r := range asSlice(out["results"]) {
		rs = append(rs, r.(map[string]any))
	}
	return code, rs
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func (e *env) brief(title string) string {
	e.t.Helper()
	code, out := e.do(http.MethodPost, "/api/v1/briefs", ownerTok, map[string]any{"title": title, "body": "body"})
	if code != 201 {
		e.t.Fatalf("create brief %q: %d %v", title, code, out)
	}
	return out["id"].(string)
}

func (e *env) task(briefID, title, spec string) string {
	e.t.Helper()
	tk, err := e.s.Board.CreateTask(context.Background(), model.Owner, board.TaskInput{
		BriefID: briefID, Title: title, Spec: spec, Repo: "SigmaUno/foo", Role: "worker",
		AcceptanceCriteria: []string{"works"},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return tk.ID
}

func (e *env) agent(id string) string {
	e.t.Helper()
	code, out := e.do(http.MethodPost, "/api/v1/agents", ownerTok, map[string]any{"id": id, "role": "worker", "kind": "claude-code", "model": "sonnet-login"})
	if code != 201 {
		e.t.Fatalf("register %s: %d %v", id, code, out)
	}
	return out["connect"].(map[string]any)["token"].(string)
}

func ids(rs []map[string]any) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, fmt.Sprint(r["kind"], ":", r["id"]))
	}
	return out
}

func TestSearchFindsEveryKindWithHighlights(t *testing.T) {
	e := newEnv(t)
	b := e.brief("Login flow")
	tk := e.task(b, "Add login page", "uses sessions for the user")
	_, _ = e.do(http.MethodPost, "/api/v1/threads/task/"+tk+"/messages", ownerTok, map[string]any{"body": "the login works now"})
	e.agent("login-bot")

	code, rs := e.search("q=login")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(rs) != 4 {
		t.Fatalf("want one hit per kind, got %v", ids(rs))
	}
	for _, r := range rs {
		if !strings.Contains(r["snippet"].(string), "«login»") && !strings.Contains(r["snippet"].(string), "«Login»") {
			t.Errorf("snippet without highlight: %v", r)
		}
	}

	_, rs = e.search("q=login&kind=task")
	if len(rs) != 1 || rs[0]["kind"] != "task" || rs[0]["field"] != "title" || rs[0]["id"] != tk {
		t.Fatalf("kind=task: %v", rs)
	}

	_, rs = e.search("q=sessions&kind=task")
	if len(rs) != 1 || rs[0]["field"] != "spec" || !strings.Contains(rs[0]["snippet"].(string), "«sessions»") {
		t.Fatalf("spec match: %v", rs)
	}

	_, rs = e.search("q=sonnet-login&kind=agent")
	if len(rs) != 1 || rs[0]["field"] != "model" {
		t.Fatalf("agent model match: %v", rs)
	}
}

func TestSearchRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"", "q=%20%20", "q=x&kind=secret", "q=x&limit=0", "q=x&limit=abc",
		"q=" + strings.Repeat("a", search.MaxQueryRunes+1)} {
		if code, _ := e.search(q); code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q[:min(len(q), 20)], code)
		}
	}
}

func TestSearchRequiresOwner(t *testing.T) {
	e := newEnv(t)
	tok := e.agent("bot-one")
	if code, _ := e.do(http.MethodGet, "/api/v1/search/?q=bot", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := e.do(http.MethodGet, "/api/v1/search/?q=bot", tok, nil); code != http.StatusForbidden {
		t.Errorf("agent token: %d", code)
	}
}

func TestSearchEscapesLikeWildcards(t *testing.T) {
	e := newEnv(t)
	e.brief("100% done")
	e.brief("snake_case name")
	e.brief(`back\slash`)
	e.brief("plain text")

	for _, q := range []string{"%", "_", `\`, "e_"} {
		_, rs := e.search("q=" + url.QueryEscape(q) + "&kind=brief")
		if len(rs) != 1 || !strings.Contains(rs[0]["snippet"].(string), "«"+q+"»") {
			t.Errorf("q=%q: got %v", q, rs)
		}
	}
}

func TestSearchLimits(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.brief(fmt.Sprintf("limit probe %d", i))
	}
	for limit, want := range map[string]int{"": 5, "2": 2, "1000": 5} {
		q := "q=probe&kind=brief"
		if limit != "" {
			q += "&limit=" + limit
		}
		if _, rs := e.search(q); len(rs) != want {
			t.Errorf("limit=%q: got %d results, want %d", limit, len(rs), want)
		}
	}
}

func TestSearchOrdersNewestFirst(t *testing.T) {
	e := newEnv(t)
	e.brief("ordering old")
	time.Sleep(5 * time.Millisecond)
	newID := e.brief("ordering new")
	_, rs := e.search("q=ordering&kind=brief")
	if len(rs) != 2 || rs[0]["id"] != newID {
		t.Fatalf("newest first: %v", rs)
	}
}

func TestSearchNeverExposesAgentSecrets(t *testing.T) {
	e := newEnv(t)
	tok := e.agent("tok-bot")

	for _, q := range []string{tok, "ssp_", auth.Hash(tok), tok[:12]} {
		_, rs := e.search("q=" + url.QueryEscape(q))
		if len(rs) != 0 {
			t.Errorf("secret %q matched %v", q[:min(len(q), 8)], ids(rs))
		}
	}

	_, rs := e.search("q=tok-bot&kind=agent")
	if len(rs) != 1 {
		t.Fatalf("agent not found by id: %v", rs)
	}
	raw, _ := json.Marshal(rs)
	if strings.Contains(string(raw), tok) || strings.Contains(string(raw), auth.Hash(tok)) {
		t.Fatalf("results leak credentials: %s", raw)
	}
}
