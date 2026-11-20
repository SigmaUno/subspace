package bots

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	MaxBulk   = 1000
	MaxWeight = 1000
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var kinds = map[string]struct{ kind, provider string }{
	"claude-code":        {model.KindClaudeCode, "anthropic"},
	"codex":              {model.KindCodex, "openai"},
	"opencode-anthropic": {model.KindOpenCode, "anthropic"},
	"opencode-deepseek":  {model.KindOpenCode, "deepseek"},
}

type Bot struct {
	model.Agent
	Weight int `json:"capacity_weight"`
}

type AgentStore interface {
	Insert(ctx context.Context, bots []*Bot) error
	Get(ctx context.Context, id string) (*Bot, error)
	List(ctx context.Context, role, status string) ([]*Bot, error)
	Update(ctx context.Context, b *Bot) error
	Delete(ctx context.Context, id string) error
	ReleaseLeases(ctx context.Context, agentID, note string) error
}

type Spawner interface {
	Spawn(ctx context.Context, r spawner.Request) error
	Stop(ctx context.Context, agentID string) error
}

type Capacity interface {
	Check(ctx context.Context, provider, model string, weight int) error
}

type Auditor interface {
	Record(ctx context.Context, e *model.AuditEvent) error
}

type Deps struct {
	Agents    AgentStore
	Spawner   Spawner
	Capacity  Capacity
	Auditor   Auditor
	PublicURL string
	Now       func() time.Time
}

type Service struct {
	d  Deps
	mu sync.Mutex
}

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{d: d}
}

type CreateInput struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"capacity_weight"`
}

type BulkInput struct {
	Prefix   string `json:"prefix"`
	Count    int    `json:"count"`
	Role     string `json:"role"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"capacity_weight"`
}

type Connect struct {
	AgentID  string            `json:"agent_id"`
	JoinLine string            `json:"join_line"`
	MCPURL   string            `json:"mcp_url"`
	Snippets map[string]string `json:"snippets"`
	Token    string            `json:"token,omitempty"`
}

type Created struct {
	Bot     *Bot    `json:"bot"`
	Connect Connect `json:"connect"`
}

func (s *Service) connect(id, token string) Connect {
	cc, codex, oc := spawner.Snippets(s.d.PublicURL, token)
	return Connect{
		AgentID:  id,
		JoinLine: spawner.JoinLine(s.d.PublicURL, id),
		MCPURL:   spawner.MCPURL(s.d.PublicURL),
		Snippets: map[string]string{"claude_code": cc, "codex_toml": codex, "opencode_json": oc},
		Token:    token,
	}
}

type spec struct {
	id, role, kind, provider, model string
	weight                          int
}

func newSpec(id, role, kind, provider, mdl string, weight int) (spec, error) {
	if !idRe.MatchString(id) {
		return spec{}, model.BadRequest("bot name must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	if role == "" {
		role = model.RoleWorker
	}
	if !model.ValidAgentRole(role) {
		return spec{}, model.BadRequest("role must be leader, worker, tester or reviewer")
	}
	k, ok := kinds[kind]
	if !ok {
		return spec{}, model.BadRequest("kind must be claude-code, codex, opencode-anthropic or opencode-deepseek")
	}
	if provider == "" {
		provider = k.provider
	}
	if provider != k.provider {
		return spec{}, model.BadRequest("provider for kind %s must be %s", kind, k.provider)
	}
	if weight == 0 {
		weight = 1
	}
	if weight < 1 || weight > MaxWeight {
		return spec{}, model.BadRequest("capacity_weight must be 1..%d", MaxWeight)
	}
	return spec{id: id, role: role, kind: k.kind, provider: provider, model: mdl, weight: weight}, nil
}

func (s *Service) Create(ctx context.Context, actor model.Actor, in CreateInput) (*Created, error) {
	sp, err := newSpec(in.Name, in.Role, in.Kind, in.Provider, in.Model, in.Weight)
	if err != nil {
		return nil, err
	}
	out, err := s.issue(ctx, actor, "create_bot", []spec{sp}, map[string]any{"bot": sp.id, "kind": in.Kind})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

func (s *Service) BulkCreate(ctx context.Context, actor model.Actor, in BulkInput) ([]*Created, error) {
	if in.Count < 1 || in.Count > MaxBulk {
		return nil, model.BadRequest("count must be 1..%d", MaxBulk)
	}
	width := max(3, len(fmt.Sprint(in.Count)))
	specs := make([]spec, in.Count)
	for i := range specs {
		sp, err := newSpec(fmt.Sprintf("%s-%0*d", in.Prefix, width, i+1), in.Role, in.Kind, in.Provider, in.Model, in.Weight)
		if err != nil {
			return nil, err
		}
		specs[i] = sp
	}
	return s.issue(ctx, actor, "bulk_create_bots", specs, map[string]any{"first": specs[0].id, "count": len(specs), "kind": in.Kind})
}

func (s *Service) issue(ctx context.Context, actor model.Actor, tool string, specs []spec, after map[string]any) ([]*Created, error) {
	now := s.d.Now()
	bots := make([]*Bot, len(specs))
	out := make([]*Created, len(specs))
	for i, sp := range specs {
		tok, err := auth.NewToken()
		if err != nil {
			return nil, err
		}
		b := &Bot{
			Agent: model.Agent{ID: sp.id, Role: sp.role, Kind: sp.kind, Model: sp.model, Provider: sp.provider,
				Status: model.AgentActive, CreatedAt: now, TokenHash: auth.Hash(tok)},
			Weight: sp.weight,
		}
		bots[i] = b
		out[i] = &Created{Bot: b, Connect: s.connect(sp.id, tok)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.d.Agents.Insert(ctx, bots); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return nil, model.Conflict("a bot with one of these names already exists")
		}
		return nil, err
	}
	if err := s.audit(ctx, actor, tool, nil, after, ""); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, role, status string) ([]*Bot, error) {
	bs, err := s.d.Agents.List(ctx, role, status)
	if bs == nil {
		bs = []*Bot{}
	}
	return bs, err
}

func (s *Service) get(ctx context.Context, id string) (*Bot, error) {
	b, err := s.d.Agents.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, model.NotFound("bot %s not found", id)
	}
	return b, err
}

func (s *Service) audit(ctx context.Context, actor model.Actor, tool string, before, after map[string]any, note string) error {
	e := &model.AuditEvent{TS: s.d.Now(), AgentID: actor.ID, Role: actor.Role, Tool: tool, Before: before, After: after}
	if note != "" {
		e.Note = &note
	}
	return s.d.Auditor.Record(ctx, e)
}

func state(b *Bot) map[string]any {
	return map[string]any{"bot": b.ID, "status": b.Status}
}

func reasonPtr(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}

func (s *Service) Revoke(ctx context.Context, actor model.Actor, id, reason string) (*Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.Status == model.AgentRevoked {
		return nil, model.Conflict("bot %s is already revoked", id)
	}
	before := state(b)
	b.Status, b.PauseReason = model.AgentRevoked, reasonPtr(reason)
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return nil, err
	}
	if err := s.d.Agents.ReleaseLeases(ctx, id, "bot revoked"); err != nil {
		return nil, err
	}
	return b, s.audit(ctx, actor, "revoke_bot", before, state(b), reason)
}

func (s *Service) pause(ctx context.Context, b *Bot, reason string) error {
	if b.Status == model.AgentRevoked {
		return model.Conflict("bot %s is revoked", b.ID)
	}
	if reason == "" {
		reason = "paused by owner"
	}
	b.Status, b.PauseReason = model.AgentPaused, &reason
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return err
	}
	return s.d.Agents.ReleaseLeases(ctx, b.ID, "bot paused: "+reason)
}

func (s *Service) Pause(ctx context.Context, actor model.Actor, id, reason string) (*Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	before := state(b)
	if err := s.pause(ctx, b, reason); err != nil {
		return nil, err
	}
	return b, s.audit(ctx, actor, "pause_bot", before, state(b), reason)
}

func (s *Service) Resume(ctx context.Context, actor model.Actor, id string) (*Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.Status == model.AgentRevoked {
		return nil, model.Conflict("bot %s is revoked", id)
	}
	before := state(b)
	b.Status, b.PauseReason = model.AgentActive, nil
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return nil, err
	}
	return b, s.audit(ctx, actor, "resume_bot", before, state(b), "")
}

func (s *Service) Kill(ctx context.Context, actor model.Actor, id, reason string) (*Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	before := state(b)
	if reason == "" {
		reason = "killed by owner"
	}
	if err := s.pause(ctx, b, reason); err != nil {
		return nil, err
	}
	stopErr := s.d.Spawner.Stop(ctx, id)
	note := reason
	if stopErr != nil {
		note = "stop failed: " + stopErr.Error()
	}
	if err := s.audit(ctx, actor, "kill_bot", before, state(b), note); err != nil {
		return nil, err
	}
	if stopErr != nil {
		return nil, model.Errf("internal", "bot %s paused but stop failed: %v", id, stopErr)
	}
	b.Spawned = false
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) Delete(ctx context.Context, actor model.Actor, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if b.Status != model.AgentRevoked {
		return model.Conflict("bot %s must be revoked before it is deleted", id)
	}
	before := state(b)
	if err := s.d.Agents.Delete(ctx, id); err != nil {
		return err
	}
	return s.audit(ctx, actor, "delete_bot", before, map[string]any{"bot": id, "deleted": true}, "")
}

func (s *Service) Spawn(ctx context.Context, actor model.Actor, id, repo string) (*Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.Status != model.AgentActive {
		return nil, model.Conflict("bot %s is %s", id, b.Status)
	}
	if err := s.d.Capacity.Check(ctx, b.Provider, b.Model, b.Weight); err != nil {
		return nil, err
	}
	tok, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	before := state(b)
	prevHash := b.TokenHash
	b.TokenHash = auth.Hash(tok)
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return nil, err
	}
	spawnErr := s.d.Spawner.Spawn(ctx, spawner.Request{
		AgentID:  id,
		Kind:     b.Kind,
		Model:    b.Model,
		Provider: b.Provider,
		Repo:     strings.TrimSpace(repo),
		JoinLine: spawner.JoinLine(s.d.PublicURL, id),
		MCPURL:   spawner.MCPURL(s.d.PublicURL),
		Token:    tok,
	})
	if spawnErr != nil {
		b.TokenHash = prevHash
		restoreErr := s.d.Agents.Update(ctx, b)
		if err := s.audit(ctx, actor, "spawn_bot", before, state(b), "spawn failed: "+spawnErr.Error()); err != nil {
			return nil, err
		}
		if restoreErr != nil {
			return nil, restoreErr
		}
		return nil, model.Errf("internal", "spawn of %s failed: %v", id, spawnErr)
	}
	b.Spawned = true
	if err := s.d.Agents.Update(ctx, b); err != nil {
		return nil, err
	}
	return b, s.audit(ctx, actor, "spawn_bot", before, map[string]any{"bot": id, "status": b.Status, "spawned": true}, "")
}
