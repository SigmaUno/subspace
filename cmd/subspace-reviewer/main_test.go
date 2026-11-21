package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

const repo = "o/r"

type recorder struct {
	mu    sync.Mutex
	calls []toolCall
}

type toolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"arguments"`
}

func (r *recorder) named(name string) []toolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []toolCall
	for _, c := range r.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

type fakePR struct {
	repo, title, body, state, branch, diff string
}

type fakeForge struct {
	mu  sync.Mutex
	prs map[string]fakePR
}

func (f *fakeForge) set(key string, pr fakePR) {
	f.mu.Lock()
	f.prs[key] = pr
	f.mu.Unlock()
}

func (f *fakeForge) get(key string) (fakePR, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, ok := f.prs[key]
	return pr, ok
}

type policy func(in promptInput, call int) string

type harness struct {
	t      *testing.T
	env    *servertest.Env
	mcp    *httptest.Server
	forge  *httptest.Server
	gh     *httptest.Server
	llm    *httptest.Server
	rec    *recorder
	prs    *fakeForge
	lead   string
	worker string
	tester string
	brief  string

	mu       sync.Mutex
	policy   policy
	llmCalls map[string]int
	systems  []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, env: servertest.New(t), rec: &recorder{}, prs: &fakeForge{prs: map[string]fakePR{}}, llmCalls: map[string]int{}}
	inner := h.env.Srv.Handler()
	h.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string   `json:"method"`
			Params toolCall `json:"params"`
		}
		if json.Unmarshal(body, &rpc) == nil && rpc.Method == "tools/call" {
			h.rec.mu.Lock()
			h.rec.calls = append(h.rec.calls, rpc.Params)
			h.rec.mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(h.mcp.Close)
	h.forge = httptest.NewServer(http.HandlerFunc(h.serveForgejo))
	t.Cleanup(h.forge.Close)
	h.gh = httptest.NewServer(http.HandlerFunc(h.serveGitHub))
	t.Cleanup(h.gh.Close)
	h.llm = httptest.NewServer(http.HandlerFunc(h.serveLLM))
	t.Cleanup(h.llm.Close)

	h.lead = h.env.Register("rex", "leader", "other")
	h.worker = h.env.Register("claude-1", "worker", "claude-code")
	h.tester = h.env.Register("tess", "tester", "claude-code")
	code, br := h.env.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b", "repos": []string{repo, "o/other"}, "max_cost_eur": 100, "max_parallel_agents": 10})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, br)
	}
	h.brief = br["id"].(string)
	return h
}

func (h *harness) reviewerToken(id, kind string) string {
	return h.env.Register(id, "reviewer", kind)
}

func (h *harness) serveForgejo(w http.ResponseWriter, r *http.Request) {
	path, isDiff := strings.CutSuffix(r.URL.Path, ".diff")
	if r.Header.Get("Authorization") != "token git-read-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pr, ok := h.prs.get(strings.TrimPrefix(path, "/api/v1/repos/"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.writePR(w, pr, isDiff)
}

func (h *harness) serveGitHub(w http.ResponseWriter, r *http.Request) {
	pr, ok := h.prs.get(strings.TrimPrefix(r.URL.Path, "/repos/"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.writePR(w, pr, r.Header.Get("Accept") == "application/vnd.github.diff")
}

func (h *harness) writePR(w http.ResponseWriter, pr fakePR, diff bool) {
	if diff {
		_, _ = io.WriteString(w, pr.diff)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"number": 1, "title": pr.title, "body": pr.body, "state": pr.state, "merged": false,
		"html_url": "u", "head": map[string]any{"ref": pr.branch}, "base": map[string]any{"ref": "main"},
	})
}

func (h *harness) serveLLM(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer llm-key" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) < 2 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var in promptInput
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &in); err != nil {
		http.Error(w, "user message is not json", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.systems = append(h.systems, req.Messages[0].Content)
	call := h.llmCalls[in.Title]
	h.llmCalls[in.Title] = call + 1
	text := h.policy(in, call)
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": fmt.Sprintf("chat-%s-%d", in.TaskID, call), "model": "gpt-test",
		"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 50},
	})
}

func (h *harness) calls(title string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.llmCalls[title]
}

type taskSpec struct {
	title    string
	criteria []string
	role     string
}

func (h *harness) submitTasks(prRepo string, specs ...taskSpec) []string {
	h.t.Helper()
	var tasks []any
	for _, s := range specs {
		role := s.role
		if role == "" {
			role = "worker"
		}
		tasks = append(tasks, map[string]any{"title": s.title, "spec": "spec of " + s.title, "repo": repo, "role": role, "acceptance_criteria": s.criteria})
	}
	plan := h.env.Must(h.lead, "propose_plan", map[string]any{"brief_id": h.brief, "tasks": tasks})["plan"].(map[string]any)
	if code, out := h.env.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		h.t.Fatalf("approve: %d %v", code, out)
	}
	var ids []string
	for i, x := range plan["tasks"].([]any) {
		task := x.(map[string]any)
		id, branch := task["id"].(string), task["branch"].(string)
		ids = append(ids, id)
		tok, agent := h.worker, "claude-1"
		if specs[i].role == "tester" {
			tok, agent = h.tester, "tess"
		}
		h.env.Must(h.lead, "assign", map[string]any{"task_id": id, "agent_id": agent})
		h.env.Must(tok, "claim_task", map[string]any{"task_id": id})
		if specs[i].role == "tester" {
			h.env.Must(tok, "report_tests", map[string]any{"task_id": id, "passed": 3, "failed": 1})
		}
		n := strings.TrimPrefix(id, "T-")
		h.prs.set(prRepo+"/pulls/"+n, fakePR{repo: prRepo, title: specs[i].title, body: "implements " + specs[i].title, state: "open", branch: branch,
			diff: "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new " + specs[i].title + "\n"})
		h.env.Must(tok, "submit", map[string]any{"task_id": id, "pr_url": h.forge.URL + "/" + prRepo + "/pulls/" + n, "summary": "done"})
	}
	return ids
}

func (h *harness) status(id string) map[string]any {
	h.t.Helper()
	code, task := h.env.REST("GET", "/api/v1/tasks/"+id, nil)
	if code != 200 {
		h.t.Fatalf("get %s: %d %v", id, code, task)
	}
	return task
}

func (h *harness) run(token string, extra ...string) (int, string) {
	h.t.Helper()
	env := map[string]string{"SUBSPACE_TOKEN": token, "SUBSPACE_REVIEWER_API_KEY": "llm-key", "SUBSPACE_REVIEWER_GIT_TOKEN": "git-read-token"}
	args := append([]string{"-url", h.mcp.URL, "-provider", "openai", "-model", "gpt-test", "-llm-url", h.llm.URL,
		"-githost", "forgejo", "-githost-url", h.forge.URL, "-once"}, extra...)
	var stderr bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, &stderr)
	return code, stderr.String()
}

func respond(verdict string, comments []string, results ...criterionResult) string {
	b, _ := json.Marshal(output{Verdict: verdict, Comments: comments, PerCriterion: results})
	return string(b)
}

func allMet(in promptInput) []criterionResult {
	var out []criterionResult
	for _, c := range in.AcceptanceCriteria {
		met := true
		out = append(out, criterionResult{Criterion: c, Met: &met, Evidence: "x.go: +new"})
	}
	return out
}

func TestReviewPass(t *testing.T) {
	h := newHarness(t)
	rev := h.reviewerToken("codex-r", "codex")
	ids := h.submitTasks(repo,
		taskSpec{title: "good", criteria: []string{"A1", "A2"}},
		taskSpec{title: "half", criteria: []string{"B1", "B2"}},
		taskSpec{title: "tests red", criteria: []string{"C1"}, role: "tester"},
		taskSpec{title: "retry", criteria: []string{"D1"}},
		taskSpec{title: "no criteria"},
	)
	foreign := h.submitTasks("o/other", taskSpec{title: "foreign", criteria: []string{"E1"}})
	h.policy = func(in promptInput, call int) string {
		res := allMet(in)
		switch in.Title {
		case "half":
			unmet := false
			res[1].Met, res[1].Evidence = &unmet, "no change for B2"
		case "retry":
			if call == 0 {
				return "Sure! Here is my review: APPROVED"
			}
		}
		return respond(verdictApprove, []string{"looks fine"}, res...)
	}

	code, logs := h.run(rev)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, logs)
	}
	want := map[string]string{ids[0]: "approved", ids[1]: "changes_requested", ids[2]: "changes_requested", ids[3]: "approved", ids[4]: "changes_requested", foreign[0]: "changes_requested"}
	for id, st := range want {
		if got := h.status(id)["status"]; got != st {
			t.Errorf("%s: status %v want %s", id, got, st)
		}
	}
	if h.calls("foreign") != 0 || h.calls("retry") != 2 || h.calls("good") != 1 {
		t.Errorf("llm calls foreign=%d retry=%d good=%d", h.calls("foreign"), h.calls("retry"), h.calls("good"))
	}
	for _, s := range h.systems {
		if s != systemPrompt {
			t.Fatalf("system prompt changed: %q", s)
		}
	}

	reviews := map[string]string{}
	for _, c := range h.rec.named("review") {
		reviews[c.Args["task_id"].(string)] = c.Args["comments"].(string)
	}
	checks := map[string]string{
		ids[1]:     "Criterion not met: B2",
		ids[2]:     "Tests failed: 1 failed, 3 passed.",
		ids[4]:     "no acceptance criteria",
		foreign[0]: `not in the task repo "o/r"`,
	}
	for id, sub := range checks {
		if !strings.Contains(reviews[id], sub) {
			t.Errorf("%s comments %q missing %q", id, reviews[id], sub)
		}
	}

	usage := h.rec.named("report_usage")
	if len(usage) != 6 {
		t.Fatalf("report_usage calls: %d", len(usage))
	}
	for _, u := range usage {
		if u.Args["provider"] != "openai" || u.Args["model"] != "gpt-test" || u.Args["tokens_in"] != float64(1000) ||
			u.Args["tokens_out"] != float64(50) || u.Args["brief_id"] != h.brief || u.Args["task_id"] == "" {
			t.Errorf("usage %v", u.Args)
		}
	}
	if strings.Contains(logs, "llm-key") || strings.Contains(logs, "git-read-token") || strings.Contains(logs, rev) {
		t.Errorf("secret in logs: %s", logs)
	}
}

func TestRefusesSameProviderAsRegistration(t *testing.T) {
	h := newHarness(t)
	rev := h.reviewerToken("claude-r", "claude-code")
	ids := h.submitTasks(repo, taskSpec{title: "t", criteria: []string{"A"}})
	h.policy = func(in promptInput, _ int) string { return respond(verdictApprove, []string{}, allMet(in)...) }

	code, logs := h.run(rev)
	if code != exitRefused || !strings.Contains(logs, "differs from the reviewer agent's registered provider") {
		t.Fatalf("exit %d: %s", code, logs)
	}
	if h.calls("t") != 0 || len(h.rec.named("review")) != 0 || h.status(ids[0])["status"] != "submitted" {
		t.Fatal("reviewed despite refusal")
	}
}

func TestMaxRoundsRespected(t *testing.T) {
	h := newHarness(t)
	rev := h.reviewerToken("codex-r", "codex")
	ids := h.submitTasks(repo, taskSpec{title: "t", criteria: []string{"A"}})
	h.policy = func(in promptInput, _ int) string {
		unmet := false
		return respond(verdictChanges, []string{"missing A"}, criterionResult{Criterion: "A", Met: &unmet})
	}
	if code, logs := h.run(rev, "-max-rounds", "1"); code != exitOK || h.status(ids[0])["status"] != "changes_requested" {
		t.Fatalf("first round: %d %s", code, logs)
	}
	h.env.Must(h.worker, "claim_task", map[string]any{"task_id": ids[0]})
	h.env.Must(h.worker, "submit", map[string]any{"task_id": ids[0], "pr_url": h.forge.URL + "/o/r/pulls/1", "summary": "again"})

	code, logs := h.run(rev, "-max-rounds", "1")
	if code != exitOK || !strings.Contains(logs, "reached the maximum of 1") {
		t.Fatalf("exit %d: %s", code, logs)
	}
	task := h.status(ids[0])
	if task["status"] != "submitted" || task["review_rounds"] != float64(1) || h.calls("t") != 1 {
		t.Fatalf("task %v calls %d", task, h.calls("t"))
	}
}

func TestFakeModelWithGitHub(t *testing.T) {
	h := newHarness(t)
	rev := h.reviewerToken("codex-r", "codex")
	ids := h.submitTasks(repo, taskSpec{title: "gh", criteria: []string{"A"}})
	env := map[string]string{"SUBSPACE_TOKEN": rev}
	args := []string{"-url", h.mcp.URL, "-fake-model", "approve", "-githost", "github", "-githost-url", h.gh.URL, "-once"}
	var stderr bytes.Buffer
	if code := run(context.Background(), args, func(k string) string { return env[k] }, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if st := h.status(ids[0])["status"]; st != "approved" {
		t.Fatalf("status %v: %s", st, stderr.String())
	}
	usage := h.rec.named("report_usage")
	if len(usage) != 1 || usage[0].Args["model"] != "fake" || usage[0].Args["provider"] != "openai" {
		t.Fatalf("usage %v", usage)
	}
}
