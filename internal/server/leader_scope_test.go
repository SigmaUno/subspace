package server_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type lsEnv struct {
	*servertest.Env
	rex, max, w1, w2, r1 string
}

func lsNew(t *testing.T) *lsEnv {
	e := &lsEnv{Env: servertest.New(t)}
	e.rex = e.Register("rex", "leader", "other")
	e.max = e.Register("max", "leader", "other")
	e.w1 = e.Register("w1", "worker", "claude-code")
	e.w2 = e.Register("w2", "worker", "claude-code")
	e.r1 = e.Register("r1", "reviewer", "codex")
	return e
}

func (e *lsEnv) want(code int, method, path string, body any) map[string]any {
	e.T.Helper()
	got, out := e.REST(method, path, body)
	if got != code {
		e.T.Fatalf("%s %s: %d want %d (%v)", method, path, got, code, out)
	}
	return out
}

func (e *lsEnv) brief(title, leader string) string {
	e.T.Helper()
	body := map[string]any{"title": title, "repos": []string{"o/r"}}
	if leader != "" {
		body["leader_id"] = leader
	}
	return e.want(201, "POST", "/api/v1/briefs", body)["id"].(string)
}

func lsTask(title string) map[string]any {
	return map[string]any{"ref": title, "title": title, "spec": "do " + title, "repo": "o/r"}
}

func (e *lsEnv) approved(lead, bid string, titles ...string) []string {
	e.T.Helper()
	tasks := make([]any, len(titles))
	for i, title := range titles {
		tasks[i] = lsTask(title)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": tasks})["plan"].(map[string]any)
	e.want(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	var ids []string
	for _, x := range plan["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	return ids
}

func (e *lsEnv) fail(tok, tool string, args map[string]any) (string, string) {
	e.T.Helper()
	out, isErr := e.Call(tok, tool, args)
	if !isErr {
		e.T.Fatalf("%s %v should fail, got %v", tool, args, out)
	}
	er, _ := out["error"].(map[string]any)
	code, _ := er["code"].(string)
	msg, _ := er["message"].(string)
	return code, msg
}

var lsIDs = regexp.MustCompile(`\b(B|T|P|HR|A|Q)-\d+\b`)

func (e *lsEnv) sameAsMissing(tok, tool string, foreign, missing map[string]any, code string) {
	e.T.Helper()
	fc, fm := e.fail(tok, tool, foreign)
	mc, mm := e.fail(tok, tool, missing)
	if fc != code || mc != code {
		e.T.Fatalf("%s: codes %q (foreign) / %q (missing), want %q", tool, fc, mc, code)
	}
	if lsIDs.ReplaceAllString(fm, "<id>") != lsIDs.ReplaceAllString(mm, "<id>") {
		e.T.Fatalf("%s leaks existence: %q vs %q", tool, fm, mm)
	}
}

func ids(list []any) []string {
	var out []string
	for _, x := range list {
		out = append(out, x.(map[string]any)["id"].(string))
	}
	return out
}

func columnIDs(cols map[string]any) []string {
	var out []string
	for _, c := range cols {
		out = append(out, ids(c.([]any))...)
	}
	return out
}

func TestLeaderToolsOnlyReachOwnBriefs(t *testing.T) {
	e := lsNew(t)
	bRex := e.brief("rex brief", "rex")
	bMax := e.brief("max secret brief", "max")
	bAny := e.brief("shared brief", "")

	rexTasks := e.approved(e.rex, bRex, "r1", "r2")
	maxTasks := e.approved(e.max, bMax, "m1", "m2")
	anyTasks := e.approved(e.max, bAny, "a1")
	mt := maxTasks[0]
	e.Must(e.max, "assign", map[string]any{"task_id": mt, "agent_id": "w2"})
	split := e.Must(e.w2, "suggest_split", map[string]any{"task_id": mt, "proposed_tasks": []any{
		map[string]any{"title": "half a", "spec": "a"}, map[string]any{"title": "half b", "spec": "b"}}})["request"].(map[string]any)["id"].(string)
	art := e.Must(e.max, "put_artifact", map[string]any{"scope": "project:" + bMax, "name": "plan.txt",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("secret"))})["artifact"].(map[string]any)["id"].(string)

	for _, tc := range []struct {
		tool             string
		foreign, missing map[string]any
		code             string
	}{
		{"get_brief", map[string]any{"brief_id": bMax}, map[string]any{"brief_id": "B-999"}, "not_found"},
		{"board", map[string]any{"brief_id": bMax}, map[string]any{"brief_id": "B-999"}, "not_found"},
		{"propose_plan", map[string]any{"brief_id": bMax, "tasks": []any{lsTask("x")}}, map[string]any{"brief_id": "B-999", "tasks": []any{lsTask("x")}}, "not_found"},
		{"create_task", map[string]any{"brief_id": bMax, "title": "x", "spec": "x", "repo": "o/r"}, map[string]any{"brief_id": "B-999", "title": "x", "spec": "x", "repo": "o/r"}, "not_found"},
		{"create_task_from_template",
			map[string]any{"brief_id": bMax, "template_id": "write-integration-tests", "repo": "o/r", "vars": map[string]any{"subject": "s", "scope": "x"}},
			map[string]any{"brief_id": "B-999", "template_id": "write-integration-tests", "repo": "o/r", "vars": map[string]any{"subject": "s", "scope": "x"}}, "not_found"},
		{"get_task", map[string]any{"task_id": mt}, map[string]any{"task_id": "T-999"}, "not_found"},
		{"update_task", map[string]any{"task_id": maxTasks[1], "spec": "hijack"}, map[string]any{"task_id": "T-999", "spec": "hijack"}, "not_found"},
		{"update_task", map[string]any{"task_id": maxTasks[1], "status": "cancelled"}, map[string]any{"task_id": "T-999", "status": "cancelled"}, "not_found"},
		{"update_task", map[string]any{"task_id": rexTasks[0], "depends_on": []string{maxTasks[1]}}, map[string]any{"task_id": rexTasks[0], "depends_on": []string{"T-999"}}, "bad_request"},
		{"assign", map[string]any{"task_id": maxTasks[1], "agent_id": "w1"}, map[string]any{"task_id": "T-999", "agent_id": "w1"}, "not_found"},
		{"reassign", map[string]any{"task_id": mt, "agent_id": "w1"}, map[string]any{"task_id": "T-999", "agent_id": "w1"}, "not_found"},
		{"request_spawn", map[string]any{"brief_id": bMax, "worker_kind": "claude-code", "model": "m", "n": 1}, map[string]any{"brief_id": "B-999", "worker_kind": "claude-code", "model": "m", "n": 1}, "not_found"},
		{"read_thread", map[string]any{"thread": "task:" + mt}, map[string]any{"thread": "task:T-999"}, "not_found"},
		{"read_thread", map[string]any{"thread": "brief:" + bMax}, map[string]any{"thread": "brief:B-999"}, "not_found"},
		{"post_message", map[string]any{"thread": "task:" + mt, "body": "hi"}, map[string]any{"thread": "task:T-999", "body": "hi"}, "not_found"},
		{"post_message", map[string]any{"thread": "brief:" + bMax, "body": "hi"}, map[string]any{"thread": "brief:B-999", "body": "hi"}, "not_found"},
		{"add_note", map[string]any{"task_id": mt, "body": "hi"}, map[string]any{"task_id": "T-999", "body": "hi"}, "not_found"},
		{"ask_owner", map[string]any{"task_id": mt, "question": "?"}, map[string]any{"task_id": "T-999", "question": "?"}, "not_found"},
		{"accept_split", map[string]any{"request_id": split}, map[string]any{"request_id": "HR-999"}, "not_found"},
		{"get_artifact", map[string]any{"id": art}, map[string]any{"id": "A-999"}, "not_found"},
		{"list_artifacts", map[string]any{"scope": "project:" + bMax}, map[string]any{"scope": "project:B-999"}, "forbidden"},
		{"put_artifact", map[string]any{"scope": "task:" + mt, "name": "x", "content_base64": "eA=="}, map[string]any{"scope": "task:T-999", "name": "x", "content_base64": "eA=="}, "forbidden"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			e.T = t
			e.sameAsMissing(e.rex, tc.tool, tc.foreign, tc.missing, tc.code)
		})
	}
	e.T = t

	briefs := ids(e.Must(e.rex, "list_briefs", map[string]any{})["briefs"].([]any))
	if !slices.Equal(briefs, []string{bRex, bAny}) {
		t.Fatalf("rex list_briefs = %v", briefs)
	}
	all := columnIDs(e.Must(e.rex, "board", map[string]any{})["columns"].(map[string]any))
	slices.Sort(all)
	want := append(append([]string{}, rexTasks...), anyTasks...)
	slices.Sort(want)
	if !slices.Equal(all, want) {
		t.Fatalf("rex board = %v, want %v", all, want)
	}
	if hr := e.Must(e.rex, "list_help_requests", map[string]any{"status": "all"})["requests"].([]any); len(hr) != 0 {
		t.Fatalf("rex sees help requests %v", hr)
	}
	if hr := e.Must(e.max, "list_help_requests", map[string]any{"status": "all"})["requests"].([]any); len(hr) != 1 {
		t.Fatalf("max help requests %v", hr)
	}
	if arts := e.Must(e.rex, "list_artifacts", map[string]any{})["artifacts"].([]any); len(arts) != 0 {
		t.Fatalf("rex lists artifacts %v", arts)
	}
	if ag := e.Must(e.rex, "list_agents", map[string]any{})["agents"].([]any); len(ag) != 5 {
		t.Fatalf("list_agents filtered: %v", ag)
	}
	e.Must(e.rex, "capacity", map[string]any{})

	e.Must(e.rex, "assign", map[string]any{"task_id": anyTasks[0], "agent_id": "w1"})
	e.Must(e.rex, "assign", map[string]any{"task_id": rexTasks[0], "agent_id": "w1"})
	e.Must(e.rex, "post_message", map[string]any{"thread": "brief:" + bAny, "body": "rex here"})
	e.Must(e.max, "update_task", map[string]any{"task_id": maxTasks[1], "spec": "still mine"})
	e.Must(e.max, "accept_split", map[string]any{"request_id": split})
	if c := e.want(200, "GET", "/api/v1/tasks/"+maxTasks[1], nil); c["spec"] != "still mine" || c["status"] != "ready" {
		t.Fatalf("max task changed by rex: %v", c)
	}
	e.MustFail(e.max, "get_brief", map[string]any{"brief_id": bRex}, "not_found")
}

func TestLeaderHandoff(t *testing.T) {
	e := lsNew(t)
	bid := e.brief("handoff", "rex")
	tasks := e.approved(e.rex, bid, "a", "b")
	e.MustFail(e.max, "get_brief", map[string]any{"brief_id": bid}, "not_found")

	br := e.want(200, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": "max"})
	if br["leader_id"] != "max" {
		t.Fatalf("leader_id = %v", br["leader_id"])
	}
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"get_brief", map[string]any{"brief_id": bid}},
		{"get_task", map[string]any{"task_id": tasks[0]}},
		{"assign", map[string]any{"task_id": tasks[0], "agent_id": "w1"}},
		{"read_thread", map[string]any{"thread": "brief:" + bid}},
		{"create_task", map[string]any{"brief_id": bid, "title": "x", "spec": "x", "repo": "o/r"}},
	} {
		e.MustFail(e.rex, call.tool, call.args, "not_found")
	}
	e.Must(e.max, "assign", map[string]any{"task_id": tasks[0], "agent_id": "w1"})

	msgs := e.want(200, "GET", "/api/v1/threads/brief/"+bid+"/messages", nil)["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["kind"] != "system" || last["body"] != "Leader of "+bid+" changed: rex → max" {
		t.Fatalf("handoff message %v", last)
	}
	for agent, want := range map[string]string{"rex": "no longer the leader of brief " + bid, "max": "now the leader of brief " + bid} {
		am := e.want(200, "GET", "/api/v1/threads/agent/"+agent+"/messages", nil)["messages"].([]any)
		if len(am) == 0 || !strings.Contains(am[len(am)-1].(map[string]any)["body"].(string), want) {
			t.Fatalf("%s agent thread %v", agent, am)
		}
	}
	ev := e.want(200, "GET", "/api/v1/audit?tool=set_brief_leader", nil)["events"].([]any)
	if len(ev) != 1 {
		t.Fatalf("audit %v", ev)
	}
	e.want(200, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": "max"})
	if ev := e.want(200, "GET", "/api/v1/audit?tool=set_brief_leader", nil)["events"].([]any); len(ev) != 1 {
		t.Fatalf("same leader again wrote audit: %v", ev)
	}

	if br := e.want(200, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": nil}); br["leader_id"] != nil {
		t.Fatalf("cleared leader_id = %v", br["leader_id"])
	}
	e.Must(e.rex, "get_brief", map[string]any{"brief_id": bid})
	e.Must(e.max, "get_brief", map[string]any{"brief_id": bid})

	if br := e.want(200, "PATCH", "/api/v1/briefs/"+bid, map[string]any{"leader_id": "rex"}); br["leader_id"] != "rex" {
		t.Fatalf("patched leader_id = %v", br["leader_id"])
	}
	e.MustFail(e.max, "get_task", map[string]any{"task_id": tasks[1]}, "not_found")
	if br := e.want(200, "PATCH", "/api/v1/briefs/"+bid, map[string]any{"max_cost_eur": 5}); br["leader_id"] != "rex" {
		t.Fatalf("unrelated patch changed leader_id: %v", br["leader_id"])
	}
	if br := e.want(200, "PATCH", "/api/v1/briefs/"+bid, map[string]any{"leader_id": ""}); br["leader_id"] != nil {
		t.Fatalf("patch clear leader_id = %v", br["leader_id"])
	}

	e.want(400, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": "w1"})
	e.want(400, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{})
	e.want(404, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": "ghost"})
	e.want(404, "POST", "/api/v1/briefs/B-999/leader", map[string]any{"agent_id": "rex"})
	e.want(400, "POST", "/api/v1/briefs", map[string]any{"title": "bad", "leader_id": "w1"})
	e.want(200, "POST", "/api/v1/agents/max/revoke", map[string]any{})
	e.want(409, "POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": "max"})
}

func TestBriefImportSetsLeader(t *testing.T) {
	e := lsNew(t)
	doc := "---\ntitle: imported\nrepos: [o/r]\nleader_id: max\n---\nbody\n"
	req, _ := http.NewRequest("POST", e.TS.URL+"/api/v1/briefs/import", strings.NewReader(doc))
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	req.Header.Set("Content-Type", "text/markdown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Brief map[string]any `json:"brief"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 201 {
		t.Fatalf("import: %d %v", resp.StatusCode, err)
	}
	if out.Brief["leader_id"] != "max" {
		t.Fatalf("imported leader_id = %v", out.Brief["leader_id"])
	}
	bid := out.Brief["id"].(string)
	e.MustFail(e.rex, "get_brief", map[string]any{"brief_id": bid}, "not_found")
	e.Must(e.max, "get_brief", map[string]any{"brief_id": bid})
}

func TestBriefWithoutLeaderIsSharedByAllLeaders(t *testing.T) {
	e := lsNew(t)
	bid := e.brief("legacy", "")
	if br := e.want(200, "GET", "/api/v1/briefs/"+bid, nil); br["leader_id"] != nil {
		t.Fatalf("leader_id = %v", br["leader_id"])
	}
	tasks := e.approved(e.rex, bid, "a", "b")
	e.Must(e.max, "assign", map[string]any{"task_id": tasks[0], "agent_id": "w1"})
	e.Must(e.rex, "assign", map[string]any{"task_id": tasks[1], "agent_id": "w2"})
	e.Must(e.max, "post_message", map[string]any{"thread": "task:" + tasks[0], "body": "max"})
	e.Must(e.rex, "read_thread", map[string]any{"thread": "task:" + tasks[0]})
	e.Must(e.w1, "claim_task", map[string]any{"task_id": tasks[0]})
	e.Must(e.rex, "reassign", map[string]any{"task_id": tasks[0], "agent_id": "w2"})
	for _, lead := range []string{e.rex, e.max} {
		if bs := e.Must(lead, "list_briefs", map[string]any{})["briefs"].([]any); len(bs) != 1 {
			t.Fatalf("list_briefs %v", bs)
		}
	}
}

func TestLeaderCannotGuessIDsAcrossBriefs(t *testing.T) {
	e := lsNew(t)
	bRex := e.brief("rex brief", "rex")
	bMax := e.brief("max secret brief", "max")
	e.approved(e.rex, bRex, "rex-task")
	maxTasks := e.approved(e.max, bMax, "classified-one", "classified-two")
	e.Must(e.max, "assign", map[string]any{"task_id": maxTasks[0], "agent_id": "w1"})
	e.Must(e.w1, "claim_task", map[string]any{"task_id": maxTasks[0]})
	e.Must(e.w1, "ask_owner", map[string]any{"task_id": maxTasks[0], "question": "classified question"})
	e.Must(e.w1, "request_help", map[string]any{"task_id": maxTasks[0], "kind": "unclear_spec", "details": "classified help"})

	probe := func(tool, key, prefix string, n int) {
		t.Helper()
		var shape string
		for i := 1; i <= n; i++ {
			args := map[string]any{key: fmt.Sprintf("%s%d", prefix, i)}
			if tool == "read_thread" || tool == "post_message" {
				args["body"] = "probe"
			}
			out, isErr := e.Call(e.rex, tool, args)
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "classified") || strings.Contains(string(raw), "secret") {
				t.Fatalf("%s %v leaked: %s", tool, args, raw)
			}
			if !isErr {
				continue
			}
			s := lsIDs.ReplaceAllString(string(raw), "<id>")
			if shape == "" {
				shape = s
			} else if s != shape {
				t.Fatalf("%s %v: error %s differs from %s", tool, args, s, shape)
			}
		}
	}
	probe("get_brief", "brief_id", "B-", 10)
	probe("get_task", "task_id", "T-", 30)
	probe("update_task", "task_id", "T-", 30)
	probe("add_note", "task_id", "T-", 30)
	probe("read_thread", "thread", "task:T-", 30)
	probe("read_thread", "thread", "brief:B-", 10)
	probe("accept_split", "request_id", "HR-", 10)
	probe("get_artifact", "id", "A-", 5)
	for _, tool := range []string{"list_briefs", "board", "list_help_requests", "list_artifacts", "list_my_tasks"} {
		raw, _ := json.Marshal(e.Must(e.rex, tool, map[string]any{}))
		if strings.Contains(string(raw), "classified") || strings.Contains(string(raw), bMax) {
			t.Fatalf("%s leaked: %s", tool, raw)
		}
	}
	for _, s := range []string{"open", "all"} {
		raw, _ := json.Marshal(e.Must(e.rex, "list_help_requests", map[string]any{"status": s, "brief_id": bMax}))
		if strings.Contains(string(raw), "classified") {
			t.Fatalf("list_help_requests leaked: %s", raw)
		}
	}
}
