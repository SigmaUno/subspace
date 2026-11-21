package spawner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeEngine struct {
	mu        sync.Mutex
	seq       int
	bodies    map[string]createBody
	names     map[string]string
	running   map[string]bool
	failStart bool
	startMsg  string
	calls     []string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{bodies: map[string]createBody{}, names: map[string]string{}, running: map[string]bool{}}
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/containers/create":
		var b createBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, `{"message":"bad body"}`, http.StatusBadRequest)
			return
		}
		name := r.URL.Query().Get("name")
		for _, n := range f.names {
			if n == name {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"name in use"}`))
				return
			}
		}
		f.seq++
		id := fmt.Sprintf("c%d", f.seq)
		f.bodies[id] = b
		f.names[id] = name
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
		if f.failStart {
			w.WriteHeader(http.StatusInternalServerError)
			msg := f.startMsg
			if msg == "" {
				msg = "cannot start " + strings.Join(f.bodies[id].Env, " ")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
			return
		}
		f.running[id] = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/kill"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/kill")
		if !f.running[id] {
			http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
			return
		}
		delete(f.running, id)
		delete(f.bodies, id)
		delete(f.names, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimPrefix(path, "/containers/")
		delete(f.running, id)
		delete(f.bodies, id)
		delete(f.names, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == "/containers/json":
		var filters map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil {
			http.Error(w, `{"message":"bad filters"}`, http.StatusBadRequest)
			return
		}
		out := []engineContainer{}
		for id := range f.running {
			b := f.bodies[id]
			if matchLabels(b.Labels, filters["label"]) {
				out = append(out, engineContainer{ID: id, Image: b.Image, State: "running", Labels: b.Labels})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

func matchLabels(labels map[string]string, want []string) bool {
	for _, w := range want {
		k, v, hasVal := strings.Cut(w, "=")
		got, ok := labels[k]
		if !ok || (hasVal && got != v) {
			return false
		}
	}
	return true
}

func startEngine(t *testing.T, f *fakeEngine) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func dockerFixture(t *testing.T) (*Docker, *fakeEngine) {
	t.Helper()
	f := newFakeEngine()
	d := NewDocker(DockerConfig{
		Socket: startEngine(t, f),
		Images: map[string]string{
			"claude-code": "subspace/worker-claude:1",
			"codex":       "subspace/worker-codex:1",
			"opencode":    "subspace/worker-opencode:1",
		},
		CPUs:        1.5,
		MemoryBytes: 2 << 30,
		Network:     "subspace-egress",
	})
	return d, f
}

func TestDockerSpawnListStop(t *testing.T) {
	d, f := dockerFixture(t)
	ctx := context.Background()

	req := testRequest()
	req.AgentID = "claude-1"
	req.Kind = "claude-code"
	req.Env = map[string]string{"EXTRA": "1", "SUBSPACE_TOKEN": "spoofed"}
	if err := d.Spawn(ctx, req); err != nil {
		t.Fatal(err)
	}
	other := testRequest()
	other.AgentID = "codex-1"
	other.Kind = "codex"
	if err := d.Spawn(ctx, other); err != nil {
		t.Fatal(err)
	}

	var body createBody
	for _, b := range f.bodies {
		if b.Labels[LabelAgent] == "claude-1" {
			body = b
		}
	}
	if body.Image != "subspace/worker-claude:1" {
		t.Fatalf("image %q", body.Image)
	}
	wantEnv := []string{"EXTRA=1", "SUBSPACE_AGENT_ID=claude-1", "SUBSPACE_TOKEN=" + req.Token, "SUBSPACE_URL=" + req.MCPURL}
	if !slices.Equal(body.Env, wantEnv) {
		t.Fatalf("env %v", body.Env)
	}
	hc := body.HostConfig
	if !hc.AutoRemove || hc.NanoCpus != 1_500_000_000 || hc.Memory != 2<<30 || hc.NetworkMode != "subspace-egress" {
		t.Fatalf("hostconfig %+v", hc)
	}

	list, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].AgentID != "claude-1" || list[0].Kind != "claude-code" || list[1].AgentID != "codex-1" {
		t.Fatalf("list %+v", list)
	}

	if err := d.Stop(ctx, "claude-1"); err != nil {
		t.Fatal(err)
	}
	list, err = d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].AgentID != "codex-1" {
		t.Fatalf("list after stop %+v", list)
	}
	if err := d.Stop(ctx, "claude-1"); err != nil {
		t.Fatalf("stop of absent agent: %v", err)
	}
}

func TestDockerStartFailureCleansUpAndRedacts(t *testing.T) {
	d, f := dockerFixture(t)
	f.failStart = true
	req := testRequest()
	req.AgentID = "oc-1"
	req.Kind = "opencode"
	err := d.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), req.Token) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error leaks token or lacks redaction: %v", err)
	}
	if len(f.bodies) != 0 || len(f.names) != 0 {
		t.Fatalf("container not removed: %v", f.names)
	}
}

func TestDockerSpawnRejectsBadInput(t *testing.T) {
	d, f := dockerFixture(t)
	ctx := context.Background()

	req := testRequest()
	req.AgentID = "Bad ID"
	req.Kind = "codex"
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("invalid agent id accepted")
	}
	req.AgentID = "ok-1"
	req.Kind = "unknown"
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("unknown kind accepted")
	}
	req.Kind = "codex"
	req.Env = map[string]string{"BAD KEY": "x"}
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("invalid env key accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("engine called: %v", f.calls)
	}
}

func TestDockerDuplicateAgentConflict(t *testing.T) {
	d, _ := dockerFixture(t)
	req := testRequest()
	req.AgentID = "dup-1"
	req.Kind = "codex"
	if err := d.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := d.Spawn(context.Background(), req); err == nil || !strings.Contains(err.Error(), "name in use") {
		t.Fatalf("err %v", err)
	}
}

func TestDockerRedactsTokenAcrossTruncation(t *testing.T) {
	d, f := dockerFixture(t)
	f.failStart = true
	f.startMsg = strings.Repeat("x", 505) + testToken
	req := testRequest()
	req.Kind = "opencode"
	err := d.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken[:7]) {
		t.Fatalf("token prefix leaked: %v", err)
	}
}
