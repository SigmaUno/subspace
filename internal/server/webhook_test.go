package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	hookSecretGitHub  = "github-hook-secret-for-tests"
	hookSecretForgejo = "forgejo-hook-secret-for-tests"
)

var hookDelivery atomic.Int64

type hookEnv struct {
	*env
	lead, wk string
	prs      int
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	t.Setenv("SUBSPACE_WEBHOOK_SECRET_GITHUB", hookSecretGitHub)
	t.Setenv("SUBSPACE_WEBHOOK_SECRET_FORGEJO", hookSecretForgejo)
	e := newEnv(t)
	return &hookEnv{env: e, lead: e.register("rex", "leader", "other"), wk: e.register("claude-1", "worker", "claude-code")}
}

func (h *hookEnv) brief(autoDone bool) string {
	h.t.Helper()
	_, b := h.rest("POST", "/api/v1/briefs", map[string]any{"title": "Hooks", "cross_review": false})
	id := b["id"].(string)
	if autoDone {
		if code, out := h.rest("PATCH", "/api/v1/briefs/"+id, map[string]any{"auto_done_on_merge": true}); code != 200 || out["auto_done_on_merge"] != true {
			h.t.Fatalf("patch: %d %v", code, out)
		}
	}
	return id
}

func (h *hookEnv) submitted(briefID string) (taskID, prURL string, number int) {
	h.t.Helper()
	h.prs++
	number = 100 + h.prs
	prURL = fmt.Sprintf("https://github.com/o/r/pull/%d", number)
	plan := h.must(h.lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": []any{
		map[string]any{"title": "Work", "spec": "do it", "repo": "o/r"},
	}})["plan"].(map[string]any)
	taskID = plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	if code, out := h.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		h.t.Fatalf("approve: %d %v", code, out)
	}
	h.must(h.lead, "assign", map[string]any{"task_id": taskID, "agent_id": "claude-1"})
	h.must(h.wk, "claim_task", map[string]any{"task_id": taskID})
	h.must(h.wk, "submit", map[string]any{"task_id": taskID, "pr_url": prURL, "summary": "done"})
	return taskID, prURL, number
}

func (h *hookEnv) taskStatus(id string) string {
	h.t.Helper()
	_, out := h.rest("GET", "/api/v1/tasks/"+id, nil)
	return out["status"].(string)
}

func (h *hookEnv) thread(id string) []map[string]any {
	h.t.Helper()
	_, out := h.rest("GET", "/api/v1/threads/task/"+id+"/messages", nil)
	var ms []map[string]any
	for _, m := range out["messages"].([]any) {
		ms = append(ms, m.(map[string]any))
	}
	return ms
}

func (h *hookEnv) hookMessages(id string) []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, m := range h.thread(id) {
		if m["author_id"] == "system" && strings.Contains(m["body"].(string), "ull request") {
			out = append(out, m)
		}
	}
	return out
}

func prPayload(action string, merged bool, number int, url string) []byte {
	b, _ := json.Marshal(map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"number": number, "html_url": url, "merged": merged,
			"head": map[string]any{"ref": "sub/t-1-work"},
		},
		"repository": map[string]any{"full_name": "o/r"},
	})
	return b
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

type hookReq struct {
	path     string
	body     []byte
	headers  map[string]string
	delivery string
}

func githubReq(body []byte) hookReq {
	id := fmt.Sprintf("gh-%d", hookDelivery.Add(1))
	return hookReq{"/hooks/github", body, map[string]string{
		"X-Hub-Signature-256": "sha256=" + hmacHex(hookSecretGitHub, body),
		"X-GitHub-Event":      "pull_request",
		"X-GitHub-Delivery":   id,
	}, id}
}

func forgejoReq(body []byte) hookReq {
	id := fmt.Sprintf("fj-%d", hookDelivery.Add(1))
	return hookReq{"/hooks/forgejo", body, map[string]string{
		"X-Forgejo-Signature": hmacHex(hookSecretForgejo, body),
		"X-Forgejo-Event":     "pull_request",
		"X-Forgejo-Delivery":  id,
	}, id}
}

func (h *hookEnv) send(r hookReq) int {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.ts.URL+r.path, bytes.NewReader(r.body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestWebhookPullRequestLifecycle(t *testing.T) {
	h := newHookEnv(t)

	t.Run("opened and updated post notes", func(t *testing.T) {
		id, url, n := h.submitted(h.brief(false))
		for _, action := range []string{"opened", "synchronize"} {
			if code := h.send(githubReq(prPayload(action, false, n, url))); code != 200 {
				t.Fatalf("%s: status %d", action, code)
			}
		}
		ms := h.hookMessages(id)
		if len(ms) != 2 || ms[0]["kind"] != "note" || !strings.Contains(ms[0]["body"].(string), url) {
			t.Fatalf("messages: %v", ms)
		}
		if s := h.taskStatus(id); s != "approved" {
			t.Fatalf("status %s", s)
		}
	})

	t.Run("merged without auto done only notes", func(t *testing.T) {
		id, url, n := h.submitted(h.brief(false))
		if code := h.send(githubReq(prPayload("closed", true, n, url))); code != 200 {
			t.Fatalf("status %d", code)
		}
		if s := h.taskStatus(id); s != "approved" {
			t.Fatalf("status %s, want approved", s)
		}
		if ms := h.hookMessages(id); len(ms) != 1 || !strings.Contains(ms[0]["body"].(string), "merged") {
			t.Fatalf("messages: %v", ms)
		}
	})

	t.Run("merged with auto done marks done", func(t *testing.T) {
		id, url, n := h.submitted(h.brief(true))
		if code := h.send(githubReq(prPayload("closed", true, n, url))); code != 200 {
			t.Fatalf("status %d", code)
		}
		if s := h.taskStatus(id); s != "done" {
			t.Fatalf("status %s, want done", s)
		}
	})

	t.Run("forgejo merged with auto done marks done", func(t *testing.T) {
		id, url, n := h.submitted(h.brief(true))
		if code := h.send(forgejoReq(prPayload("closed", true, n, url))); code != 200 {
			t.Fatalf("status %d", code)
		}
		if s := h.taskStatus(id); s != "done" {
			t.Fatalf("status %s, want done", s)
		}
	})

	t.Run("merged task not yet approved is not forced done", func(t *testing.T) {
		b := h.brief(true)
		h.rest("PATCH", "/api/v1/briefs/"+b, map[string]any{"cross_review": true})
		id, url, n := h.submitted(b)
		if code := h.send(githubReq(prPayload("closed", true, n, url))); code != 200 {
			t.Fatalf("status %d", code)
		}
		if s := h.taskStatus(id); s != "submitted" {
			t.Fatalf("status %s, want submitted", s)
		}
		if ms := h.hookMessages(id); len(ms) != 1 || !strings.Contains(ms[0]["body"].(string), "not marked done") {
			t.Fatalf("messages: %v", ms)
		}
	})

	t.Run("closed unmerged posts warning", func(t *testing.T) {
		id, url, n := h.submitted(h.brief(true))
		if code := h.send(githubReq(prPayload("closed", false, n, url))); code != 200 {
			t.Fatalf("status %d", code)
		}
		ms := h.hookMessages(id)
		if len(ms) != 1 || ms[0]["kind"] != "system" || !strings.Contains(ms[0]["body"].(string), "Warning") {
			t.Fatalf("messages: %v", ms)
		}
		if s := h.taskStatus(id); s != "approved" {
			t.Fatalf("status %s", s)
		}
	})

	t.Run("unknown pr is accepted and changes nothing", func(t *testing.T) {
		id, _, _ := h.submitted(h.brief(true))
		if code := h.send(githubReq(prPayload("closed", true, 999, "https://github.com/o/r/pull/999"))); code != 200 {
			t.Fatalf("status %d", code)
		}
		if s := h.taskStatus(id); s != "approved" || len(h.hookMessages(id)) != 0 {
			t.Fatalf("task touched: %s %v", s, h.thread(id))
		}
	})
}

func TestWebhookReplayAndAuth(t *testing.T) {
	h := newHookEnv(t)
	id, url, n := h.submitted(h.brief(false))
	body := prPayload("opened", false, n, url)

	req := githubReq(body)
	if code := h.send(req); code != 200 {
		t.Fatalf("first: %d", code)
	}
	if code := h.send(req); code != 202 {
		t.Fatalf("replay: %d, want 202", code)
	}
	if got := len(h.hookMessages(id)); got != 1 {
		t.Fatalf("replay produced %d messages", got)
	}

	bad := map[string]func() hookReq{
		"wrong secret": func() hookReq {
			r := githubReq(body)
			r.headers["X-Hub-Signature-256"] = "sha256=" + hmacHex("other", body)
			return r
		},
		"missing signature": func() hookReq {
			r := githubReq(body)
			delete(r.headers, "X-Hub-Signature-256")
			return r
		},
		"tampered body": func() hookReq {
			r := githubReq(body)
			r.body = prPayload("opened", true, n, url)
			return r
		},
		"forgejo headers on github path": func() hookReq {
			r := forgejoReq(body)
			r.path = "/hooks/github"
			r.headers["X-Forgejo-Signature"] = hmacHex(hookSecretGitHub, body)
			return r
		},
		"github secret on forgejo path": func() hookReq {
			r := forgejoReq(body)
			r.headers["X-Forgejo-Signature"] = hmacHex(hookSecretGitHub, body)
			return r
		},
	}
	for name, mk := range bad {
		t.Run(name, func(t *testing.T) {
			if code := h.send(mk()); code != 401 {
				t.Fatalf("status %d, want 401", code)
			}
		})
	}
	if got := len(h.hookMessages(id)); got != 1 {
		t.Fatalf("rejected requests produced messages: %d", got)
	}

	t.Run("oversized body", func(t *testing.T) {
		big := bytes.Repeat([]byte("a"), 2<<20)
		if code := h.send(githubReq(big)); code != 413 {
			t.Fatalf("status %d, want 413", code)
		}
	})

	t.Run("no bearer needed but GET is rejected", func(t *testing.T) {
		resp, err := http.Get(h.ts.URL + "/hooks/github")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Fatalf("status %d, want 405", resp.StatusCode)
		}
	})
}

func TestWebhookNotMountedWithoutSecret(t *testing.T) {
	t.Setenv("SUBSPACE_WEBHOOK_SECRET_GITHUB", hookSecretGitHub)
	t.Setenv("SUBSPACE_WEBHOOK_SECRET_FORGEJO", "")
	h := &hookEnv{env: newEnv(t)}
	body := prPayload("opened", false, 1, "https://git.example/o/r/pulls/1")
	if code := h.send(forgejoReq(body)); code != 404 {
		t.Fatalf("forgejo: %d, want 404", code)
	}
	if code := h.send(githubReq(body)); code != 200 {
		t.Fatalf("github: %d, want 200", code)
	}

	t.Setenv("SUBSPACE_WEBHOOK_SECRET_GITHUB", "")
	h = &hookEnv{env: newEnv(t)}
	if code := h.send(githubReq(body)); code != 404 {
		t.Fatalf("github unset: %d, want 404", code)
	}
}

func TestWebhookIgnoresPullRequestFromAnotherRepo(t *testing.T) {
	h := newHookEnv(t)
	bid := h.brief(true)
	plan := h.must(h.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "Work", "spec": "do it", "repo": "o/r"},
	}})["plan"].(map[string]any)
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	if code, out := h.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	foreign := "https://github.com/o/x/pull/7"
	h.must(h.lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	h.must(h.wk, "claim_task", map[string]any{"task_id": id})
	h.must(h.wk, "submit", map[string]any{"task_id": id, "pr_url": foreign, "summary": "done"})
	body, _ := json.Marshal(map[string]any{
		"action": "closed", "number": 7,
		"pull_request": map[string]any{"number": 7, "html_url": foreign, "merged": true, "head": map[string]any{"ref": "feature"}},
		"repository":   map[string]any{"full_name": "o/x"},
	})
	if code := h.send(githubReq(body)); code != 200 {
		t.Fatalf("status %d", code)
	}
	if s := h.taskStatus(id); s != "approved" {
		t.Fatalf("task in o/r became %s after a merge in o/x", s)
	}
	if ms := h.hookMessages(id); len(ms) != 0 {
		t.Fatalf("messages: %v", ms)
	}
}
