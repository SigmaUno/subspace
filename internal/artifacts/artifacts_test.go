package artifacts_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type dir struct {
	tasks  map[string]*model.Task
	briefs map[string]bool
	agents map[string]bool
}

func (d dir) Task(_ context.Context, id string) (*model.Task, error) {
	if t, ok := d.tasks[id]; ok {
		return t, nil
	}
	return nil, store.ErrNotFound
}

func (d dir) Brief(_ context.Context, id string) (*model.Brief, error) {
	if d.briefs[id] {
		return &model.Brief{ID: id}, nil
	}
	return nil, store.ErrNotFound
}

func (d dir) Agent(_ context.Context, id string) (*model.Agent, error) {
	if d.agents[id] {
		return &model.Agent{ID: id}, nil
	}
	return nil, store.ErrNotFound
}

func (d dir) AgentTasks(_ context.Context, agentID string) ([]*model.Task, error) {
	var out []*model.Task
	for _, t := range d.tasks {
		if (t.Assignee != nil && *t.Assignee == agentID) || (t.Reviewer != nil && *t.Reviewer == agentID) {
			out = append(out, t)
		}
	}
	return out, nil
}

type auditLog struct {
	mu     sync.Mutex
	events []*model.AuditEvent
}

func (a *auditLog) Record(_ context.Context, e *model.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *auditLog) tools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.events {
		out = append(out, e.Tool)
	}
	return out
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func ptr(s string) *string { return &s }

type env struct {
	svc   *artifacts.Service
	h     http.Handler
	dl    http.Handler
	root  string
	audit *auditLog
	clock *clock
}

func setup(t *testing.T, mut func(*artifacts.Config)) *env {
	t.Helper()
	d := dir{
		tasks: map[string]*model.Task{
			"T-1": {ID: "T-1", BriefID: "B-1", Assignee: ptr("alice")},
			"T-2": {ID: "T-2", BriefID: "B-1", Assignee: ptr("bob")},
			"T-3": {ID: "T-3", BriefID: "B-2", Assignee: ptr("carol"), Reviewer: ptr("alice")},
		},
		briefs: map[string]bool{"B-1": true, "B-2": true},
		agents: map[string]bool{"alice": true, "bob": true, "carol": true},
	}
	e := &env{root: t.TempDir(), audit: &auditLog{}, clock: &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}}
	cfg := artifacts.Config{
		Root:         e.root,
		MaxFileSize:  1 << 20,
		ProjectQuota: 4 << 20,
		InlineMax:    64,
		Store:        artifacts.NewMemStore(),
		Directory:    d,
		Auditor:      e.audit,
		URLBase:      "http://dl.test/dl",
		Secret:       []byte("0123456789abcdef0123456789abcdef"),
		Now:          e.clock.now,
	}
	if mut != nil {
		mut(&cfg)
	}
	svc, err := artifacts.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.h = http.StripPrefix("/api/v1", svc.Handler())
	e.dl = http.StripPrefix("/dl", svc.DownloadHandler())
	return e
}

func (e *env) upload(t *testing.T, scope, filename string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if scope != "" {
		_ = mw.WriteField("scope", scope)
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	hdr.Set("Content-Type", "application/octet-stream")
	fw, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(body)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return e.do(req)
}

func (e *env) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func blobFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRESTLifecycleAndDedup(t *testing.T) {
	e := setup(t, nil)
	content := []byte("hello artifacts\n")

	rec := e.upload(t, "task:T-1", "notes.txt", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	a := decodeBody[artifacts.Artifact](t, rec)
	if a.ID == "" || a.Scope != "task:T-1" || a.Name != "notes.txt" || a.Size != int64(len(content)) || a.UploadedBy != "owner" || a.Project != "B-1" {
		t.Fatalf("unexpected metadata: %+v", a)
	}

	rec = e.upload(t, "project:B-1", "copy.txt", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second upload: %d %s", rec.Code, rec.Body)
	}
	b := decodeBody[artifacts.Artifact](t, rec)
	if b.SHA256 != a.SHA256 {
		t.Fatal("identical content must share a digest")
	}
	if n := len(blobFiles(t, e.root)); n != 1 {
		t.Fatalf("want one deduplicated blob, got %d files", n)
	}

	rec = e.get("/api/v1/artifacts/" + a.ID)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename=notes.txt` {
		t.Fatalf("content-disposition %q", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}

	list := decodeBody[struct{ Artifacts []artifacts.Artifact }](t, e.get("/api/v1/artifacts?scope=task:T-1"))
	if len(list.Artifacts) != 1 || list.Artifacts[0].ID != a.ID {
		t.Fatalf("scope filter: %+v", list.Artifacts)
	}
	all := decodeBody[struct{ Artifacts []artifacts.Artifact }](t, e.get("/api/v1/artifacts"))
	if len(all.Artifacts) != 2 {
		t.Fatalf("list all: %+v", all.Artifacts)
	}

	del := func(id string) int {
		return e.do(httptest.NewRequest(http.MethodDelete, "/api/v1/artifacts/"+id, nil)).Code
	}
	if c := del(a.ID); c != http.StatusNoContent {
		t.Fatalf("delete: %d", c)
	}
	if n := len(blobFiles(t, e.root)); n != 1 {
		t.Fatalf("blob must survive while referenced, got %d files", n)
	}
	if rec := e.get("/api/v1/artifacts/" + b.ID); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("remaining artifact unreadable: %d", rec.Code)
	}
	if c := del(b.ID); c != http.StatusNoContent {
		t.Fatalf("delete: %d", c)
	}
	if n := len(blobFiles(t, e.root)); n != 0 {
		t.Fatalf("blob must be removed with its last reference, got %d files", n)
	}
	if c := e.get("/api/v1/artifacts/" + a.ID).Code; c != http.StatusNotFound {
		t.Fatalf("deleted artifact: %d", c)
	}

	want := []string{"put_artifact", "put_artifact", "delete_artifact", "delete_artifact"}
	if got := e.audit.tools(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit: %v", got)
	}
	if ev := e.audit.events[0]; ev.TaskID == nil || *ev.TaskID != "T-1" || ev.Role != model.RoleOwner {
		t.Fatalf("audit event: %+v", ev)
	}
}

func TestUploadLimitsEnforcedWhileStreaming(t *testing.T) {
	e := setup(t, func(c *artifacts.Config) {
		c.MaxFileSize = 1024
		c.ProjectQuota = 1500
	})

	rec := e.upload(t, "task:T-1", "big.bin", bytes.Repeat([]byte{1}, 4096))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", rec.Code, rec.Body)
	}
	if code := decodeBody[struct{ Error struct{ Code string } }](t, rec).Error.Code; code != artifacts.CodeTooLarge {
		t.Fatalf("code %q", code)
	}

	huge := io.LimitReader(zeros{}, 64<<20)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("scope", "task:T-1")
	fw, _ := mw.CreateFormFile("file", "huge.bin")
	_, _ = io.Copy(fw, huge)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = -1
	if rec := e.do(req); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversize: %d %s", rec.Code, rec.Body)
	}

	if rec := e.upload(t, "task:T-1", "a.bin", bytes.Repeat([]byte{2}, 1000)); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rec = e.upload(t, "project:B-1", "b.bin", bytes.Repeat([]byte{3}, 600))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("quota: %d %s", rec.Code, rec.Body)
	}
	if rec := e.upload(t, "project:B-2", "c.bin", bytes.Repeat([]byte{3}, 600)); rec.Code != http.StatusCreated {
		t.Fatalf("other project must have its own quota: %d %s", rec.Code, rec.Body)
	}

	if n := len(blobFiles(t, e.root)); n != 2 {
		t.Fatalf("rejected uploads must leave no files behind, got %d", n)
	}
	if got := len(e.audit.tools()); got != 2 {
		t.Fatalf("rejected uploads must not be audited, got %d events", got)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPathTraversalImpossible(t *testing.T) {
	e := setup(t, nil)
	parent := filepath.Dir(e.root)

	for _, name := range []string{"../../../etc/passwd", `..\..\evil.sh`, "/abs/path/x.txt", "a/../../b"} {
		rec := e.upload(t, "task:T-1", name, []byte("x"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%q: %d %s", name, rec.Code, rec.Body)
		}
		a := decodeBody[artifacts.Artifact](t, rec)
		if strings.ContainsAny(a.Name, `/\`) {
			t.Fatalf("name %q kept separators: %q", name, a.Name)
		}
	}
	for _, f := range blobFiles(t, parent) {
		if !strings.HasPrefix(f, e.root+string(filepath.Separator)) {
			t.Fatalf("file written outside root: %s", f)
		}
	}
	for _, scope := range []string{"task:../T-1", "task:T-1/../T-2", "project:", "tenant:B-1", "task:T-1\n", "task:%2e%2e"} {
		if rec := e.upload(t, scope, "x", []byte("x")); rec.Code != http.StatusBadRequest {
			t.Fatalf("scope %q: %d", scope, rec.Code)
		}
	}
	if rec := e.upload(t, "task:T-99", "x", []byte("x")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown task: %d", rec.Code)
	}
	if rec := e.get("/api/v1/artifacts/..%2f..%2fetc%2fpasswd"); rec.Code != http.StatusNotFound {
		t.Fatalf("traversal id: %d", rec.Code)
	}
}

func TestUploadRejectsNonMultipartAndMissingFile(t *testing.T) {
	e := setup(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts?scope=task:T-1", strings.NewReader("raw"))
	req.Header.Set("Content-Type", "application/octet-stream")
	if rec := e.do(req); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-multipart: %d", rec.Code)
	}
	if rec := e.upload(t, "", "x", []byte("x")); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing scope: %d", rec.Code)
	}
}

func call(t *testing.T, tool *mcp.Tool, actor model.Actor, args any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := tool.Handler(context.Background(), &mcp.Call{Actor: actor, Args: raw})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func toolSet(e *env) map[string]*mcp.Tool {
	m := map[string]*mcp.Tool{}
	for _, tl := range e.svc.Tools() {
		m[tl.Name] = tl
	}
	return m
}

func codeOf(err error) string { return model.AsError(err).Code }

func TestMCPToolsAreScopedLikeThreads(t *testing.T) {
	e := setup(t, nil)
	tools := toolSet(e)
	alice := model.Actor{ID: "alice", Role: model.RoleWorker}
	bob := model.Actor{ID: "bob", Role: model.RoleWorker}
	carol := model.Actor{ID: "carol", Role: model.RoleTester}
	leader := model.Actor{ID: "rex", Role: model.RoleLeader}

	put := func(actor model.Actor, scope, name, content string) (map[string]any, error) {
		return call(t, tools["put_artifact"], actor, map[string]any{
			"scope": scope, "name": name, "content_base64": base64.StdEncoding.EncodeToString([]byte(content)),
		})
	}
	for _, c := range []struct {
		actor model.Actor
		scope string
	}{
		{alice, "agent:alice"}, {alice, "task:T-1"}, {alice, "project:B-1"},
		{bob, "task:T-2"}, {carol, "task:T-3"}, {alice, "task:T-3"},
	} {
		if _, err := put(c.actor, c.scope, "f.txt", c.actor.ID+c.scope); err != nil {
			t.Fatalf("%s put %s: %v", c.actor.ID, c.scope, err)
		}
	}
	for _, c := range []struct {
		actor model.Actor
		scope string
	}{
		{alice, "agent:bob"}, {alice, "task:T-2"}, {bob, "task:T-1"}, {bob, "project:B-2"}, {alice, "task:T-404"},
	} {
		if _, err := put(c.actor, c.scope, "f.txt", "x"); codeOf(err) != "forbidden" {
			t.Fatalf("%s put %s: want forbidden, got %v", c.actor.ID, c.scope, err)
		}
	}

	names := func(actor model.Actor, args any) []string {
		out, err := call(t, tools["list_artifacts"], actor, args)
		if err != nil {
			t.Fatal(err)
		}
		var scopes []string
		for _, a := range out["artifacts"].([]any) {
			scopes = append(scopes, a.(map[string]any)["scope"].(string))
		}
		return scopes
	}
	if got := names(alice, map[string]any{}); strings.Join(got, ",") != "agent:alice,task:T-1,project:B-1,task:T-3,task:T-3" {
		t.Fatalf("alice sees %v", got)
	}
	if got := names(bob, map[string]any{}); strings.Join(got, ",") != "project:B-1,task:T-2" {
		t.Fatalf("bob sees %v", got)
	}
	if got := names(leader, map[string]any{}); len(got) != 6 {
		t.Fatalf("leader sees %v", got)
	}
	if _, err := call(t, tools["list_artifacts"], bob, map[string]any{"scope": "task:T-1"}); codeOf(err) != "forbidden" {
		t.Fatalf("bob list T-1: %v", err)
	}

	bobList, _ := call(t, tools["list_artifacts"], bob, map[string]any{"scope": "task:T-2"})
	bobID := bobList["artifacts"].([]any)[0].(map[string]any)["id"].(string)
	if _, err := call(t, tools["get_artifact"], alice, map[string]any{"id": bobID}); codeOf(err) != "forbidden" {
		t.Fatalf("alice get bob's artifact: %v", err)
	}
	got, err := call(t, tools["get_artifact"], bob, map[string]any{"id": bobID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(got["content_base64"].(string))
	if string(raw) != "bobtask:T-2" {
		t.Fatalf("content %q", raw)
	}

	_, err = call(t, tools["put_artifact"], alice, map[string]any{"scope": "task:T-1", "name": "bad", "content_base64": "!!!not base64!!!"})
	if codeOf(err) != "bad_request" {
		t.Fatalf("bad base64: %v", err)
	}

	for _, tl := range tools {
		if len(tl.Roles) != len(mcp.AllRoles) || tl.Schema["type"] != "object" {
			t.Fatalf("tool %s misconfigured", tl.Name)
		}
	}
	if tools["put_artifact"].ReadOnly || !tools["get_artifact"].ReadOnly || !tools["list_artifacts"].ReadOnly {
		t.Fatal("read-only flags wrong")
	}
	last := e.audit.events[len(e.audit.events)-1]
	if last.AgentID != "alice" || last.Role != model.RoleWorker || last.Tool != "put_artifact" {
		t.Fatalf("audit attributes the call to the agent: %+v", last)
	}
}

func TestLargeArtifactsAreServedByExpiringSignedURL(t *testing.T) {
	e := setup(t, nil)
	tools := toolSet(e)
	alice := model.Actor{ID: "alice", Role: model.RoleWorker}
	content := bytes.Repeat([]byte("0123456789"), 100)

	put, err := call(t, tools["put_artifact"], alice, map[string]any{
		"scope": "task:T-1", "name": "report.pdf", "content_type": "application/pdf",
		"content_base64": base64.StdEncoding.EncodeToString(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := put["artifact"].(map[string]any)["id"].(string)

	got, err := call(t, tools["get_artifact"], alice, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if _, inline := got["content_base64"]; inline {
		t.Fatal("large artifact must not be inlined")
	}
	link := got["url"].(string)
	if !strings.HasPrefix(link, "http://dl.test/dl/"+id+"?") {
		t.Fatalf("url %q", link)
	}
	path := strings.TrimPrefix(link, "http://dl.test")

	fetch := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.dl.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	rec := fetch(path)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("signed download: %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/pdf" || rec.Header().Get("Content-Disposition") != "attachment; filename=report.pdf" {
		t.Fatalf("headers: %v", rec.Header())
	}

	tampered := strings.Replace(path, "/dl/"+id, "/dl/A-999", 1)
	if c := fetch(tampered).Code; c != http.StatusForbidden {
		t.Fatalf("link for another id: %d", c)
	}
	if c := fetch(strings.Replace(path, "sig=", "sig=00", 1)).Code; c != http.StatusForbidden {
		t.Fatalf("bad signature: %d", c)
	}
	if c := fetch("/dl/" + id).Code; c != http.StatusForbidden {
		t.Fatalf("unsigned: %d", c)
	}

	e.clock.t = e.clock.t.Add(11 * time.Minute)
	if c := fetch(path).Code; c != http.StatusForbidden {
		t.Fatalf("expired link: %d", c)
	}
}

func TestLargeArtifactWithoutURLBaseFails(t *testing.T) {
	e := setup(t, func(c *artifacts.Config) { c.URLBase = "" })
	tools := toolSet(e)
	alice := model.Actor{ID: "alice", Role: model.RoleWorker}
	put, err := call(t, tools["put_artifact"], alice, map[string]any{
		"scope": "task:T-1", "name": "big", "content_base64": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 200)),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := put["artifact"].(map[string]any)["id"].(string)
	if _, err := call(t, tools["get_artifact"], alice, map[string]any{"id": id}); codeOf(err) != "bad_request" {
		t.Fatalf("got %v", err)
	}
}

func TestToolPutEnforcesLimits(t *testing.T) {
	e := setup(t, func(c *artifacts.Config) { c.MaxFileSize = 100 })
	alice := model.Actor{ID: "alice", Role: model.RoleWorker}
	_, err := call(t, toolSet(e)["put_artifact"], alice, map[string]any{
		"scope": "task:T-1", "name": "big", "content_base64": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 101)),
	})
	if codeOf(err) != artifacts.CodeTooLarge {
		t.Fatalf("got %v", err)
	}
	if n := len(blobFiles(t, e.root)); n != 0 {
		t.Fatalf("leftover files: %d", n)
	}
}

func TestConcurrentUploadsRespectQuota(t *testing.T) {
	e := setup(t, func(c *artifacts.Config) {
		c.MaxFileSize = 100
		c.ProjectQuota = 500
	})
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- e.upload(t, "project:B-1", "f", []byte(fmt.Sprintf("%0100d", i))).Code
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusCreated {
			ok++
		}
	}
	if ok != 5 {
		t.Fatalf("want exactly 5 uploads within the 500 byte quota, got %d", ok)
	}
}
