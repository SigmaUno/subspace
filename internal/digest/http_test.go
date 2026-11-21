package digest_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/digest"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	specMarker = "CONFIDENTIAL-SPEC-BODY"
	secret     = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
)

func TestPreviewOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	get := func(path, token string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", e.TS.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	preview := func() digest.Preview {
		t.Helper()
		code, body := get("/api/v1/digest/preview", servertest.OwnerToken)
		if code != 200 {
			t.Fatalf("preview: %d %s", code, body)
		}
		if strings.Contains(string(body), specMarker) || strings.Contains(string(body), secret) {
			t.Fatalf("preview leaks task body or secret: %s", body)
		}
		var p digest.Preview
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := preview()
	if p.Digest.Tasks != (digest.Tasks{}) || p.Digest.Decisions.Count != 0 || len(p.Digest.Risks) != 0 || p.Digest.Active() {
		t.Fatalf("empty server: %+v", p.Digest)
	}
	if p.Digest.Since.IsZero() || !p.Digest.Until.After(p.Digest.Since) {
		t.Fatalf("empty server window: %+v", p.Digest)
	}

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 1.0, "max_parallel_agents": 2})
	bid := br["id"].(string)
	task := func(ref string) map[string]any {
		return map[string]any{"ref": ref, "title": "Task " + ref, "spec": specMarker + " " + ref, "repo": "o/r",
			"acceptance_criteria": []string{"ok"}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 1.0}}
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{task("a"), task("b")}})["plan"].(map[string]any)
	if p = preview(); p.Digest.Decisions.Count != 1 || len(p.Digest.Decisions.Titles) != 1 || !strings.HasPrefix(p.Digest.Decisions.Titles[0], "Approve plan "+plan["id"].(string)) {
		t.Fatalf("proposed plan: %+v", p.Digest.Decisions)
	}

	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve plan")
	}
	tasks := plan["tasks"].([]any)
	t1, t2 := tasks[0].(map[string]any)["id"].(string), tasks[1].(map[string]any)["id"].(string)
	if p = preview(); p.Digest.Tasks.Created != 2 || p.Digest.Decisions.Count != 0 || !p.Digest.Active() {
		t.Fatalf("approved plan: %+v", p.Digest)
	}

	e.Must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": t1})
	e.Must(wk, "update_status", map[string]any{"task_id": t1, "status": "in_progress", "cost_eur": 0.9})
	e.Must(wk, "submit", map[string]any{"task_id": t1, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
	e.Must(rev, "review", map[string]any{"task_id": t1, "verdict": "approve", "comments": "lgtm"})
	if p = preview(); p.Digest.Decisions.Count != 2 || !slices.Contains(p.Digest.Decisions.Titles, "Merge "+t1+": Task a") {
		t.Fatalf("approved task awaits merge: %+v", p.Digest.Decisions)
	}
	if code, _ := e.REST("POST", "/api/v1/tasks/"+t1+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatal("mark done")
	}
	p = preview()
	if p.Digest.Tasks.Done != 1 || p.Digest.CostEUR < 0.89 || p.Digest.CostEUR > 0.91 || p.Digest.Decisions.Count != 1 {
		t.Fatalf("after merge: %+v", p.Digest)
	}
	if len(p.Digest.Risks) == 0 || p.Digest.Risks[0].Source != digest.SourceForecast || p.Digest.Risks[0].Subject != bid {
		t.Fatalf("forecast risk missing: %+v", p.Digest.Risks)
	}

	if code, out := e.REST("PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": 1}); code != 200 {
		t.Fatalf("limits: %d %v", code, out)
	}
	var spend *digest.Risk
	for _, r := range preview().Digest.Risks {
		if r.Source == digest.SourceSpend {
			spend = &r
		}
	}
	if spend == nil || spend.Subject != "daily" || spend.Severity != digest.SeverityMedium {
		t.Fatalf("spend risk: %+v", spend)
	}

	e.Must(lead, "assign", map[string]any{"task_id": t2, "agent_id": "claude-1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": t2})
	e.Must(wk, "ask_owner", map[string]any{"task_id": t2, "question": "May I use " + secret + " for the tests?"})
	e.Must(wk, "update_status", map[string]any{"task_id": t2, "status": "blocked", "note": specMarker})
	p = preview()
	if p.Digest.Tasks.Blocked != 1 || p.Digest.Decisions.Count != 4 {
		t.Fatalf("blocked task and question: %+v", p.Digest)
	}
	if !strings.Contains(p.Text, "Decisions pending: 4") || !strings.Contains(p.Text, "Unblock "+t2) || p.Title != "Subspace digest: 1 done, 4 decisions pending" {
		t.Fatalf("text:\n%s\n%s", p.Title, p.Text)
	}

	for _, bad := range []string{"abc", "0s", "-1h", "800h"} {
		if code, body := get("/api/v1/digest/preview?window="+bad, servertest.OwnerToken); code != 400 {
			t.Fatalf("window %q: %d %s", bad, code, body)
		}
	}
	if code, body := get("/api/v1/digest/preview?window=1h", servertest.OwnerToken); code != 200 {
		t.Fatalf("window 1h: %d %s", code, body)
	}
	if code, _ := get("/api/v1/digest/preview", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := get("/api/v1/digest/preview", wk); code != 403 {
		t.Fatalf("agent token: %d", code)
	}
}
