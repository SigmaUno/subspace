package spawner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	DefaultDockerSocket  = "/var/run/docker.sock"
	DefaultLabelPrefix   = "subspace"
	DefaultDockerUser    = "65532:65532"
	DefaultDockerNetwork = "subspace-agents"
	DefaultEgressNetwork = "bridge"
	DefaultEgressAlias   = "subspace-egress"
	DefaultEgressPort    = 3128
	DefaultPidsLimit     = 512
	DefaultWorkdirBytes  = 2 << 30
	DefaultTmpBytes      = 256 << 20

	AgentWorkdir   = "/work"
	AgentRepoDir   = "/work/repo"
	AgentTokenPath = "/run/secrets/subspace_token" //nolint:gosec // file path, not a credential

	engineHost       = "http://docker"
	maxEngineReply   = 4 << 20
	defaultEngineTTL = 30 * time.Second
	secretFile       = "token"
)

type DockerConfig struct {
	Socket         string
	Images         map[string][]string
	Profiles       map[string]Limits
	Limits         Limits
	User           string
	Cmd            []string
	LabelPrefix    string
	Network        string
	EgressImage    string
	EgressNetwork  string
	EgressAllow    []string
	EgressPrivate  []string
	RepoVolume     bool
	SecretsDir     string
	HostSecretsDir string
	WorkdirBytes   int64
	LogPoll        time.Duration
	LogRate        float64
	LogBurst       int
	Now            func() time.Time
}

type Container struct {
	ID      string
	AgentID string
	Kind    string
	Role    string
	Image   string
	State   string
}

type Docker struct {
	cfg    DockerConfig
	uid    int
	gid    int
	client *http.Client
	stream *http.Client

	mu    sync.Mutex
	ready bool
}

func NewDocker(cfg DockerConfig) (*Docker, error) {
	if cfg.Socket == "" {
		cfg.Socket = DefaultDockerSocket
	}
	if cfg.User == "" {
		cfg.User = DefaultDockerUser
	}
	if cfg.LabelPrefix == "" {
		cfg.LabelPrefix = DefaultLabelPrefix
	}
	if cfg.Network == "" {
		cfg.Network = DefaultDockerNetwork
	}
	if cfg.EgressNetwork == "" {
		cfg.EgressNetwork = DefaultEgressNetwork
	}
	if cfg.HostSecretsDir == "" {
		cfg.HostSecretsDir = cfg.SecretsDir
	}
	if cfg.WorkdirBytes <= 0 {
		cfg.WorkdirBytes = DefaultWorkdirBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	uid, gid, err := parseUser(cfg.User)
	if err != nil {
		return nil, err
	}
	if !labelPrefixRe.MatchString(cfg.LabelPrefix) {
		return nil, fmt.Errorf("spawner docker: invalid label prefix %q", cfg.LabelPrefix)
	}
	if !resourceNameRe.MatchString(cfg.Network) || !resourceNameRe.MatchString(cfg.EgressNetwork) {
		return nil, errors.New("spawner docker: invalid network name")
	}
	for kind, imgs := range cfg.Images {
		for _, img := range imgs {
			if !imageRe.MatchString(img) {
				return nil, fmt.Errorf("spawner docker: invalid image %q for kind %q", img, kind)
			}
		}
	}
	for _, h := range append(slices.Clone(cfg.EgressAllow), cfg.EgressPrivate...) {
		if _, err := netip.ParseAddr(h); err == nil || !egressHostRe.MatchString(h) {
			return nil, fmt.Errorf("spawner docker: invalid egress host %q", h)
		}
	}
	if err := cfg.Limits.validate(); err != nil {
		return nil, err
	}
	for _, l := range cfg.Profiles {
		if err := l.validate(); err != nil {
			return nil, err
		}
	}
	socket := cfg.Socket
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Docker{
		cfg: cfg, uid: uid, gid: gid,
		client: &http.Client{Timeout: defaultEngineTTL, Transport: &http.Transport{DialContext: dial, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}},
	}, nil
}

func parseUser(s string) (int, int, error) {
	u, g, ok := strings.Cut(s, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid <= 0 || gid <= 0 {
		return 0, 0, fmt.Errorf("spawner docker: user must be a non-root numeric uid:gid, got %q", s)
	}
	return uid, gid, nil
}

func (l Limits) validate() error {
	if l.CPUs < 0 || l.MemoryBytes < 0 || l.Pids < 0 {
		return errors.New("spawner docker: limits must not be negative")
	}
	if l.MemoryBytes > 0 && l.MemoryBytes < 6<<20 {
		return errors.New("spawner docker: memory limit must be at least 6MiB")
	}
	return nil
}

func (l Limits) over(o Limits) Limits {
	if o.CPUs > 0 {
		l.CPUs = o.CPUs
	}
	if o.MemoryBytes > 0 {
		l.MemoryBytes = o.MemoryBytes
	}
	if o.Pids > 0 {
		l.Pids = o.Pids
	}
	return l
}

func (*Docker) Name() string { return "docker" }

func (d *Docker) label(k string) string { return d.cfg.LabelPrefix + "." + k }

func (d *Docker) labels(extra map[string]string) map[string]string {
	m := map[string]string{d.label("managed"): "true"}
	for k, v := range extra {
		m[d.label(k)] = v
	}
	return m
}

type createBody struct {
	Image      string            `json:"Image"`
	User       string            `json:"User"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Env        []string          `json:"Env"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	AutoRemove     bool              `json:"AutoRemove"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Privileged     bool              `json:"Privileged"`
	Init           bool              `json:"Init"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	NanoCpus       int64             `json:"NanoCpus,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	MemorySwap     int64             `json:"MemorySwap,omitempty"`
	PidsLimit      int64             `json:"PidsLimit,omitempty"`
	NetworkMode    string            `json:"NetworkMode"`
	IpcMode        string            `json:"IpcMode,omitempty"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Mounts         []mountSpec       `json:"Mounts,omitempty"`
	LogConfig      logConfig         `json:"LogConfig"`
	RestartPolicy  *restartPolicy    `json:"RestartPolicy,omitempty"`
}

type mountSpec struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
}

type logConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type engineContainer struct {
	ID     string            `json:"Id"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func (d *Docker) hardened(l Limits) hostConfig {
	hc := hostConfig{
		ReadonlyRootfs: true,
		Init:           true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		NanoCpus:       int64(l.CPUs * 1e9),
		Memory:         l.MemoryBytes,
		PidsLimit:      l.Pids,
		IpcMode:        "private",
		LogConfig:      logConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
	}
	if l.MemoryBytes > 0 {
		hc.MemorySwap = l.MemoryBytes
	}
	return hc
}

func (d *Docker) limitsFor(r Request) (Limits, error) {
	if err := r.Limits.validate(); err != nil {
		return Limits{}, err
	}
	l := Limits{Pids: DefaultPidsLimit}.over(d.cfg.Limits).over(d.cfg.Profiles[r.Kind]).over(r.Limits)
	return l, l.validate()
}

func (d *Docker) imageFor(r Request) (string, error) {
	allowed := d.cfg.Images[r.Kind]
	if len(allowed) == 0 {
		return "", fmt.Errorf("spawner docker: no image configured for kind %q", r.Kind)
	}
	if r.Image == "" {
		return allowed[0], nil
	}
	if !slices.Contains(allowed, r.Image) {
		return "", fmt.Errorf("spawner docker: image %q is not allowed for kind %q", r.Image, r.Kind)
	}
	return r.Image, nil
}

func (d *Docker) agentName(agentID string) string { return d.cfg.LabelPrefix + "-" + agentID }

func (d *Docker) repoVolume(agentID string) string { return d.cfg.LabelPrefix + "-repo-" + agentID }

func (d *Docker) egressName() string { return d.cfg.LabelPrefix + "-egress" }

func (d *Docker) Spawn(ctx context.Context, r Request) error {
	if d.client == nil {
		return ErrNotImplemented
	}
	if !agentIDRe.MatchString(r.AgentID) {
		return fmt.Errorf("spawner docker: invalid agent id %q", r.AgentID)
	}
	if r.Token == "" {
		return errors.New("spawner docker: token is required")
	}
	image, err := d.imageFor(r)
	if err != nil {
		return err
	}
	lim, err := d.limitsFor(r)
	if err != nil {
		return err
	}
	env, err := d.agentEnv(r)
	if err != nil {
		return err
	}
	if d.cfg.SecretsDir == "" {
		return errors.New("spawner docker: a secrets directory is required")
	}
	if err := d.ensureReady(ctx); err != nil {
		return err
	}
	secret, err := d.writeSecret(r.AgentID, r.Token)
	if err != nil {
		return err
	}
	hc := d.hardened(lim)
	hc.AutoRemove = true
	hc.NetworkMode = d.cfg.Network
	hc.Tmpfs = map[string]string{
		AgentWorkdir: fmt.Sprintf("rw,nosuid,nodev,size=%d,uid=%d,gid=%d,mode=0700", d.cfg.WorkdirBytes, d.uid, d.gid),
		"/tmp":       fmt.Sprintf("rw,nosuid,nodev,size=%d,mode=1777", DefaultTmpBytes),
	}
	hc.Mounts = []mountSpec{{Type: "bind", Source: secret, Target: AgentTokenPath, ReadOnly: true}}
	if d.cfg.RepoVolume {
		name := d.repoVolume(r.AgentID)
		vol := map[string]any{"Name": name, "Labels": d.labels(map[string]string{"agent": r.AgentID})}
		if err := d.do(ctx, r.Token, http.MethodPost, "/volumes/create", vol, nil); err != nil {
			d.removeSecret(r.AgentID)
			return fmt.Errorf("spawner docker: volume: %w", err)
		}
		hc.Mounts = append(hc.Mounts, mountSpec{Type: "volume", Source: name, Target: AgentRepoDir})
	}
	body := createBody{
		Image:      image,
		User:       d.cfg.User,
		WorkingDir: AgentWorkdir,
		Env:        env,
		Cmd:        d.cfg.Cmd,
		Labels:     d.labels(map[string]string{"agent": r.AgentID, "kind": r.Kind, "role": "agent"}),
		HostConfig: hc,
	}
	var created struct {
		ID string `json:"Id"`
	}
	q := url.Values{"name": {d.agentName(r.AgentID)}}
	if err := d.do(ctx, r.Token, http.MethodPost, "/containers/create?"+q.Encode(), body, &created); err != nil {
		d.removeSecret(r.AgentID)
		return fmt.Errorf("spawner docker: create: %w", err)
	}
	if err := d.do(ctx, r.Token, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = d.do(cleanup, r.Token, http.MethodDelete, "/containers/"+created.ID+"?force=true", nil, nil)
		d.removeSecret(r.AgentID)
		return fmt.Errorf("spawner docker: start: %w", err)
	}
	return nil
}

func (d *Docker) agentEnv(r Request) ([]string, error) {
	m := map[string]string{}
	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("spawner docker: invalid env key %q", k)
		}
		if strings.Contains(v, r.Token) && r.Token != "" {
			return nil, fmt.Errorf("spawner docker: env %s carries the agent token", k)
		}
		if reservedEnv(k) {
			continue
		}
		m[k] = v
	}
	m["HOME"] = AgentWorkdir
	m["SUBSPACE_AGENT_ID"] = r.AgentID
	m["SUBSPACE_KIND"] = r.Kind
	m["SUBSPACE_MODEL"] = r.Model
	m["SUBSPACE_URL"] = r.MCPURL
	m["SUBSPACE_JOIN_LINE"] = r.JoinLine
	m["SUBSPACE_TOKEN_FILE"] = AgentTokenPath
	if r.Repo != "" {
		m["SUBSPACE_REPO"] = r.Repo
	}
	if d.cfg.EgressImage != "" {
		proxy := fmt.Sprintf("http://%s:%d", DefaultEgressAlias, DefaultEgressPort)
		for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
			m[k] = proxy
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+m[k])
	}
	return env, nil
}

func reservedEnv(k string) bool {
	u := strings.ToUpper(k)
	return strings.HasPrefix(u, "SUBSPACE_") || u == "HOME" || u == "HTTPS_PROXY" || u == "HTTP_PROXY" ||
		u == "NO_PROXY" || u == "ALL_PROXY"
}

func (d *Docker) writeSecret(agentID, token string) (string, error) {
	dir := filepath.Join(d.cfg.SecretsDir, agentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("spawner docker: secret dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return "", fmt.Errorf("spawner docker: secret: %w", err)
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("spawner docker: secret: %w", err)
	}
	if _, err := f.WriteString(token); err != nil {
		return fail(err)
	}
	if err := f.Chmod(0o400); err != nil {
		return fail(err)
	}
	if os.Getuid() != d.uid {
		if err := f.Chown(d.uid, d.gid); err != nil {
			return fail(err)
		}
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, secretFile)); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("spawner docker: secret: %w", err)
	}
	return filepath.Join(d.cfg.HostSecretsDir, agentID, secretFile), nil
}

func (d *Docker) removeSecret(agentID string) {
	if d.cfg.SecretsDir == "" || !agentIDRe.MatchString(agentID) {
		return
	}
	_ = os.RemoveAll(filepath.Join(d.cfg.SecretsDir, agentID))
}

func (d *Docker) ensureReady(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready {
		return nil
	}
	if err := d.ensureNetwork(ctx); err != nil {
		return err
	}
	if d.cfg.EgressImage != "" {
		if err := d.ensureEgress(ctx); err != nil {
			return err
		}
	}
	d.ready = true
	return nil
}

func (d *Docker) ensureNetwork(ctx context.Context) error {
	var nw struct {
		Internal bool              `json:"Internal"`
		Labels   map[string]string `json:"Labels"`
	}
	err := d.do(ctx, "", http.MethodGet, "/networks/"+url.PathEscape(d.cfg.Network), nil, &nw)
	if err == nil {
		if !nw.Internal {
			return fmt.Errorf("spawner docker: network %s exists and is not internal", d.cfg.Network)
		}
		return nil
	}
	if !isStatus(err, http.StatusNotFound) {
		return fmt.Errorf("spawner docker: network: %w", err)
	}
	body := map[string]any{
		"Name": d.cfg.Network, "Driver": "bridge", "Internal": true, "CheckDuplicate": true,
		"Labels": d.labels(map[string]string{"role": "network"}),
	}
	if err := d.do(ctx, "", http.MethodPost, "/networks/create", body, nil); err != nil && !isStatus(err, http.StatusConflict) {
		return fmt.Errorf("spawner docker: network: %w", err)
	}
	return nil
}

func (d *Docker) egressCmd() []string {
	cmd := []string{"-listen", ":" + strconv.Itoa(DefaultEgressPort)}
	if len(d.cfg.EgressAllow) > 0 {
		cmd = append(cmd, "-allow", strings.Join(d.cfg.EgressAllow, ","))
	}
	if len(d.cfg.EgressPrivate) > 0 {
		cmd = append(cmd, "-private", strings.Join(d.cfg.EgressPrivate, ","))
	}
	return cmd
}

func (d *Docker) ensureEgress(ctx context.Context) error {
	name := d.egressName()
	var info struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	err := d.do(ctx, "", http.MethodGet, "/containers/"+name+"/json", nil, &info)
	switch {
	case isStatus(err, http.StatusNotFound):
		hc := d.hardened(Limits{CPUs: 1, MemoryBytes: 256 << 20, Pids: 256})
		hc.NetworkMode = d.cfg.EgressNetwork
		hc.RestartPolicy = &restartPolicy{Name: "unless-stopped"}
		body := createBody{
			Image: d.cfg.EgressImage, User: d.cfg.User, Env: []string{}, Cmd: d.egressCmd(),
			Labels: d.labels(map[string]string{"role": "egress"}), HostConfig: hc,
		}
		var created struct {
			ID string `json:"Id"`
		}
		if err := d.do(ctx, "", http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body, &created); err != nil {
			return fmt.Errorf("spawner docker: egress create: %w", err)
		}
		info.ID = created.ID
	case err != nil:
		return fmt.Errorf("spawner docker: egress inspect: %w", err)
	}
	if _, ok := info.NetworkSettings.Networks[d.cfg.Network]; !ok {
		conn := map[string]any{"Container": info.ID, "EndpointConfig": map[string]any{"Aliases": []string{DefaultEgressAlias}}}
		if err := d.do(ctx, "", http.MethodPost, "/networks/"+url.PathEscape(d.cfg.Network)+"/connect", conn, nil); err != nil {
			return fmt.Errorf("spawner docker: egress connect: %w", err)
		}
	}
	if !info.State.Running {
		if err := d.do(ctx, "", http.MethodPost, "/containers/"+info.ID+"/start", nil, nil); err != nil && !isStatus(err, http.StatusNotModified) {
			return fmt.Errorf("spawner docker: egress start: %w", err)
		}
	}
	return nil
}

func (d *Docker) Stop(ctx context.Context, agentID string) error {
	if d.client == nil {
		return ErrNotImplemented
	}
	if !agentIDRe.MatchString(agentID) {
		return fmt.Errorf("spawner docker: invalid agent id %q", agentID)
	}
	list, err := d.list(ctx, d.label("agent")+"="+agentID, true)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := d.do(ctx, "", http.MethodPost, "/containers/"+c.ID+"/kill", nil, nil); err != nil &&
			!isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusConflict) {
			return fmt.Errorf("spawner docker: kill: %w", err)
		}
		if err := d.do(ctx, "", http.MethodDelete, "/containers/"+c.ID+"?force=true", nil, nil); err != nil &&
			!isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusConflict) {
			return fmt.Errorf("spawner docker: remove: %w", err)
		}
	}
	d.removeSecret(agentID)
	return nil
}

func (d *Docker) Status(ctx context.Context, agentID string) (Status, error) {
	if d.client == nil {
		return StatusUnknown, ErrNotImplemented
	}
	if !agentIDRe.MatchString(agentID) {
		return StatusUnknown, fmt.Errorf("spawner docker: invalid agent id %q", agentID)
	}
	list, err := d.list(ctx, d.label("agent")+"="+agentID, true)
	if err != nil {
		return StatusUnknown, err
	}
	for _, c := range list {
		if alive(c.State) {
			return StatusRunning, nil
		}
	}
	return StatusExited, nil
}

func alive(state string) bool {
	switch state {
	case "created", "running", "restarting", "paused":
		return true
	}
	return false
}

func (d *Docker) List(ctx context.Context) ([]Container, error) {
	if d.client == nil {
		return nil, ErrNotImplemented
	}
	return d.list(ctx, d.label("agent"), false)
}

func (d *Docker) list(ctx context.Context, label string, all bool) ([]Container, error) {
	f := map[string][]string{"label": {d.label("managed") + "=true", label}}
	if !all {
		f["status"] = []string{"running"}
	}
	filters, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var raw []engineContainer
	q := url.Values{"filters": {string(filters)}}
	if all {
		q.Set("all", "1")
	}
	if err := d.do(ctx, "", http.MethodGet, "/containers/json?"+q.Encode(), nil, &raw); err != nil {
		return nil, fmt.Errorf("spawner docker: list: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, c := range raw {
		out = append(out, Container{
			ID: c.ID, AgentID: c.Labels[d.label("agent")], Kind: c.Labels[d.label("kind")],
			Role: c.Labels[d.label("role")], Image: c.Image, State: c.State,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

type CleanupReport struct {
	Containers int
	Networks   int
	Volumes    int
	Secrets    int
}

func (d *Docker) Cleanup(ctx context.Context) (CleanupReport, error) {
	var rep CleanupReport
	if d.client == nil {
		return rep, ErrNotImplemented
	}
	list, err := d.list(ctx, d.label("managed")+"=true", true)
	if err != nil {
		return rep, err
	}
	d.mu.Lock()
	d.ready = false
	d.mu.Unlock()
	live := map[string]bool{}
	for _, c := range list {
		if alive(c.State) {
			if c.AgentID != "" {
				live[c.AgentID] = true
			}
			continue
		}
		err := d.do(ctx, "", http.MethodDelete, "/containers/"+c.ID+"?force=true", nil, nil)
		switch {
		case err == nil:
			rep.Containers++
		case !isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusConflict):
			return rep, fmt.Errorf("spawner docker: cleanup container: %w", err)
		}
	}
	if d.cfg.SecretsDir != "" {
		entries, err := os.ReadDir(d.cfg.SecretsDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return rep, fmt.Errorf("spawner docker: cleanup secrets: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() && agentIDRe.MatchString(e.Name()) && !live[e.Name()] {
				d.removeSecret(e.Name())
				rep.Secrets++
			}
		}
	}
	filters, err := json.Marshal(map[string][]string{"label": {d.label("managed") + "=true"}})
	if err != nil {
		return rep, err
	}
	q := url.Values{"filters": {string(filters)}}.Encode()
	var nets []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := d.do(ctx, "", http.MethodGet, "/networks?"+q, nil, &nets); err != nil {
		return rep, fmt.Errorf("spawner docker: cleanup networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == d.cfg.Network {
			continue
		}
		if err := d.do(ctx, "", http.MethodDelete, "/networks/"+n.ID, nil, nil); err == nil {
			rep.Networks++
		}
	}
	var vols struct {
		Volumes []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if err := d.do(ctx, "", http.MethodGet, "/volumes?"+q, nil, &vols); err != nil {
		return rep, fmt.Errorf("spawner docker: cleanup volumes: %w", err)
	}
	for _, v := range vols.Volumes {
		if live[v.Labels[d.label("agent")]] {
			continue
		}
		if err := d.do(ctx, "", http.MethodDelete, "/volumes/"+url.PathEscape(v.Name), nil, nil); err == nil {
			rep.Volumes++
		}
	}
	return rep, nil
}

type engineError struct {
	status  int
	message string
}

func (e *engineError) Error() string {
	return fmt.Sprintf("engine returned %d: %s", e.status, e.message)
}

func isStatus(err error, status int) bool {
	var he *engineError
	return errors.As(err, &he) && he.status == status
}

func (d *Docker) do(ctx context.Context, token, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, engineHost+path, rd)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), token))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := netguard.ReadBody(resp.Body, maxEngineReply)
	if err != nil {
		return errors.New(redact(err.Error(), token))
	}
	if resp.StatusCode/100 != 2 {
		return engineFailure(resp.StatusCode, data, token)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func engineFailure(status int, data []byte, token string) error {
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	msg = redact(msg, token)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return &engineError{status: status, message: msg}
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}
