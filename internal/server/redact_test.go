package server

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAgentTextIsRedactedBeforePersisting(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	tester := e.register("tess", "tester", "other")
	rev := e.register("codex-r", "reviewer", "codex")

	secrets := map[string]string{
		"agent token": "ssp_" + "Zk3Qm9vT1xYb7LwR2nHd5CpA8sGf4JuE0iKoX6tNyBc",
		"bearer":      "q8Zr2LmN5vTx9KbW3cYd7HjF1gPs4AeU",
		"anthropic":   "sk-ant-" + "api03-Qw8Er2Ty6Ui0Op4As9Df3Gh7Jk1Lz5X",
		"github":      "ghp_" + "Qw8Er2Ty6Ui0Op4As9Df3Gh7Jk1Lz5Xc9Vb",
		"aws":         "AKIA" + "QWERTYUIOPASDFGH",
		"pem body":    "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
		"password":    "correct-horse-battery-staple",
	}
	leak := func(tag string) string {
		return tag + " " + strings.Join([]string{
			"token " + secrets["agent token"],
			"Authorization: Bearer " + secrets["bearer"],
			secrets["anthropic"],
			secrets["github"],
			secrets["aws"],
			"-----BEGIN RSA PRIVATE KEY-----\n" + secrets["pem body"] + "\n-----END RSA PRIVATE KEY-----",
			"db_password=" + secrets["password"],
		}, " | ")
	}

	_, brief := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Redact"})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": brief["id"], "tasks": []any{
		map[string]any{"title": "Code", "spec": "do it", "repo": "o/r"},
		map[string]any{"title": "Test", "spec": "test it", "repo": "o/r", "role": "tester"},
	}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tasks := plan["tasks"].([]any)
	work, test := tasks[0].(map[string]any)["id"].(string), tasks[1].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": work, "agent_id": "claude-1"})
	e.must(lead, "assign", map[string]any{"task_id": test, "agent_id": "tess"})
	e.must(wk, "claim_task", map[string]any{"task_id": work})
	e.must(tester, "claim_task", map[string]any{"task_id": test})

	e.must(wk, "post_message", map[string]any{"thread": "task:" + work, "body": leak("post")})
	e.must(wk, "add_note", map[string]any{"task_id": work, "body": leak("note")})
	e.must(wk, "add_note", map[string]any{"body": leak("own note")})
	e.must(wk, "ask_owner", map[string]any{"task_id": work, "question": leak("ask"), "options": []string{leak("opt a"), "plain"}})
	e.must(wk, "update_status", map[string]any{"task_id": work, "status": "in_progress", "progress_pct": 50, "note": leak("status")})
	e.must(tester, "report_tests", map[string]any{"task_id": test, "passed": 3, "failed": 1, "log_excerpt": leak("log")})
	e.must(wk, "submit", map[string]any{"task_id": work, "pr_url": "https://git.example/o/r/pulls/1", "summary": leak("summary")})
	e.must(rev, "review", map[string]any{"task_id": work, "verdict": "changes_requested", "comments": leak("review")})
	e.must(wk, "claim_task", map[string]any{"task_id": work})
	e.must(wk, "update_status", map[string]any{"task_id": work, "status": "blocked", "note": leak("blocked")})

	var seen []byte
	for _, path := range []string{
		"/api/v1/threads/task/" + work + "/messages",
		"/api/v1/threads/task/" + test + "/messages",
		"/api/v1/threads/agent/claude-1/messages",
		"/api/v1/audit?limit=1000",
		"/api/v1/questions",
		"/api/v1/tasks/" + work,
	} {
		code, out := e.rest("GET", path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		b, _ := json.Marshal(out)
		seen = append(seen, b...)
	}
	if bytes.Count(seen, []byte("[redacted:")) < 20 {
		t.Fatalf("expected redaction markers, got:\n%s", seen)
	}
	for name, s := range secrets {
		if bytes.Contains(seen, []byte(s)) {
			t.Errorf("%s leaked through the API", name)
		}
	}

	const control = "owner-brief-control-marker"
	if code, out := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "control", "body": control}); code != 201 {
		t.Fatalf("control brief: %d %v", code, out)
	}
	var raw []byte
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(e.srv.cfg.DBPath + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		raw = append(raw, b...)
	}
	if !bytes.Contains(raw, []byte(control)) || !bytes.Contains(raw, []byte("[redacted:")) {
		t.Fatal("database bytes do not contain the expected control data")
	}
	for name, s := range secrets {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("%s found in the database bytes", name)
		}
	}
}
