package instructions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type recorder struct {
	mu     sync.Mutex
	events []instructions.Event
}

func (r *recorder) Record(_ context.Context, e instructions.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

type env struct {
	t     *testing.T
	st    *sqlite.Store
	svc   *instructions.Service
	srv   *httptest.Server
	audit *recorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	rec := &recorder{}
	svc := instructions.New(st, rec, func() time.Time {
		clock = clock.Add(time.Minute)
		return clock
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", instructions.Handler(svc, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, svc: svc, srv: srv, audit: rec}
}

func (e *env) do(method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+"/api/v1"+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func (e *env) save(scope, kind, text, msg string) instructions.Version {
	e.t.Helper()
	var v instructions.Version
	if code := e.do("PUT", "/instructions/"+scope+"/"+kind, map[string]string{"text": text, "message": msg}, &v); code != http.StatusCreated {
		e.t.Fatalf("save %s/%s: status %d", scope, kind, code)
	}
	return v
}

func TestVersioningHistoryDiffRollback(t *testing.T) {
	e := newEnv(t)

	v1 := e.save("global", "rules", "alpha\nbeta\ngamma", "first")
	v2 := e.save("global", "rules", "alpha\nBETA\ngamma\ndelta", "second")
	if v1.Version != 1 || v2.Version != 2 || v2.Author != "owner" || v2.Message != "second" || !v2.CreatedAt.After(v1.CreatedAt) {
		t.Fatalf("unexpected versions: %+v %+v", v1, v2)
	}

	var cur instructions.Version
	if code := e.do("GET", "/instructions/global/rules", nil, &cur); code != 200 || cur.Version != 2 || cur.Text != v2.Text {
		t.Fatalf("current: %d %+v", code, cur)
	}

	var old instructions.Version
	if code := e.do("GET", "/instructions/global/rules/versions/1", nil, &old); code != 200 || old.Text != "alpha\nbeta\ngamma" {
		t.Fatalf("v1 is immutable: %d %+v", code, old)
	}

	var hist struct{ Versions []instructions.Meta }
	e.do("GET", "/instructions/global/rules/versions", nil, &hist)
	if len(hist.Versions) != 2 || hist.Versions[0].Version != 2 || hist.Versions[1].Message != "first" {
		t.Fatalf("history: %+v", hist)
	}

	var diff struct{ Lines []instructions.DiffLine }
	if code := e.do("GET", "/instructions/global/rules/diff?from=1&to=2", nil, &diff); code != 200 {
		t.Fatalf("diff status %d", code)
	}
	want := []instructions.DiffLine{
		{Op: "equal", Text: "alpha"}, {Op: "delete", Text: "beta"}, {Op: "add", Text: "BETA"},
		{Op: "equal", Text: "gamma"}, {Op: "add", Text: "delta"},
	}
	if len(diff.Lines) != len(want) {
		t.Fatalf("diff: %+v", diff.Lines)
	}
	for i := range want {
		if diff.Lines[i] != want[i] {
			t.Fatalf("diff[%d] = %+v, want %+v", i, diff.Lines[i], want[i])
		}
	}

	var rb instructions.Version
	if code := e.do("POST", "/instructions/global/rules/rollback", map[string]any{"to": 1}, &rb); code != 201 {
		t.Fatalf("rollback status %d", code)
	}
	if rb.Version != 3 || rb.Text != v1.Text || rb.Message != "rollback to v1" {
		t.Fatalf("rollback: %+v", rb)
	}
	e.do("GET", "/instructions/global/rules/versions", nil, &hist)
	if len(hist.Versions) != 3 {
		t.Fatalf("history must keep all versions: %+v", hist)
	}

	if code := e.do("POST", "/instructions/global/rules/rollback", map[string]any{"to": 3}, nil); code != http.StatusConflict {
		t.Fatalf("rollback to head: %d", code)
	}
	if code := e.do("POST", "/instructions/global/rules/rollback", map[string]any{"to": 9}, nil); code != http.StatusNotFound {
		t.Fatalf("rollback to missing: %d", code)
	}

	if len(e.audit.events) != 3 {
		t.Fatalf("audit events: %+v", e.audit.events)
	}
	last := e.audit.events[2]
	if last.Tool != "instructions_rollback" || last.Before != 2 || last.After != 3 || last.Actor != model.Owner {
		t.Fatalf("rollback audit: %+v", last)
	}
}

func TestValidationErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{"PUT", "/instructions/team/rules", map[string]string{"text": "x"}, 400},
		{"PUT", "/instructions/project:/rules", map[string]string{"text": "x"}, 400},
		{"PUT", "/instructions/global/prompts", map[string]string{"text": "x"}, 400},
		{"PUT", "/instructions/global/rules", map[string]string{"text": strings.Repeat("x", 300<<10)}, 400},
		{"GET", "/instructions/global/rules", nil, 404},
		{"GET", "/instructions/global/rules/versions/x", nil, 400},
		{"GET", "/instructions/global/rules/diff?from=1", nil, 400},
		{"GET", "/instructions/effective?project=a/b", nil, 400},
	}
	for _, c := range cases {
		var body struct{ Error struct{ Code string } }
		if got := e.do(c.method, c.path, c.body, &body); got != c.want || body.Error.Code == "" {
			t.Errorf("%s %s: status %d (%+v), want %d", c.method, c.path, got, body, c.want)
		}
	}
	if len(e.audit.events) != 0 {
		t.Fatalf("failed saves must not be audited: %+v", e.audit.events)
	}
}

func TestEffectiveMergesProjectOverGlobal(t *testing.T) {
	e := newEnv(t)
	e.save("global", "rules", "Be careful.\n\n## Git\nnever push to main\n\n## Secrets\nnone in code\n\n```\n## Git\n```", "")
	e.save("global", "style", "## Code\nno comments", "")
	e.save("project:B-1", "rules", "## git\nopen PR against feat/x\n\n## Testing\nrun go test", "")
	e.save("project:B-2", "rules", "## Git\nother project", "")

	var eff instructions.Effective
	if code := e.do("GET", "/instructions/effective?project=B-1", nil, &eff); code != 200 {
		t.Fatalf("status %d", code)
	}
	if eff.Version != "g1.1.0/p1.0.0" {
		t.Fatalf("version %q", eff.Version)
	}
	for _, want := range []string{"# Rules", "Be careful.", "open PR against feat/x", "none in code", "run go test", "# Style", "no comments"} {
		if !strings.Contains(eff.Text, want) {
			t.Errorf("effective text missing %q:\n%s", want, eff.Text)
		}
	}
	if strings.Contains(eff.Text, "never push to main") || strings.Contains(eff.Text, "other project") {
		t.Errorf("override leaked or other project included:\n%s", eff.Text)
	}
	if strings.Contains(eff.Text, "# Templates") {
		t.Errorf("empty kind rendered:\n%s", eff.Text)
	}
	if strings.Index(eff.Text, "open PR") > strings.Index(eff.Text, "none in code") {
		t.Errorf("override must keep global section position:\n%s", eff.Text)
	}

	var global instructions.Effective
	e.do("GET", "/instructions/effective", nil, &global)
	if global.Version != "g1.1.0" || !strings.Contains(global.Text, "never push to main") {
		t.Fatalf("global effective: %+v", global)
	}

	before := eff.Version
	e.save("project:B-1", "style", "## Code\ntabs", "")
	e.do("GET", "/instructions/effective?project=B-1", nil, &eff)
	if eff.Version == before || eff.Version != "g1.1.0/p1.1.0" {
		t.Fatalf("version did not change: %q -> %q", before, eff.Version)
	}
}

type fixedProject string

func (f fixedProject) ProjectOf(context.Context, string) (string, error) { return string(f), nil }

func TestMCPGetInstructionsUsesAgentProject(t *testing.T) {
	e := newEnv(t)
	e.save("global", "rules", "## Git\nglobal git", "")
	e.save("project:B-7", "rules", "## Git\nproject git", "")

	ctx := context.Background()
	err := e.st.Atomic(ctx, func(tx store.Tx) error {
		assignee := "claude-1"
		return tx.InsertTask(&model.Task{
			ID: "T-1", BriefID: "B-7", Status: model.StatusInProgress, Assignee: &assignee,
			Role: model.RoleWorker, AcceptanceCriteria: []string{}, DependsOn: []string{},
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	tools := instructions.NewTools(e.svc, instructions.StoreProjects{Store: e.st})
	tool := tools.GetInstructions()
	if tool.Name != "get_instructions" || !tool.ReadOnly || tool.Schema["type"] != "object" {
		t.Fatalf("tool definition: %+v", tool)
	}

	out, err := tool.Handler(ctx, model.Actor{ID: "claude-1", Role: model.RoleWorker}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if !strings.Contains(res["instructions"].(string), "project git") || res["version"] != "g1.0.0/p1.0.0" || res["project"] != "B-7" {
		t.Fatalf("worker result: %+v", res)
	}

	out, err = tool.Handler(ctx, model.Actor{ID: "nobody", Role: model.RoleWorker}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res = out.(map[string]any)
	if !strings.Contains(res["instructions"].(string), "global git") || res["version"] != "g1.0.0" {
		t.Fatalf("agent without tasks must get global only: %+v", res)
	}

	v, err := tools.InstructionsVersion(ctx, model.Actor{ID: "claude-1", Role: model.RoleWorker})
	if err != nil || v != "g1.0.0/p1.0.0" {
		t.Fatalf("version %q, %v", v, err)
	}

	custom := instructions.NewTools(e.svc, fixedProject("bad/id"))
	if _, err := custom.GetInstructions().Handler(ctx, model.Actor{ID: "x"}, nil); err == nil {
		t.Fatal("invalid project id must fail")
	}
}

func TestStoreAuditorWritesAuditEvent(t *testing.T) {
	e := newEnv(t)
	svc := instructions.New(e.st, instructions.StoreAuditor{Store: e.st}, nil)
	if _, err := svc.Save(context.Background(), model.Owner, "global", "rules", "x", "init"); err != nil {
		t.Fatal(err)
	}
	var events []*model.AuditEvent
	err := e.st.View(context.Background(), func(tx store.Tx) error {
		var err error
		events, err = tx.ListAudit(store.AuditFilter{Tool: "instructions_save"})
		return err
	})
	if err != nil || len(events) != 1 || events[0].AgentID != "owner" || events[0].After["version"] != float64(1) {
		t.Fatalf("audit: %+v, %v", events, err)
	}
}

func TestConcurrentSavesGetDistinctVersions(t *testing.T) {
	e := newEnv(t)
	const n = 40
	var wg sync.WaitGroup
	got := make(chan int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.svc.Save(context.Background(), model.Actor{ID: "owner", Role: model.RoleOwner}, "project:B-1", "style", strings.Repeat("x", i+1), "")
			if err != nil {
				t.Error(err)
				return
			}
			got <- v.Version
		}()
	}
	wg.Wait()
	close(got)
	seen := map[int]bool{}
	for v := range got {
		if seen[v] {
			t.Fatalf("version %d issued twice", v)
		}
		seen[v] = true
	}
	if len(seen) != n || !seen[1] || !seen[n] {
		t.Fatalf("versions not contiguous: %v", seen)
	}
}
