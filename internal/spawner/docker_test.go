package spawner

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeContainer struct {
	body  createBody
	name  string
	state string
	nets  []string
}

type fakeNetwork struct {
	id       string
	internal bool
	labels   map[string]string
}

type fakeEngine struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*fakeContainer
	networks   map[string]*fakeNetwork
	volumes    map[string]map[string]string
	logs       map[string][]byte
	raw        []string
	calls      []string
	failStart  bool
	startMsg   string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		containers: map[string]*fakeContainer{}, networks: map[string]*fakeNetwork{},
		volumes: map[string]map[string]string{}, logs: map[string][]byte{},
	}
}

func (f *fakeEngine) add(name, state string, labels map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("c%d", f.seq)
	f.containers[id] = &fakeContainer{body: createBody{Image: "x", Labels: labels}, name: name, state: state}
	return id
}

func (f *fakeEngine) byRef(ref string) (string, *fakeContainer) {
	if c, ok := f.containers[ref]; ok {
		return ref, c
	}
	for id, c := range f.containers {
		if c.name == ref {
			return id, c
		}
	}
	return "", nil
}

func (f *fakeEngine) get(ref string) (string, *fakeContainer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, c := f.byRef(ref)
	if c == nil {
		return "", nil
	}
	cp := *c
	return id, &cp
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.raw = append(f.raw, r.URL.String()+" "+string(body))
	path := r.URL.Path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && path == "/containers/create":
		var b createBody
		if err := json.Unmarshal(body, &b); err != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		name := r.URL.Query().Get("name")
		if id, _ := f.byRef(name); id != "" {
			writeErr(w, http.StatusConflict, "name in use")
			return
		}
		f.seq++
		id := fmt.Sprintf("c%d", f.seq)
		f.containers[id] = &fakeContainer{body: b, name: name, state: "created", nets: []string{b.HostConfig.NetworkMode}}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case r.Method == http.MethodGet && path == "/containers/json":
		var filters map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil {
			writeErr(w, http.StatusBadRequest, "bad filters")
			return
		}
		all := r.URL.Query().Get("all") == "1"
		out := []engineContainer{}
		for id, c := range f.containers {
			if (!all && c.state != "running") || !matchLabels(c.body.Labels, filters["label"]) {
				continue
			}
			if st := filters["status"]; len(st) > 0 && !slices.Contains(st, c.state) {
				continue
			}
			out = append(out, engineContainer{ID: id, Image: c.body.Image, State: c.state, Labels: c.body.Labels})
		}
		_ = json.NewEncoder(w).Encode(out)
	case len(parts) == 3 && parts[0] == "containers" && r.Method == http.MethodGet && parts[2] == "json":
		id, c := f.byRef(parts[1])
		if c == nil {
			writeErr(w, http.StatusNotFound, "no such container")
			return
		}
		nets := map[string]any{}
		for _, n := range c.nets {
			nets[n] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]bool{"Running": c.state == "running"},
			"NetworkSettings": map[string]any{"Networks": nets}})
	case len(parts) == 3 && parts[0] == "containers" && r.Method == http.MethodPost && parts[2] == "start":
		_, c := f.byRef(parts[1])
		if c == nil {
			writeErr(w, http.StatusNotFound, "no such container")
			return
		}
		if f.failStart && c.body.Labels[DefaultLabelPrefix+".role"] == "agent" {
			msg := f.startMsg
			if msg == "" {
				msg = "cannot start " + strings.Join(c.body.Env, " ")
			}
			writeErr(w, http.StatusInternalServerError, msg)
			return
		}
		c.state = "running"
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[0] == "containers" && r.Method == http.MethodPost && parts[2] == "kill":
		_, c := f.byRef(parts[1])
		switch {
		case c == nil:
			writeErr(w, http.StatusNotFound, "no such container")
		case c.state != "running":
			writeErr(w, http.StatusConflict, "container is not running")
		default:
			c.state = "exited"
			w.WriteHeader(http.StatusNoContent)
		}
	case len(parts) == 3 && parts[0] == "containers" && r.Method == http.MethodGet && parts[2] == "logs":
		_, c := f.byRef(parts[1])
		if c == nil {
			writeErr(w, http.StatusNotFound, "no such container")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(f.logs[parts[1]])
	case len(parts) == 2 && parts[0] == "containers" && r.Method == http.MethodDelete:
		id, c := f.byRef(parts[1])
		if c == nil {
			writeErr(w, http.StatusNotFound, "no such container")
			return
		}
		delete(f.containers, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && path == "/networks/create":
		var b struct {
			Name     string
			Internal bool
			Labels   map[string]string
		}
		_ = json.Unmarshal(body, &b)
		f.seq++
		f.networks[b.Name] = &fakeNetwork{id: fmt.Sprintf("n%d", f.seq), internal: b.Internal, labels: b.Labels}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"n"}`))
	case r.Method == http.MethodGet && path == "/networks":
		var filters map[string][]string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		out := []map[string]any{}
		for name, n := range f.networks {
			if matchLabels(n.labels, filters["label"]) {
				out = append(out, map[string]any{"Id": n.id, "Name": name})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case len(parts) == 2 && parts[0] == "networks" && r.Method == http.MethodGet:
		n, ok := f.networks[parts[1]]
		if !ok {
			writeErr(w, http.StatusNotFound, "network not found")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Internal": n.internal, "Labels": n.labels})
	case len(parts) == 2 && parts[0] == "networks" && r.Method == http.MethodDelete:
		for name, n := range f.networks {
			if n.id == parts[1] {
				delete(f.networks, name)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[0] == "networks" && parts[2] == "connect":
		var b struct{ Container string }
		_ = json.Unmarshal(body, &b)
		_, c := f.byRef(b.Container)
		if c == nil {
			writeErr(w, http.StatusNotFound, "no such container")
			return
		}
		c.nets = append(c.nets, parts[1])
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && path == "/volumes/create":
		var b struct {
			Name   string
			Labels map[string]string
		}
		_ = json.Unmarshal(body, &b)
		f.volumes[b.Name] = b.Labels
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && path == "/volumes":
		var filters map[string][]string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		vols := []map[string]any{}
		for name, labels := range f.volumes {
			if matchLabels(labels, filters["label"]) {
				vols = append(vols, map[string]any{"Name": name, "Labels": labels})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": vols})
	case len(parts) == 2 && parts[0] == "volumes" && r.Method == http.MethodDelete:
		delete(f.volumes, parts[1])
		w.WriteHeader(http.StatusNoContent)
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

func startEngine(t *testing.T, f http.Handler) string {
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

func dockerFixture(t *testing.T, mod func(*DockerConfig)) (*Docker, *fakeEngine) {
	t.Helper()
	f := newFakeEngine()
	cfg := DockerConfig{
		Socket: startEngine(t, f),
		Images: map[string][]string{
			"claude-code": {"subspace/worker-claude:1", "subspace/worker-claude:2"},
			"codex":       {"subspace/worker-codex:1"},
			"opencode":    {"subspace/worker-opencode:1"},
		},
		Limits:     Limits{CPUs: 1.5, MemoryBytes: 2 << 30},
		Profiles:   map[string]Limits{"codex": {CPUs: 4, Pids: 128}},
		SecretsDir: t.TempDir(),
		User:       fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	}
	if os.Getuid() == 0 {
		cfg.User = DefaultDockerUser
	}
	if mod != nil {
		mod(&cfg)
	}
	d, err := NewDocker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d, f
}

func (f *fakeEngine) agentBody(t *testing.T, agentID string) createBody {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.body.Labels[DefaultLabelPrefix+".agent"] == agentID {
			return c.body
		}
	}
	t.Fatalf("no container for %s", agentID)
	return createBody{}
}

func TestDockerSpawnRequestShape(t *testing.T) {
	d, f := dockerFixture(t, func(c *DockerConfig) {
		c.EgressImage = "subspace/egress:1"
		c.EgressAllow = []string{"github.com", "*.githubusercontent.com"}
		c.EgressPrivate = []string{"s.example.com"}
		c.RepoVolume = true
	})
	ctx := context.Background()
	req := testRequest()
	req.Env = map[string]string{"EXTRA": "1", "SUBSPACE_TOKEN": "spoofed", "https_proxy": "http://evil:1"}
	if err := d.Spawn(ctx, req); err != nil {
		t.Fatal(err)
	}

	for _, raw := range f.raw {
		if strings.Contains(raw, testToken) {
			t.Fatalf("token sent to engine: %s", raw)
		}
	}
	nw := f.networks[DefaultDockerNetwork]
	if nw == nil || !nw.internal || nw.labels["subspace.managed"] != "true" {
		t.Fatalf("network %+v", nw)
	}

	body := f.agentBody(t, req.AgentID)
	if body.Image != "subspace/worker-claude:1" || body.User != d.cfg.User || body.WorkingDir != AgentWorkdir {
		t.Fatalf("body %+v", body)
	}
	proxy := "http://subspace-egress:3128"
	wantEnv := []string{
		"EXTRA=1", "HOME=/work", "HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy,
		"SUBSPACE_AGENT_ID=" + req.AgentID, "SUBSPACE_JOIN_LINE=" + req.JoinLine, "SUBSPACE_KIND=claude-code",
		"SUBSPACE_MODEL=claude sonnet", "SUBSPACE_REPO=SigmaUno/foo", "SUBSPACE_TOKEN_FILE=" + AgentTokenPath,
		"SUBSPACE_URL=" + req.MCPURL, "http_proxy=" + proxy, "https_proxy=" + proxy,
	}
	if !slices.Equal(body.Env, wantEnv) {
		t.Fatalf("env\n got %q\nwant %q", body.Env, wantEnv)
	}
	if len(body.Cmd) != 0 {
		t.Fatalf("cmd %v", body.Cmd)
	}
	wantLabels := map[string]string{"subspace.managed": "true", "subspace.agent": req.AgentID, "subspace.kind": "claude-code", "subspace.role": "agent"}
	if fmt.Sprint(body.Labels) != fmt.Sprint(wantLabels) {
		t.Fatalf("labels %v", body.Labels)
	}

	hc := body.HostConfig
	if !hc.AutoRemove || !hc.ReadonlyRootfs || hc.Privileged || !hc.Init || hc.IpcMode != "private" {
		t.Fatalf("hostconfig %+v", hc)
	}
	if !slices.Equal(hc.CapDrop, []string{"ALL"}) || !slices.Equal(hc.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("caps %v secopt %v", hc.CapDrop, hc.SecurityOpt)
	}
	if hc.NanoCpus != 1_500_000_000 || hc.Memory != 2<<30 || hc.MemorySwap != 2<<30 || hc.PidsLimit != DefaultPidsLimit {
		t.Fatalf("limits %+v", hc)
	}
	if hc.NetworkMode != DefaultDockerNetwork || hc.LogConfig.Type != "json-file" {
		t.Fatalf("network %q log %+v", hc.NetworkMode, hc.LogConfig)
	}
	uid, gid, _ := parseUser(d.cfg.User)
	if want := fmt.Sprintf("rw,nosuid,nodev,size=%d,uid=%d,gid=%d,mode=0700", DefaultWorkdirBytes, uid, gid); hc.Tmpfs[AgentWorkdir] != want {
		t.Fatalf("tmpfs %v", hc.Tmpfs)
	}
	secret := filepath.Join(d.cfg.SecretsDir, req.AgentID, "token")
	wantMounts := []mountSpec{
		{Type: "bind", Source: secret, Target: AgentTokenPath, ReadOnly: true},
		{Type: "volume", Source: "subspace-repo-" + req.AgentID, Target: AgentRepoDir},
	}
	if !slices.Equal(hc.Mounts, wantMounts) {
		t.Fatalf("mounts %+v", hc.Mounts)
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != testToken {
		t.Fatalf("secret %q %v", data, err)
	}
	if st, _ := os.Stat(secret); st.Mode().Perm() != 0o400 {
		t.Fatalf("secret mode %v", st.Mode())
	}
	if f.volumes["subspace-repo-"+req.AgentID]["subspace.agent"] != req.AgentID {
		t.Fatalf("volumes %v", f.volumes)
	}

	_, eg := f.get("subspace-egress")
	if eg == nil || eg.state != "running" || !slices.Contains(eg.nets, DefaultDockerNetwork) || !slices.Contains(eg.nets, "bridge") {
		t.Fatalf("egress %+v", eg)
	}
	ehc := eg.body.HostConfig
	if ehc.AutoRemove || !ehc.ReadonlyRootfs || !slices.Equal(ehc.CapDrop, []string{"ALL"}) || ehc.RestartPolicy == nil || ehc.PidsLimit == 0 {
		t.Fatalf("egress hostconfig %+v", ehc)
	}
	wantCmd := []string{"-listen", ":3128", "-allow", "github.com,*.githubusercontent.com", "-private", "s.example.com"}
	if !slices.Equal(eg.body.Cmd, wantCmd) || eg.body.Labels["subspace.role"] != "egress" {
		t.Fatalf("egress %v %v", eg.body.Cmd, eg.body.Labels)
	}

	creates := 0
	other := testRequest()
	other.AgentID = "claude-2"
	if err := d.Spawn(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c == "POST /networks/create" || c == "POST /networks/"+DefaultDockerNetwork+"/connect" {
			creates++
		}
	}
	if creates != 2 {
		t.Fatalf("network setup repeated: %v", f.calls)
	}
}

func TestDockerImageAllowlistAndLimits(t *testing.T) {
	d, f := dockerFixture(t, nil)
	ctx := context.Background()

	req := testRequest()
	req.Image = "subspace/worker-claude:2"
	if err := d.Spawn(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := f.agentBody(t, req.AgentID).Image; got != req.Image {
		t.Fatalf("image %q", got)
	}

	before := len(f.calls)
	for _, img := range []string{"subspace/worker-codex:1", "evil/image:latest", "subspace/worker-claude:1@sha256:00"} {
		bad := testRequest()
		bad.AgentID = "bad-1"
		bad.Image = img
		if err := d.Spawn(ctx, bad); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: %v", img, err)
		}
	}
	if len(f.calls) != before {
		t.Fatalf("engine called for rejected image: %v", f.calls[before:])
	}

	cases := []struct {
		id, kind string
		req      Limits
		want     Limits
	}{
		{"oc-1", "opencode", Limits{}, Limits{CPUs: 1.5, MemoryBytes: 2 << 30, Pids: DefaultPidsLimit}},
		{"cx-1", "codex", Limits{}, Limits{CPUs: 4, MemoryBytes: 2 << 30, Pids: 128}},
		{"cx-2", "codex", Limits{CPUs: 0.5, MemoryBytes: 512 << 20, Pids: 64}, Limits{CPUs: 0.5, MemoryBytes: 512 << 20, Pids: 64}},
	}
	for _, c := range cases {
		r := testRequest()
		r.AgentID, r.Kind, r.Limits = c.id, c.kind, c.req
		if err := d.Spawn(ctx, r); err != nil {
			t.Fatal(err)
		}
		hc := f.agentBody(t, c.id).HostConfig
		if hc.NanoCpus != int64(c.want.CPUs*1e9) || hc.Memory != c.want.MemoryBytes || hc.PidsLimit != c.want.Pids {
			t.Fatalf("%s: %+v", c.id, hc)
		}
	}
	neg := testRequest()
	neg.AgentID = "neg-1"
	neg.Limits = Limits{Pids: -1}
	if err := d.Spawn(ctx, neg); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestDockerSpawnRejectsBadInput(t *testing.T) {
	d, f := dockerFixture(t, nil)
	ctx := context.Background()

	req := testRequest()
	req.AgentID = "Bad ID"
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("invalid agent id accepted")
	}
	req = testRequest()
	req.Kind = "unknown"
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("unknown kind accepted")
	}
	req = testRequest()
	req.Env = map[string]string{"BAD KEY": "x"}
	if err := d.Spawn(ctx, req); err == nil {
		t.Fatal("invalid env key accepted")
	}
	req = testRequest()
	req.Env = map[string]string{"GIT_AUTH": "Bearer " + testToken}
	if err := d.Spawn(ctx, req); err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in env accepted: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("engine called: %v", f.calls)
	}
	for _, user := range []string{"0:0", "root", "1000", "-1:5"} {
		if _, err := NewDocker(DockerConfig{User: user}); err == nil {
			t.Fatalf("user %q accepted", user)
		}
	}
}

func TestDockerRefusesNonInternalNetwork(t *testing.T) {
	d, f := dockerFixture(t, nil)
	f.networks[DefaultDockerNetwork] = &fakeNetwork{id: "n0", internal: false}
	if err := d.Spawn(context.Background(), testRequest()); err == nil || !strings.Contains(err.Error(), "not internal") {
		t.Fatalf("err %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("container created on open network")
	}
}

func TestDockerStatusStopByLabels(t *testing.T) {
	d, f := dockerFixture(t, nil)
	ctx := context.Background()
	req := testRequest()
	if err := d.Spawn(ctx, req); err != nil {
		t.Fatal(err)
	}
	foreign := f.add("other-"+req.AgentID, "running", map[string]string{"other.managed": "true", "other.agent": req.AgentID})
	unmanaged := f.add("lookalike", "running", map[string]string{"subspace.agent": req.AgentID})

	if st, err := d.Status(ctx, req.AgentID); err != nil || st != StatusRunning {
		t.Fatalf("status %v %v", st, err)
	}
	list, err := d.List(ctx)
	if err != nil || len(list) != 1 || list[0].AgentID != req.AgentID || list[0].Kind != "claude-code" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := d.Stop(ctx, req.AgentID); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(ctx, req.AgentID); err != nil || st != StatusExited {
		t.Fatalf("status after stop %v %v", st, err)
	}
	if _, c := f.get(foreign); c == nil || c.state != "running" {
		t.Fatal("foreign container touched")
	}
	if _, c := f.get(unmanaged); c == nil || c.state != "running" {
		t.Fatal("unmanaged container touched")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.SecretsDir, req.AgentID)); !os.IsNotExist(err) {
		t.Fatalf("secret kept after stop: %v", err)
	}
	if err := d.Stop(ctx, req.AgentID); err != nil {
		t.Fatalf("stop of absent agent: %v", err)
	}

	f.add("subspace-dead", "exited", map[string]string{"subspace.managed": "true", "subspace.agent": "dead-1"})
	if st, err := d.Status(ctx, "dead-1"); err != nil || st != StatusExited {
		t.Fatalf("exited container status %v %v", st, err)
	}
	f.add("subspace-new", "created", map[string]string{"subspace.managed": "true", "subspace.agent": "new-1"})
	if st, err := d.Status(ctx, "new-1"); err != nil || st != StatusRunning {
		t.Fatalf("created container status %v %v", st, err)
	}
}

func TestDockerStartFailureCleansUpAndRedacts(t *testing.T) {
	d, f := dockerFixture(t, nil)
	f.failStart = true
	f.startMsg = "boom " + testToken
	req := testRequest()
	err := d.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), req.Token) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error leaks token or lacks redaction: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("container not removed: %v", f.containers)
	}
	if _, err := os.Stat(filepath.Join(d.cfg.SecretsDir, req.AgentID)); !os.IsNotExist(err) {
		t.Fatalf("secret kept after failed start: %v", err)
	}
}

func TestDockerRedactsTokenAcrossTruncation(t *testing.T) {
	d, f := dockerFixture(t, nil)
	f.failStart = true
	f.startMsg = strings.Repeat("x", 505) + testToken
	err := d.Spawn(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken[:7]) {
		t.Fatalf("token prefix leaked: %v", err)
	}
}

func TestDockerDuplicateAgentConflict(t *testing.T) {
	d, _ := dockerFixture(t, nil)
	req := testRequest()
	if err := d.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := d.Spawn(context.Background(), req); err == nil || !strings.Contains(err.Error(), "name in use") {
		t.Fatalf("err %v", err)
	}
}

func TestDockerCleanupOrphans(t *testing.T) {
	d, f := dockerFixture(t, func(c *DockerConfig) { c.RepoVolume = true })
	ctx := context.Background()
	live := testRequest()
	live.AgentID = "live-1"
	if err := d.Spawn(ctx, live); err != nil {
		t.Fatal(err)
	}
	dead := f.add("subspace-dead-1", "exited", map[string]string{"subspace.managed": "true", "subspace.agent": "dead-1"})
	foreign := f.add("other-dead", "exited", map[string]string{"other.managed": "true", "other.agent": "dead-1"})
	f.networks["subspace-old"] = &fakeNetwork{id: "n-old", labels: map[string]string{"subspace.managed": "true"}}
	f.networks["unrelated"] = &fakeNetwork{id: "n-x", labels: map[string]string{"other.managed": "true"}}
	f.volumes["subspace-repo-dead-1"] = map[string]string{"subspace.managed": "true", "subspace.agent": "dead-1"}
	f.volumes["someone-else"] = map[string]string{"app": "db"}
	if err := os.MkdirAll(filepath.Join(d.cfg.SecretsDir, "dead-1"), 0o700); err != nil {
		t.Fatal(err)
	}

	rep, err := d.Cleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (CleanupReport{Containers: 1, Networks: 1, Volumes: 1, Secrets: 1}) {
		t.Fatalf("report %+v", rep)
	}
	if _, c := f.get(dead); c != nil {
		t.Fatal("orphaned container kept")
	}
	if _, c := f.get(foreign); c == nil {
		t.Fatal("foreign container removed")
	}
	if f.networks[DefaultDockerNetwork] == nil || f.networks["unrelated"] == nil || f.networks["subspace-old"] != nil {
		t.Fatalf("networks %v", f.networks)
	}
	if f.volumes["subspace-repo-live-1"] == nil || f.volumes["someone-else"] == nil || f.volumes["subspace-repo-dead-1"] != nil {
		t.Fatalf("volumes %v", f.volumes)
	}
	if _, err := os.Stat(filepath.Join(d.cfg.SecretsDir, "live-1", "token")); err != nil {
		t.Fatalf("live secret removed: %v", err)
	}
	if st, _ := d.Status(ctx, "live-1"); st != StatusRunning {
		t.Fatal("live agent stopped by cleanup")
	}
}

func frame(stream byte, s string) []byte {
	b := make([]byte, 8, 8+len(s))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:], uint32(len(s)))
	return append(b, s...)
}

type sinkRec struct {
	mu  sync.Mutex
	got []string
}

func (s *sinkRec) sink(agent, stream, text string) {
	s.mu.Lock()
	s.got = append(s.got, agent+"|"+stream+"|"+text)
	s.mu.Unlock()
}

func (s *sinkRec) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.got) >= n {
			out := slices.Clone(s.got)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sink calls: %v", n, s.got)
	return nil
}

func TestDockerWatchLogsDemuxesAndBatches(t *testing.T) {
	d, f := dockerFixture(t, func(c *DockerConfig) { c.LogPoll = 20 * time.Millisecond })
	req := testRequest()
	if err := d.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	id, _ := f.get("subspace-" + req.AgentID)
	var logs []byte
	logs = append(logs, frame(1, "hello\nwor")...)
	logs = append(logs, frame(1, "ld\r\n")...)
	logs = append(logs, frame(2, "oops\n")...)
	logs = append(logs, frame(1, "tail")...)
	f.mu.Lock()
	f.logs[id] = logs
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rec sinkRec
	go d.WatchLogs(ctx, rec.sink)
	got := rec.wait(t, 6)
	want := []string{req.AgentID + "|stdout|hello\nworld", req.AgentID + "|stderr|oops", req.AgentID + "|stdout|tail"}
	if !slices.Equal(got[:3], want) {
		t.Fatalf("got %q", got)
	}
	f.mu.Lock()
	var since string
	for _, r := range f.raw {
		if strings.Contains(r, "/logs?") && strings.Contains(r, "since=") {
			since = r
		}
	}
	f.mu.Unlock()
	if since == "" {
		t.Fatal("restarted follower did not resume with since")
	}
}

func TestDockerLogRateLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	d, f := dockerFixture(t, func(c *DockerConfig) {
		c.LogRate, c.LogBurst = 1, 5
		c.Now = func() time.Time { return now }
	})
	req := testRequest()
	if err := d.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	id, _ := f.get("subspace-" + req.AgentID)
	var sb strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	f.mu.Lock()
	f.logs[id] = frame(1, sb.String())
	f.mu.Unlock()

	var rec sinkRec
	d.pump(context.Background(), Container{ID: id, AgentID: req.AgentID}, 0, rec.sink)
	want := []string{
		req.AgentID + "|stdout|line 0\nline 1\nline 2\nline 3\nline 4",
		req.AgentID + "|note|[subspace: 995 log lines dropped by rate limit]",
	}
	if !slices.Equal(rec.got, want) {
		t.Fatalf("got %q", rec.got)
	}
}

func TestDockerFromEnv(t *testing.T) {
	env := map[string]string{
		"SUBSPACE_DOCKER_IMAGES":       "claude-code=a/claude:1,a/claude:2;codex=a/codex:1",
		"SUBSPACE_DOCKER_LIMITS":       "cpus:2,memory:4g,pids:256",
		"SUBSPACE_DOCKER_PROFILES":     "codex=cpus:1,memory:512m",
		"SUBSPACE_DOCKER_EGRESS_IMAGE": "a/egress:1",
		"SUBSPACE_DOCKER_EGRESS_ALLOW": "github.com, *.github.com",
		"SUBSPACE_DOCKER_SECRETS_DIR":  "/var/lib/subspace/secrets",
		"SUBSPACE_DOCKER_LOG_RATE":     "20:100",
		"SUBSPACE_PUBLIC_URL":          "https://Sub.Example.com:8443",
	}
	s, err := New("docker", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	d := s.(*Docker)
	c := d.cfg
	if !slices.Equal(c.Images["claude-code"], []string{"a/claude:1", "a/claude:2"}) || c.Limits != (Limits{CPUs: 2, MemoryBytes: 4 << 30, Pids: 256}) ||
		c.Profiles["codex"] != (Limits{CPUs: 1, MemoryBytes: 512 << 20}) || c.LogRate != 20 || c.LogBurst != 100 {
		t.Fatalf("cfg %+v", c)
	}
	if !slices.Equal(c.EgressAllow, []string{"github.com", "*.github.com", "sub.example.com"}) || !slices.Equal(c.EgressPrivate, []string{"sub.example.com"}) {
		t.Fatalf("egress %v %v", c.EgressAllow, c.EgressPrivate)
	}
	for k, v := range map[string]string{
		"SUBSPACE_DOCKER_IMAGES":       "claude-code",
		"SUBSPACE_DOCKER_LIMITS":       "cpus:lots",
		"SUBSPACE_DOCKER_PROFILES":     "codex=gpus:1",
		"SUBSPACE_DOCKER_USER":         "root",
		"SUBSPACE_DOCKER_EGRESS_ALLOW": "10.0.0.1",
		"SUBSPACE_DOCKER_LOG_RATE":     "fast",
	} {
		bad := map[string]string{k: v}
		if _, err := New("docker", func(k string) string { return bad[k] }); err == nil {
			t.Fatalf("%s=%s accepted", k, v)
		}
	}
}

func TestDockerPassesGitCredentialsButNotOtherServerEnv(t *testing.T) {
	d, f := dockerFixture(t, nil)
	req := testRequest()
	req.Env = map[string]string{"SUBSPACE_GIT_TOKEN": "git-credential-for-tests-0123456789", "SUBSPACE_TOKEN_FILE": "/etc/shadow", "GH_TOKEN": "gh"}
	if err := d.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	env := f.agentBody(t, req.AgentID).Env
	for _, want := range []string{"SUBSPACE_GIT_TOKEN=git-credential-for-tests-0123456789", "GH_TOKEN=gh", "SUBSPACE_TOKEN_FILE=" + AgentTokenPath} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q: %q", want, env)
		}
	}
	if slices.Contains(env, "SUBSPACE_TOKEN_FILE=/etc/shadow") {
		t.Fatal("caller overrode a reserved variable")
	}
}
