package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	DefaultListTTL   = time.Minute
	listTimeout      = 10 * time.Second
	listRetry        = 10 * time.Second
	maxListPages     = 10
	maxAuditArgs     = 2048
	maxUpstreamError = 300
)

type Options struct {
	Store    store.Store
	Commands []string
	Guard    *netguard.Guard
	Getenv   func(string) string
	Now      func() time.Time
	Log      *slog.Logger
	ListTTL  time.Duration
}

type Service struct {
	Gateways *Store

	st     store.Store
	guard  *netguard.Guard
	getenv func(string) string
	now    func() time.Time
	log    *slog.Logger
	ttl    time.Duration

	mu    sync.Mutex
	conns map[string]*conn
	usage map[string]*usage
	calls map[string][]time.Time
}

type conn struct {
	key string
	up  upstream

	mu      sync.Mutex
	tools   []Discovered
	fetched time.Time
	err     string
}

type usage struct {
	day   string
	calls int
	bytes int64
}

func New(o Options) *Service {
	s := &Service{st: o.Store, guard: o.Guard, getenv: o.Getenv, now: o.Now, log: o.Log, ttl: o.ListTTL,
		conns: map[string]*conn{}, usage: map[string]*usage{}, calls: map[string][]time.Time{}}
	if s.guard == nil {
		s.guard = netguard.New(nil)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = DefaultListTTL
	}
	s.Gateways = &Store{St: o.Store, Now: s.now, Commands: o.Commands}
	return s
}

func (s *Service) Commands() []string {
	out := make([]string, len(s.Gateways.Commands))
	copy(out, s.Gateways.Commands)
	return out
}

func (s *Service) secret(g Gateway) string {
	if g.CredentialEnv == "" || s.getenv == nil {
		return ""
	}
	return s.getenv(g.CredentialEnv)
}

func (s *Service) Close() {
	s.mu.Lock()
	conns := s.conns
	s.conns = map[string]*conn{}
	s.mu.Unlock()
	for _, c := range conns {
		c.up.close()
	}
}

func (s *Service) conn(g Gateway) *conn {
	key, _ := encode(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.conns[g.Name]; ok {
		if c.key == key {
			return c
		}
		c.up.close()
	}
	var up upstream
	secret := s.secret(g)
	if g.Transport == TransportStdio {
		up = newStdioUpstream(g, secret, s.now, s.Gateways.Commands)
	} else {
		up = newHTTPUpstream(g, secret, s.guard)
	}
	c := &conn{key: key, up: up}
	s.conns[g.Name] = c
	return c
}

func (s *Service) prune(keep map[string]bool) {
	s.mu.Lock()
	var stale []*conn
	for name, c := range s.conns {
		if !keep[name] {
			stale = append(stale, c)
			delete(s.conns, name)
		}
	}
	s.mu.Unlock()
	for _, c := range stale {
		c.up.close()
	}
}

func (s *Service) discover(ctx context.Context, g Gateway, force bool) ([]Discovered, string) {
	c := s.conn(g)
	c.mu.Lock()
	defer c.mu.Unlock()
	age := s.now().Sub(c.fetched)
	if !force && !c.fetched.IsZero() && (age < s.ttl && c.err == "" || age < listRetry) {
		return c.tools, c.err
	}
	ctx, cancel := context.WithTimeout(ctx, min(g.Timeout(), listTimeout))
	defer cancel()
	tools, err := fetchTools(ctx, c.up)
	c.fetched = s.now()
	if err != nil {
		c.err = s.describe(g, err).Message
		if s.log != nil {
			s.log.Warn("gateway tools/list failed", "gateway", g.Name, "err", c.err)
		}
		return c.tools, c.err
	}
	found := vet(g.Name, tools, s.secret(g))
	for i := range found {
		found[i].Allowed = found[i].Refused == "" && g.Allows(found[i].Name)
	}
	c.tools, c.err = found, ""
	return c.tools, ""
}

func fetchTools(ctx context.Context, up upstream) ([]upstreamTool, error) {
	var all []upstreamTool
	cursor := ""
	for range maxListPages {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := up.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []upstreamTool `json:"tools"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, errProtocol
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" || len(all) > maxToolsPerGateway {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
}

func (s *Service) enabled(ctx context.Context) []Gateway {
	gs, err := s.Gateways.List(ctx)
	if err != nil {
		if s.log != nil {
			s.log.Error("gateway list failed", "err", err)
		}
		return nil
	}
	keep := map[string]bool{}
	out := gs[:0]
	for _, g := range gs {
		if g.Enabled {
			keep[g.Name] = true
			out = append(out, g)
		}
	}
	s.prune(keep)
	return out
}

func (s *Service) Tools(ctx context.Context, role string) []*mcp.Tool {
	var mine []Gateway
	for _, g := range s.enabled(ctx) {
		if g.AllowsRole(role) {
			mine = append(mine, g)
		}
	}
	found := make([][]Discovered, len(mine))
	var wg sync.WaitGroup
	for i, g := range mine {
		wg.Go(func() { found[i], _ = s.discover(ctx, g, false) })
	}
	wg.Wait()
	var out []*mcp.Tool
	for i, g := range mine {
		for _, d := range found[i] {
			if d.Allowed {
				out = append(out, s.tool(g, d))
			}
		}
	}
	return out
}

func (s *Service) Tool(ctx context.Context, name, role string) (*mcp.Tool, bool) {
	gw, tool, ok := SplitName(name)
	if !ok {
		return nil, false
	}
	g, err := s.Gateways.Get(ctx, gw)
	if err != nil || !g.Enabled || !g.AllowsRole(role) || !g.Allows(tool) {
		return nil, false
	}
	tools, _ := s.discover(ctx, g, false)
	for _, d := range tools {
		if d.Name == tool && d.Allowed {
			return s.tool(g, d), true
		}
	}
	return nil, false
}

func (s *Service) Discover(ctx context.Context, name string) ([]Discovered, string, error) {
	g, err := s.Gateways.Get(ctx, name)
	if err != nil {
		return nil, "", err
	}
	tools, msg := s.discover(ctx, g, true)
	if tools == nil {
		tools = []Discovered{}
	}
	return tools, msg, nil
}

func (s *Service) tool(g Gateway, d Discovered) *mcp.Tool {
	return &mcp.Tool{
		Name:        d.ExposedAs,
		Description: fence(g.Name, d.Name, d.Description),
		Roles:       g.AllowedRoles,
		Schema:      d.InputSchema,
		Handler: func(ctx context.Context, c *mcp.Call) (any, error) {
			return s.invoke(ctx, g, d.Name, c)
		},
	}
}

func (s *Service) allowRate(g Gateway, tool string, now time.Time) (int, bool) {
	limit := g.rateFor(tool)
	if limit == 0 {
		return 0, true
	}
	key := g.Name + "/" + tool
	s.mu.Lock()
	defer s.mu.Unlock()
	window := s.calls[key][:0]
	for _, t := range s.calls[key] {
		if now.Sub(t) < time.Minute {
			window = append(window, t)
		}
	}
	if len(window) >= limit {
		s.calls[key] = window
		return limit, false
	}
	s.calls[key] = append(window, now)
	return limit, true
}

func (s *Service) usageLocked(g Gateway, now time.Time) *usage {
	day := now.UTC().Format(time.DateOnly)
	u := s.usage[g.Name]
	if u == nil || u.day != day {
		u = &usage{day: day}
		s.usage[g.Name] = u
	}
	return u
}

func (s *Service) reserve(g Gateway, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.usageLocked(g, now)
	if g.Budget.MaxCallsPerDay > 0 && u.calls >= g.Budget.MaxCallsPerDay {
		return false
	}
	if g.Budget.MaxResultBytesPerDay > 0 && u.bytes >= g.Budget.MaxResultBytesPerDay {
		return false
	}
	u.calls++
	return true
}

func (s *Service) spend(g Gateway, now time.Time, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageLocked(g, now).bytes += int64(n)
}

type outcome struct {
	status string
	bytes  int
}

func (s *Service) invoke(ctx context.Context, g Gateway, tool string, c *mcp.Call) (any, error) {
	start := s.now()
	out := outcome{status: "ok"}
	defer func() { s.record(ctx, g, tool, c, start, out) }()
	fail := func(err *model.Error) (any, error) {
		out.status = err.Code
		return nil, err
	}
	if limit, ok := s.allowRate(g, tool, start); !ok {
		return fail(model.Errf("rate_limited", "gateway %s allows %d calls of %s per minute", g.Name, limit, tool))
	}
	args, err := arguments(c.Args)
	if err != nil {
		return fail(model.BadRequest("arguments must be a JSON object"))
	}
	if !s.reserve(g, start) {
		return fail(model.Errf("budget_exceeded", "the daily budget of gateway %s is used up", g.Name))
	}
	ctx, cancel := context.WithTimeout(ctx, g.Timeout())
	defer cancel()
	raw, callErr := s.conn(g).up.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if callErr != nil {
		return fail(s.describe(g, callErr))
	}
	out.bytes = len(raw)
	s.spend(g, s.now(), len(raw))
	if len(raw) > g.MaxResultBytes {
		return fail(model.Errf("too_large", "the result of gateway %s exceeds %d bytes", g.Name, g.MaxResultBytes))
	}
	res, _, err := sanitizeResult(g.Name, tool, raw, s.secret(g))
	if err != nil {
		return fail(s.describe(g, err))
	}
	if res.IsError {
		out.status = "tool_error"
	}
	return res, nil
}

func arguments(raw json.RawMessage) (map[string]json.RawMessage, error) {
	args := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}

func (s *Service) describe(g Gateway, err error) *model.Error {
	var rpc *rpcError
	switch {
	case errors.As(err, &rpc):
		msg, _ := cleanText(rpc.Message, maxUpstreamError)
		return model.Errf("upstream_error", "gateway %s: upstream error %d: %s", g.Name, rpc.Code, redactSecret(msg, s.secret(g)))
	case errors.Is(err, context.DeadlineExceeded):
		return model.Errf("upstream_error", "gateway %s: upstream did not answer within %s", g.Name, g.Timeout())
	case errors.Is(err, errTooLarge):
		return model.Errf("too_large", "the result of gateway %s exceeds %d bytes", g.Name, g.MaxResultBytes)
	case errors.Is(err, errResultShape), errors.Is(err, errProtocol):
		return model.Errf("upstream_error", "gateway %s: upstream sent an invalid response", g.Name)
	case errors.Is(err, errCommandNotAllowed):
		return model.Errf("upstream_error", "gateway %s: command %s is not in SUBSPACE_GATEWAY_COMMANDS", g.Name, g.Command)
	case errors.Is(err, netguard.ErrBlocked):
		return model.Errf("upstream_error", "gateway %s: upstream address is not allowed", g.Name)
	}
	return model.Errf("upstream_error", "gateway %s: upstream is unavailable", g.Name)
}

func (s *Service) record(ctx context.Context, g Gateway, tool string, c *mcp.Call, start time.Time, out outcome) {
	after := map[string]any{
		"gateway":      g.Name,
		"tool":         tool,
		"args_hash":    audit.ArgsHash(c.Args),
		"duration_ms":  s.now().Sub(start).Milliseconds(),
		"result_bytes": out.bytes,
		"status":       out.status,
	}
	if g.AuditArguments {
		args, _ := cleanText(redactSecret(string(c.Args), s.secret(g)), maxAuditArgs)
		after["arguments"] = args
	}
	e := &model.AuditEvent{TS: start.UTC(), AgentID: c.Actor.ID, Role: c.Actor.Role, Tool: "gateway_call", After: after}
	err := s.st.Atomic(context.WithoutCancel(ctx), func(tx store.Tx) error { return tx.InsertAudit(e) })
	if err != nil && s.log != nil {
		s.log.Error("gateway audit failed", "gateway", g.Name, "err", err)
	}
}
