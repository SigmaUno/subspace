package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spawner"
)

var (
	contractRouteRow = regexp.MustCompile("^\\| *([A-Z]+(?:/[A-Z]+)*) *\\| *`(/api/v1/[^`]*)`")
	contractPathVar  = regexp.MustCompile(`\{(\w+)\}`)
	contractLineCmt  = regexp.MustCompile(`(^|\s)//.*$`)
	contractBlockCmt = regexp.MustCompile(`/\*.*?\*/`)
	contractToken    = regexp.MustCompile("`([^`]+)`")
	contractEventRe  = regexp.MustCompile(`^(hello|lagged|resync|[a-z]+\.[a-z]+)$`)
	contractToolRe   = regexp.MustCompile(`^[a-z][a-z_]*$`)
)

type contractRoute struct{ method, path string }

func contractDoc(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "\n")
}

func contractRoutes(t *testing.T) []contractRoute {
	t.Helper()
	var out []contractRoute
	for _, l := range contractDoc(t) {
		m := contractRouteRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		path, _, _ := strings.Cut(m[2], "?")
		for _, method := range strings.Split(m[1], "/") {
			r := contractRoute{method, path}
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	if len(out) < 50 {
		t.Fatalf("parsed only %d documented routes", len(out))
	}
	return out
}

func contractTable(t *testing.T, header string) [][]string {
	t.Helper()
	lines := contractDoc(t)
	start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, header) })
	if start < 0 {
		t.Fatalf("table %q not found in docs/api.md", header)
	}
	var rows [][]string
	for _, l := range lines[start:] {
		if !strings.HasPrefix(l, "|") {
			break
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	return rows[1:]
}

func contractDummyPath(p string) string {
	return contractPathVar.ReplaceAllStringFunc(p, func(m string) string {
		switch m[1 : len(m)-1] {
		case "kind":
			if strings.Contains(p, "/instructions/") {
				return "rules"
			}
			return "task"
		case "scope":
			return "global"
		case "n":
			return "1"
		}
		return "X-999"
	})
}

func contractRouteMissing(code int, body []byte) bool {
	switch code {
	case http.StatusMethodNotAllowed:
		return true
	case http.StatusNotFound:
		var e struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(body, &e) != nil || e.Error.Code == "" {
			return true
		}
		return e.Error.Message == "no such endpoint"
	}
	return false
}

func (e *env) contractDo(method, path string) (int, []byte) {
	e.t.Helper()
	var body io.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func contractEnv(t *testing.T) *env {
	t.Helper()
	return newFlowEnv(t, func(c *Config) { c.Spawner = spawner.Noop{} })
}

func TestContractRESTRoutesExist(t *testing.T) {
	e := contractEnv(t)
	for _, r := range contractRoutes(t) {
		if r.path == "/api/v1/events" {
			continue
		}
		path := contractDummyPath(r.path)
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			e.t = t
			code, body := e.contractDo(r.method, path)
			if contractRouteMissing(code, body) {
				t.Fatalf("%s %s is documented but not served: %d %s", r.method, path, code, body)
			}
			if code >= 500 {
				t.Fatalf("%s %s: %d %s", r.method, path, code, body)
			}
		})
	}
}

func TestContractMCPToolsPerRole(t *testing.T) {
	e := newEnv(t)
	rows := contractTable(t, "| Tool | leader | worker | tester | reviewer |")
	roles := []string{"leader", "worker", "tester", "reviewer"}
	want := map[string][]string{}
	for _, cells := range rows {
		if len(cells) != 1+len(roles) || strings.HasPrefix(cells[0], "-") {
			continue
		}
		var names []string
		for _, n := range strings.Split(cells[0], ",") {
			n = strings.Trim(strings.TrimSpace(n), "`")
			if !contractToolRe.MatchString(n) {
				t.Fatalf("tool permission row has invalid tool name %q", n)
			}
			names = append(names, n)
		}
		for i, role := range roles {
			if strings.Contains(cells[1+i], "✓") {
				want[role] = append(want[role], names...)
			}
		}
	}
	for _, role := range roles {
		if len(want[role]) == 0 {
			t.Fatalf("no documented tools for %s", role)
		}
		sort.Strings(want[role])
		tok := e.register("contract-"+role, role, "other")
		res, _ := e.rpc(tok, "tools/list", map[string]any{})["result"].(map[string]any)
		var got []string
		list, _ := res["tools"].([]any)
		for _, x := range list {
			got = append(got, x.(map[string]any)["name"].(string))
		}
		sort.Strings(got)
		if !slices.Equal(got, want[role]) {
			t.Errorf("%s tools/list differs from docs/api.md\n got: %v\nwant: %v", role, got, want[role])
		}
	}
}

func contractEventTypes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, cells := range contractTable(t, "| type | data | topics |") {
		if strings.HasPrefix(cells[0], "-") {
			continue
		}
		for _, m := range contractToken.FindAllStringSubmatch(cells[0], -1) {
			if !contractEventRe.MatchString(m[1]) {
				t.Fatalf("invalid event type %q in docs/api.md", m[1])
			}
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("no event types parsed")
	}
	return out
}

type contractStream struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (s *contractStream) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadString('\n')
		if typ, ok := strings.CutPrefix(line, "event: "); ok {
			s.mu.Lock()
			s.seen[strings.TrimSpace(typ)] = true
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *contractStream) missing(want []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, w := range want {
		if !s.seen[w] {
			out = append(out, w)
		}
	}
	return out
}

func (s *contractStream) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.seen))
	for k := range s.seen {
		out = append(out, k)
	}
	return out
}

func (e *env) contractEvents(ctx context.Context, topics string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/v1/events?topics="+topics, nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		e.t.Fatalf("GET /api/v1/events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return resp
}

func (e *env) contractEmitAll() {
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	e.register("r1", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil))
	rejected := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{planTask("b", nil)}})["plan"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/plans/"+rejected["id"].(string)+"/reject", map[string]any{"reason": "no"})
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": ids[0]})
	q := e.must(wk, "ask_owner", map[string]any{"task_id": ids[0], "question": "which?"})["question"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/questions/"+q["id"].(string)+"/answer", map[string]any{"answer": "a"})
	e.restWant(200, "PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 300, "limits": []any{}})
	e.must(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "m", "n": 1})
	e.must(wk, "update_status", map[string]any{"task_id": ids[0], "status": "in_progress", "cost_eur": 0.9})
	e.restWant(200, "PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": 1})
	e.restWant(200, "POST", "/api/v1/pause-all", map[string]any{"reason": "x"})
	e.restWant(200, "POST", "/api/v1/resume-all", map[string]any{})
}

func TestContractSSEEventsEmitted(t *testing.T) {
	e := contractEnv(t)
	want := contractEventTypes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	main := &contractStream{seen: map[string]bool{}}
	resp := e.contractEvents(ctx, "all")
	defer func() { _ = resp.Body.Close() }()
	go main.read(resp.Body)

	slow := e.contractEvents(ctx, "board")
	defer func() { _ = slow.Body.Close() }()
	pad := strings.Repeat("x", 128<<10)
	for range 800 {
		e.srv.Hub.Publish([]string{"board"}, "task.updated", map[string]string{"pad": pad})
	}
	lagged := &contractStream{seen: map[string]bool{}}
	go lagged.read(slow.Body)

	resumed := e.contractEvents(ctx, "all&last_event_id=999999999")
	defer func() { _ = resumed.Body.Close() }()
	go lagged.read(resumed.Body)

	e.contractEmitAll()

	wantMain := slices.DeleteFunc(slices.Clone(want), func(w string) bool { return w == "lagged" })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(main.missing(wantMain)) == 0 && len(lagged.missing([]string{"lagged"})) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	emitted := slices.Concat(main.types(), lagged.types())
	for _, w := range want {
		if !slices.Contains(emitted, w) {
			t.Errorf("event %q is documented in docs/api.md but never emitted", w)
		}
	}
	for _, typ := range emitted {
		if !slices.Contains(want, typ) {
			t.Errorf("event %q is emitted but not documented", typ)
		}
	}
}

func contractDocObject(t *testing.T, name string) map[string]any {
	t.Helper()
	lines := contractDoc(t)
	start := slices.Index(lines, "// "+name)
	if start < 0 {
		t.Fatalf("object %s not found in docs/api.md", name)
	}
	var sb strings.Builder
	for _, l := range lines[start+1:] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "```") {
			break
		}
		l = contractBlockCmt.ReplaceAllString(l, "")
		sb.WriteString(contractLineCmt.ReplaceAllString(l, ""))
		sb.WriteByte('\n')
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("object %s in docs/api.md is not valid JSON: %v\n%s", name, err, sb.String())
	}
	return out
}

func contractCompareKeys(t *testing.T, name, prefix string, want, got map[string]any) {
	t.Helper()
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s: documented field %s%s is not encoded by the server", name, prefix, k)
		}
	}
	for k, v := range got {
		w, ok := want[k]
		if !ok {
			t.Errorf("%s: encoded field %s%s is not documented", name, prefix, k)
			continue
		}
		wm, wok := w.(map[string]any)
		gm, gok := v.(map[string]any)
		if wok && gok {
			contractCompareKeys(t, name, prefix+k+".", wm, gm)
		}
	}
}

func TestContractObjectFields(t *testing.T) {
	e := contractEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{planTask("a", nil)}})["plan"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": tid})
	e.must(wk, "add_note", map[string]any{"task_id": tid, "body": "note"})
	e.must(wk, "ask_owner", map[string]any{"task_id": tid, "question": "which?", "options": []string{"a", "b"}})

	first := func(out map[string]any, key string) map[string]any {
		list, _ := out[key].([]any)
		if len(list) == 0 {
			t.Fatalf("no %s in %v", key, out)
		}
		return list[0].(map[string]any)
	}
	got := map[string]map[string]any{
		"Brief":    e.restWant(200, "GET", "/api/v1/briefs/"+bid, nil),
		"Task":     e.restWant(200, "GET", "/api/v1/tasks/"+tid, nil),
		"Plan":     e.restWant(200, "GET", "/api/v1/plans/"+plan["id"].(string), nil),
		"Agent":    e.restWant(200, "GET", "/api/v1/agents/w1", nil),
		"Message":  first(e.restWant(200, "GET", "/api/v1/threads/task/"+tid+"/messages", nil), "messages"),
		"Question": first(e.restWant(200, "GET", "/api/v1/questions", nil), "questions"),
	}
	for name, sample := range got {
		t.Run(name, func(t *testing.T) {
			contractCompareKeys(t, name, "", contractDocObject(t, name), sample)
		})
	}
}
