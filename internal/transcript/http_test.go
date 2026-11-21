package transcript_test

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type fleet struct {
	e    *servertest.Env
	lead string
	w1   string
	w2   string
	task string
}

func assignedTask(e *servertest.Env, lead string) string {
	e.T.Helper()
	code, b := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "pm", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	if code != 201 {
		e.T.Fatalf("brief: %d %v", code, b)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": b["id"], "tasks": []any{
		map[string]any{"title": "build", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"builds"}},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		e.T.Fatalf("approve: %d %v", code, out)
	}
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	return tid
}

func startFleet(t *testing.T, cfg server.Config) *fleet {
	t.Helper()
	e := servertest.Start(t, cfg)
	f := &fleet{e: e}
	f.lead = e.Register("rex", "leader", "other")
	f.w1 = e.Register("w1", "worker", "claude-code")
	f.w2 = e.Register("w2", "worker", "claude-code")
	f.task = assignedTask(e, f.lead)
	e.Must(f.w1, "claim_task", map[string]any{"task_id": f.task})
	return f
}

func serverConfig(t *testing.T) server.Config {
	t.Helper()
	return server.Config{PublicURL: "http://sub.test", DBPath: servertest.DB(t), DataDir: t.TempDir(),
		FlushEvery: 20 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
}

func texts(page map[string]any) []string {
	var out []string
	for _, l := range page["lines"].([]any) {
		out = append(out, l.(map[string]any)["text"].(string))
	}
	return out
}

func rawGet(e *servertest.Env, tok, path string) (*http.Response, []byte) {
	e.T.Helper()
	req, _ := http.NewRequest("GET", e.TS.URL+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestOwnerReadsAndDownloadsTheTranscript(t *testing.T) {
	f := startFleet(t, serverConfig(t))
	e := f.e
	e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stdout", "text": "compiling\nlinking\nGITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123"})
	e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stderr", "text": "warning: unused import"})
	e.Must(f.w1, "log_output", map[string]any{"stream": "stdout", "text": "not tied to a task"})

	base := "/api/v1/tasks/" + f.task + "/transcript"
	code, out := e.REST("GET", base, nil)
	if code != 200 || len(texts(out)) != 4 || out["first_offset"] != 1.0 || out["last_offset"] != 4.0 || out["more"] != false || out["gap"] != false {
		t.Fatalf("transcript: %d %v", code, out)
	}
	got := texts(out)
	if got[0] != "compiling" || strings.Contains(got[2], "abcdefghij") || got[3] != "warning: unused import" {
		t.Fatalf("lines: %q", got)
	}
	if l := out["lines"].([]any)[3].(map[string]any); l["stream"] != "stderr" || l["agent_id"] != "w1" || l["offset"] != 4.0 {
		t.Fatalf("line fields: %v", l)
	}

	_, out = e.REST("GET", base+"?since=1&limit=2", nil)
	if got := texts(out); len(got) != 2 || got[0] != "linking" || out["next_since"] != 3.0 || out["more"] != true {
		t.Fatalf("paged: %v", out)
	}
	_, out = e.REST("GET", base+"?q=WARNING", nil)
	if got := texts(out); len(got) != 1 || got[0] != "warning: unused import" {
		t.Fatalf("search: %v", out)
	}
	_, out = e.REST("GET", base+"?at="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339), nil)
	if len(texts(out)) != 0 {
		t.Fatalf("jump past the end: %v", out)
	}
	for _, q := range []string{"since=-1", "since=x", "limit=0", "limit=x", "at=yesterday"} {
		if code, out := e.REST("GET", base+"?"+q, nil); code != 400 {
			t.Fatalf("%s: %d %v", q, code, out)
		}
	}
	if code, _ := e.REST("GET", "/api/v1/tasks/T-404/transcript", nil); code != 404 {
		t.Fatalf("unknown task: %d", code)
	}

	resp, body := rawGet(e, servertest.OwnerToken, base+"/download")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/gzip" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), f.task+".ndjson.gz") {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	all := gunzip(t, body)
	if len(all) != 4 || all[0].Text != "compiling" || bytes.Contains(body, []byte("abcdefghij")) {
		t.Fatalf("download content: %+v", all)
	}
}

func TestSubmitFlushesTheTranscriptToDisk(t *testing.T) {
	cfg := serverConfig(t)
	f := startFleet(t, cfg)
	f.e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stdout", "text": "all green"})
	f.e.Must(f.w1, "submit", map[string]any{"task_id": f.task, "pr_url": "https://github.com/o/r/pull/1", "summary": "done"})
	deadline := time.Now().Add(5 * time.Second)
	for len(files(t, filepath.Join(cfg.DataDir, "transcripts"))) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("submit did not flush the transcript")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentsCannotReachTranscripts(t *testing.T) {
	f := startFleet(t, serverConfig(t))
	e := f.e
	e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stdout", "text": "private output"})
	for name, tok := range map[string]string{"assignee": f.w1, "other worker": f.w2, "leader": f.lead, "anonymous": "", "garbage": "ssp_nope"} {
		for _, path := range []string{"", "/download"} {
			resp, body := rawGet(e, tok, "/api/v1/tasks/"+f.task+"/transcript"+path)
			if resp.StatusCode != 401 && resp.StatusCode != 403 || bytes.Contains(body, []byte("private output")) {
				t.Fatalf("%s%s: %d %s", name, path, resp.StatusCode, body)
			}
		}
	}
	for role, names := range e.Srv.ToolNames() {
		for _, n := range names {
			if strings.Contains(n, "transcript") {
				t.Fatalf("role %s can call %s", role, n)
			}
		}
	}
	e.MustFail(f.w2, "get_task", map[string]any{"task_id": f.task}, "not_found")
	if res := e.Must(f.w1, "get_task", map[string]any{"task_id": f.task}); strings.Contains(strings.ToLower(toJSON(res)), "private output") {
		t.Fatalf("task view leaks the transcript: %v", res)
	}
}

func TestTranscriptSurvivesServerRestart(t *testing.T) {
	cfg := serverConfig(t)
	f := startFleet(t, cfg)
	f.e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stdout", "text": "before restart one\nbefore restart two"})
	f.e.Stop()

	if n := len(files(t, filepath.Join(cfg.DataDir, "transcripts"))); n != 1 {
		t.Fatalf("shutdown wrote %d segments", n)
	}
	e := servertest.Start(t, cfg)
	base := "/api/v1/tasks/" + f.task + "/transcript"
	code, out := e.REST("GET", base, nil)
	if code != 200 || len(texts(out)) != 2 || out["last_offset"] != 2.0 {
		t.Fatalf("after restart: %d %v", code, out)
	}
}

func TestReplayTimelineLinksTranscripts(t *testing.T) {
	f := startFleet(t, serverConfig(t))
	e := f.e
	e.Must(f.w1, "log_output", map[string]any{"task_id": f.task, "stream": "stdout", "text": "work"})
	e.Must(f.w1, "update_status", map[string]any{"task_id": f.task, "status": "blocked", "note": "stuck"})
	_, tasks := e.REST("GET", "/api/v1/tasks/"+f.task, nil)
	brief := tasks["brief_id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, tl := e.REST("GET", "/api/v1/briefs/"+brief+"/replay/timeline", nil)
		linked, unlinked := 0, 0
		want := "/api/v1/tasks/" + f.task + "/transcript"
		for _, k := range tl["key_events"].([]any) {
			ev := k.(map[string]any)
			if ev["task_id"] != f.task {
				continue
			}
			if ev["transcript"] == want {
				linked++
			} else if time.Now().After(deadline) {
				t.Fatalf("link: %v", ev)
			} else {
				unlinked++
			}
		}
		if linked > 0 && unlinked == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no linked key event: %v", tl)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
