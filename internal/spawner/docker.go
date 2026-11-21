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
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	LabelAgent = "subspace.agent"
	LabelKind  = "subspace.kind"

	DefaultDockerSocket = "/var/run/docker.sock"

	engineHost       = "http://docker"
	maxEngineReply   = 1 << 20
	defaultEngineTTL = 30 * time.Second
)

type DockerConfig struct {
	Socket      string
	Images      map[string]string
	CPUs        float64
	MemoryBytes int64
	Network     string
}

type Container struct {
	ID      string
	AgentID string
	Kind    string
	Image   string
	State   string
}

type Docker struct {
	cfg    DockerConfig
	client *http.Client
}

func NewDocker(cfg DockerConfig) *Docker {
	if cfg.Socket == "" {
		cfg.Socket = DefaultDockerSocket
	}
	socket := cfg.Socket
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Docker{cfg: cfg, client: &http.Client{Transport: tr, Timeout: defaultEngineTTL}}
}

func (Docker) Name() string { return "docker" }

type createBody struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	AutoRemove  bool   `json:"AutoRemove"`
	NanoCpus    int64  `json:"NanoCpus,omitempty"`
	Memory      int64  `json:"Memory,omitempty"`
	NetworkMode string `json:"NetworkMode,omitempty"`
}

type engineContainer struct {
	ID     string            `json:"Id"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func (d Docker) Spawn(ctx context.Context, r Request) error {
	if d.client == nil {
		return ErrNotImplemented
	}
	if !agentIDRe.MatchString(r.AgentID) {
		return fmt.Errorf("spawner docker: invalid agent id %q", r.AgentID)
	}
	image := d.cfg.Images[r.Kind]
	if image == "" {
		return fmt.Errorf("spawner docker: no image configured for kind %q", r.Kind)
	}
	env, err := dockerEnv(r)
	if err != nil {
		return err
	}
	body := createBody{
		Image:  image,
		Env:    env,
		Labels: map[string]string{LabelAgent: r.AgentID, LabelKind: r.Kind},
		HostConfig: hostConfig{
			AutoRemove:  true,
			NanoCpus:    int64(d.cfg.CPUs * 1e9),
			Memory:      d.cfg.MemoryBytes,
			NetworkMode: d.cfg.Network,
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	q := url.Values{"name": {"subspace-" + r.AgentID}}
	if err := d.do(ctx, r.Token, http.MethodPost, "/containers/create?"+q.Encode(), body, &created); err != nil {
		return fmt.Errorf("spawner docker: create: %w", err)
	}
	if err := d.do(ctx, r.Token, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = d.do(cleanup, r.Token, http.MethodDelete, "/containers/"+created.ID+"?force=true", nil, nil)
		return fmt.Errorf("spawner docker: start: %w", err)
	}
	return nil
}

func (d Docker) Stop(ctx context.Context, agentID string) error {
	if d.client == nil {
		return ErrNotImplemented
	}
	list, err := d.list(ctx, LabelAgent+"="+agentID)
	if err != nil {
		return err
	}
	for _, c := range list {
		err := d.do(ctx, "", http.MethodPost, "/containers/"+c.ID+"/kill", nil, nil)
		var he *engineError
		if err == nil || (errors.As(err, &he) && he.status == http.StatusNotFound) {
			continue
		}
		return fmt.Errorf("spawner docker: kill: %w", err)
	}
	return nil
}

func (d Docker) Status(ctx context.Context, agentID string) (Status, error) {
	if d.client == nil {
		return StatusUnknown, ErrNotImplemented
	}
	list, err := d.list(ctx, LabelAgent+"="+agentID)
	if err != nil {
		return StatusUnknown, err
	}
	if len(list) == 0 {
		return StatusExited, nil
	}
	return StatusRunning, nil
}

func (d Docker) List(ctx context.Context) ([]Container, error) {
	if d.client == nil {
		return nil, ErrNotImplemented
	}
	return d.list(ctx, LabelAgent)
}

func (d Docker) list(ctx context.Context, label string) ([]Container, error) {
	filters, err := json.Marshal(map[string][]string{"label": {label}, "status": {"running"}})
	if err != nil {
		return nil, err
	}
	var raw []engineContainer
	q := url.Values{"filters": {string(filters)}}
	if err := d.do(ctx, "", http.MethodGet, "/containers/json?"+q.Encode(), nil, &raw); err != nil {
		return nil, fmt.Errorf("spawner docker: list: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, c := range raw {
		agent := c.Labels[LabelAgent]
		if agent == "" {
			continue
		}
		out = append(out, Container{ID: c.ID, AgentID: agent, Kind: c.Labels[LabelKind], Image: c.Image, State: c.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

type engineError struct {
	status  int
	message string
}

func (e *engineError) Error() string {
	return fmt.Sprintf("engine returned %d: %s", e.status, e.message)
}

func (d Docker) do(ctx context.Context, token, method, path string, in, out any) error {
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxEngineReply))
	if err != nil {
		return errors.New(redact(err.Error(), token))
	}
	if resp.StatusCode/100 != 2 {
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
		return &engineError{status: resp.StatusCode, message: msg}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

func dockerEnv(r Request) ([]string, error) {
	m := map[string]string{}
	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("spawner docker: invalid env key %q", k)
		}
		m[k] = v
	}
	m["SUBSPACE_URL"] = r.MCPURL
	m["SUBSPACE_TOKEN"] = r.Token
	m["SUBSPACE_AGENT_ID"] = r.AgentID
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
