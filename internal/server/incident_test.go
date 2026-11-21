package server_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type incidentFleet struct{ lead, w1, w2, rev string }

func incidentEnv(t *testing.T, dataDir string) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{
		PublicURL: "http://sub.test", DBPath: testdb.Path(t, "t.db"), DataDir: dataDir,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
}

func incidentSetup(t *testing.T, e *servertest.Env) incidentFleet {
	t.Helper()
	return incidentFleet{
		lead: e.Register("rex", "leader", "other"),
		w1:   e.Register("cc-1", "worker", "claude-code"),
		w2:   e.Register("cx-1", "worker", "codex"),
		rev:  e.Register("rev-1", "reviewer", "codex"),
	}
}

func incidentMCPWorks(e *servertest.Env, tok string) bool {
	e.T.Helper()
	return bulkMCPStatus(e.T, e, tok) == 200
}

func incidentAgentStatus(t *testing.T, e *servertest.Env, id string) string {
	t.Helper()
	_, out := e.REST("GET", "/api/v1/agents/"+id, nil)
	return out["status"].(string)
}

func incidentPaused(t *testing.T, e *servertest.Env) bool {
	t.Helper()
	_, st := e.REST("GET", "/api/v1/state", nil)
	return st["paused"] == true
}

func incidentResult(t *testing.T, out map[string]any, action string) map[string]any {
	t.Helper()
	for _, r := range out["results"].([]any) {
		if m := r.(map[string]any); m["action"] == action {
			return m
		}
	}
	t.Fatalf("no result for %s in %v", action, out["results"])
	return nil
}

func incidentAuditVerifies(t *testing.T, e *servertest.Env) {
	t.Helper()
	code, v := e.REST("GET", "/api/v1/audit/verify", nil)
	if code != 200 || v["ok"] != true {
		t.Fatalf("audit chain: %d %v", code, v)
	}
}

func incidentAuditCounts(t *testing.T, e *servertest.Env) map[string]int {
	t.Helper()
	_, out := e.REST("GET", "/api/v1/audit?limit=1000", nil)
	tools := map[string]int{}
	for _, ev := range out["events"].([]any) {
		tools[ev.(map[string]any)["tool"].(string)]++
	}
	return tools
}

func TestIncidentPauseRotateSnapshotNotify(t *testing.T) {
	dir := t.TempDir()
	e := incidentEnv(t, dir)
	f := incidentSetup(t, e)
	sub := e.Srv.Hub.Subscribe([]string{"alerts"})
	defer sub.Close()

	code, out := e.REST("POST", "/api/v1/incident", map[string]any{
		"reason": "suspicious pushes", "actions": []string{"notify", "snapshot", "rotate_leader_tokens", "pause_all"},
	})
	if code != 201 || out["id"] != "INC-1" || out["failed"] != 0.0 || out["succeeded"] != 4.0 || out["ended_at"] != nil || out["actor"] != "root" {
		t.Fatalf("start: %d %v", code, out)
	}
	if got := out["actions"].([]any); got[0] != "pause_all" || got[3] != "notify" {
		t.Fatalf("actions not in execution order: %v", got)
	}
	if !incidentPaused(t, e) {
		t.Fatal("pause_all did not pause")
	}

	tokens := out["tokens"].([]any)
	if len(tokens) != 1 || tokens[0].(map[string]any)["agent_id"] != "rex" {
		t.Fatalf("tokens: %v", tokens)
	}
	fresh := tokens[0].(map[string]any)["token"].(string)
	if incidentMCPWorks(e, f.lead) || !incidentMCPWorks(e, fresh) {
		t.Fatal("old leader token must be dead and the new one must work")
	}
	if !incidentMCPWorks(e, f.w1) {
		t.Fatal("workers keep their tokens")
	}

	path := incidentResult(t, out, "snapshot")["detail"].(string)
	if path != filepath.Join(dir, "incidents", "INC-1.ndjson") {
		t.Fatalf("snapshot path %s", path)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	kinds := map[string]int{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l struct {
			Kind string
			Data map[string]any
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		kinds[l.Kind]++
		if l.Kind == "meta" && (l.Data["incident"] != "INC-1" || l.Data["paused"] != true) {
			t.Fatalf("meta: %v", l.Data)
		}
		if _, leaked := l.Data["TokenHash"]; leaked {
			t.Fatal("snapshot leaks token hashes")
		}
	}
	if kinds["meta"] != 1 || kinds["agent"] != 4 {
		t.Fatalf("snapshot lines: %v", kinds)
	}

	select {
	case ev := <-sub.C():
		if ev.Type != "alert.raised" {
			t.Fatalf("event %s", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification published")
	}

	tools := incidentAuditCounts(t, e)
	for _, want := range []string{"incident_start", "pause_all", "rotate_token"} {
		if tools[want] == 0 {
			t.Errorf("audit lacks %s: %v", want, tools)
		}
	}
	incidentAuditVerifies(t, e)
}

func TestIncidentRevokeByScope(t *testing.T) {
	e := incidentEnv(t, t.TempDir())
	f := incidentSetup(t, e)

	code, out := e.REST("POST", "/api/v1/incident", map[string]any{
		"reason": "codex key leaked", "actions": []string{"revoke_agents"}, "revoke": map[string]any{"provider": "openai", "role": "worker"},
	})
	if code != 201 || out["failed"] != 0.0 {
		t.Fatalf("start: %d %v", code, out)
	}
	if got := incidentResult(t, out, "revoke_agents")["agents"].([]any); len(got) != 1 || got[0] != "cx-1" {
		t.Fatalf("revoked: %v", got)
	}
	for id, want := range map[string]string{"cx-1": "revoked", "cc-1": "active", "rev-1": "active", "rex": "active"} {
		if got := incidentAgentStatus(t, e, id); got != want {
			t.Errorf("%s is %s, want %s", id, got, want)
		}
	}
	if incidentMCPWorks(e, f.w2) || !incidentMCPWorks(e, f.w1) {
		t.Fatal("only the codex worker loses its token")
	}
	if incidentPaused(t, e) {
		t.Fatal("revoking must not pause")
	}

	code, out = e.REST("POST", "/api/v1/incident", map[string]any{"reason": "x", "actions": []string{"revoke_agents"}})
	if code != 200 || out["id"] != "INC-1" {
		t.Fatalf("widen: %d %v", code, out)
	}
	got := incidentResult(t, out, "revoke_agents")["agents"].([]any)
	if len(got) != 4 {
		t.Fatalf("revoked ids accumulate across calls: %v", got)
	}
	for _, id := range []string{"rex", "cc-1", "rev-1"} {
		if got := incidentAgentStatus(t, e, id); got != "revoked" {
			t.Errorf("%s is %s", id, got)
		}
	}
	incidentAuditVerifies(t, e)
}

func TestIncidentIsIdempotent(t *testing.T) {
	e := incidentEnv(t, t.TempDir())
	incidentSetup(t, e)
	body := map[string]any{"reason": "first", "actions": []string{"pause_all", "rotate_leader_tokens", "snapshot"}}

	code, first := e.REST("POST", "/api/v1/incident", body)
	if code != 201 || len(first["tokens"].([]any)) != 1 {
		t.Fatalf("first: %d %v", code, first)
	}
	body["reason"] = "second"
	code, again := e.REST("POST", "/api/v1/incident", body)
	if code != 200 || again["id"] != first["id"] || again["reason"] != "first" {
		t.Fatalf("again: %d %v", code, again)
	}
	if again["tokens"] != nil {
		t.Fatalf("tokens must be shown once: %v", again["tokens"])
	}
	if skipped := again["skipped"].([]any); len(skipped) != 3 {
		t.Fatalf("skipped: %v", skipped)
	}

	body["actions"] = []string{"notify"}
	code, more := e.REST("POST", "/api/v1/incident", body)
	if code != 200 || more["id"] != first["id"] || more["succeeded"] != 4.0 || len(more["actions"].([]any)) != 4 {
		t.Fatalf("joining: %d %v", code, more)
	}

	_, list := e.REST("GET", "/api/v1/incident", nil)
	if n := len(list["incidents"].([]any)); n != 1 {
		t.Fatalf("incidents: %d", n)
	}
	if n := incidentAuditCounts(t, e)["incident_start"]; n != 1 {
		t.Fatalf("incident_start audited %d times", n)
	}
	if n := incidentAuditCounts(t, e)["rotate_token"]; n != 1 {
		t.Fatalf("rotate_token ran %d times", n)
	}
	incidentAuditVerifies(t, e)
}

func TestIncidentPartialFailureIsReported(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e := incidentEnv(t, blocker)
	incidentSetup(t, e)

	code, out := e.REST("POST", "/api/v1/incident", map[string]any{
		"reason": "disk trouble", "actions": []string{"pause_all", "snapshot", "notify"},
	})
	if code != 201 || out["succeeded"] != 2.0 || out["failed"] != 1.0 {
		t.Fatalf("start: %d %v", code, out)
	}
	if r := incidentResult(t, out, "snapshot"); r["status"] != "failed" || r["error"] == "" {
		t.Fatalf("snapshot: %v", r)
	}
	if r := incidentResult(t, out, "pause_all"); r["status"] != "ok" {
		t.Fatalf("pause_all: %v", r)
	}
	if !incidentPaused(t, e) {
		t.Fatal("the other actions still ran")
	}

	_, list := e.REST("GET", "/api/v1/incident", nil)
	rec := list["incidents"].([]any)[0].(map[string]any)
	if r := incidentResult(t, rec, "snapshot"); r["status"] != "failed" {
		t.Fatalf("failure not persisted: %v", rec)
	}

	code, retry := e.REST("POST", "/api/v1/incident", map[string]any{"reason": "r", "actions": []string{"pause_all", "snapshot", "notify"}})
	if code != 200 || retry["failed"] != 1.0 || len(retry["skipped"].([]any)) != 2 {
		t.Fatalf("retry reruns only the failed action: %d %v", code, retry)
	}
	incidentAuditVerifies(t, e)
}

func TestIncidentEndResumes(t *testing.T) {
	e := incidentEnv(t, t.TempDir())
	incidentSetup(t, e)
	e.REST("POST", "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"cc-1"}, "action": "pause"})
	if got := incidentAgentStatus(t, e, "cc-1"); got != "paused" {
		t.Fatalf("setup: %s", got)
	}
	_, out := e.REST("POST", "/api/v1/incident", map[string]any{"reason": "drill", "actions": []string{"pause_all", "revoke_agents"}, "revoke": map[string]any{"role": "reviewer"}})
	id := out["id"].(string)

	code, end := e.REST("POST", "/api/v1/incident/"+id+"/end", map[string]any{})
	if code != 200 || end["resumed_all"] != true || end["ended_at"] == nil || end["resumed_agents"] != nil {
		t.Fatalf("end: %d %v", code, end)
	}
	if incidentPaused(t, e) {
		t.Fatal("end must resume_all")
	}
	if got := incidentAgentStatus(t, e, "cc-1"); got != "paused" {
		t.Fatalf("paused agents stay paused unless asked: %s", got)
	}
	if got := incidentAgentStatus(t, e, "rev-1"); got != "revoked" {
		t.Fatalf("revoked agents stay revoked: %s", got)
	}

	code, again := e.REST("POST", "/api/v1/incident/"+id+"/end", map[string]any{"resume_agents": true})
	if code != 200 || again["ended_at"] != end["ended_at"] || again["resumed_agents"] != nil {
		t.Fatalf("second end must change nothing: %d %v", code, again)
	}
	if got := incidentAgentStatus(t, e, "cc-1"); got != "paused" {
		t.Fatalf("second end touched agents: %s", got)
	}

	_, out = e.REST("POST", "/api/v1/incident", map[string]any{"reason": "again", "actions": []string{"pause_all"}})
	id2 := out["id"].(string)
	if id2 == id {
		t.Fatal("a new incident gets a new id")
	}
	code, end = e.REST("POST", "/api/v1/incident/"+id2+"/end", map[string]any{"resume_agents": true, "note": "all clear"})
	if code != 200 || end["resumed_agents"].([]any)[0] != "cc-1" {
		t.Fatalf("end with resume_agents: %d %v", code, end)
	}
	if got := incidentAgentStatus(t, e, "cc-1"); got != "active" {
		t.Fatalf("agent not re-enabled: %s", got)
	}

	tools := incidentAuditCounts(t, e)
	if tools["incident_start"] != 2 || tools["incident_end"] != 2 || tools["resume_all"] != 2 {
		t.Fatalf("audit: %v", tools)
	}
	incidentAuditVerifies(t, e)
	_, list := e.REST("GET", "/api/v1/incident", nil)
	rows := list["incidents"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["id"] != id2 || rows[1].(map[string]any)["ended_at"] == nil {
		t.Fatalf("list: %v", rows)
	}
}

func TestIncidentRejectsBadRequests(t *testing.T) {
	e := incidentEnv(t, t.TempDir())
	for name, body := range map[string]map[string]any{
		"no reason":      {"actions": []string{"pause_all"}},
		"no actions":     {"reason": "x"},
		"unknown action": {"reason": "x", "actions": []string{"nuke"}},
		"scope without":  {"reason": "x", "actions": []string{"pause_all"}, "revoke": map[string]any{"role": "worker"}},
		"bad role":       {"reason": "x", "actions": []string{"revoke_agents"}, "revoke": map[string]any{"role": "king"}},
		"unknown field":  {"reason": "x", "actions": []string{"pause_all"}, "extra": 1},
	} {
		if code, _ := e.REST("POST", "/api/v1/incident", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if incidentPaused(t, e) {
		t.Fatal("rejected requests must have no effect")
	}
	if code, _ := e.REST("POST", "/api/v1/incident/INC-9/end", map[string]any{}); code != 404 {
		t.Fatalf("unknown incident: %d", code)
	}
	if code, _ := e.REST("GET", "/api/v1/incident?limit=0", nil); code != 400 {
		t.Fatalf("limit: %d", code)
	}
	if code, _ := e.RESTAs("", "POST", "/api/v1/incident", map[string]any{"reason": "x", "actions": []string{"pause_all"}}); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}
