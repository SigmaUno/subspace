package board

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type Connect struct {
	AgentID  string            `json:"agent_id"`
	JoinLine string            `json:"join_line"`
	MCPURL   string            `json:"mcp_url"`
	Snippets map[string]string `json:"snippets"`
	Token    string            `json:"token,omitempty"`
}

func (b *Board) ConnectInfo(agentID, token string) Connect {
	base := strings.TrimRight(b.o.PublicURL, "/")
	mcp := base + "/mcp"
	tk := token
	if tk == "" {
		tk = "<token>"
	}
	return Connect{
		AgentID:  agentID,
		JoinLine: fmt.Sprintf("Join Subspace at %s as %s. Call get_instructions and follow them.", base, agentID),
		MCPURL:   mcp,
		Snippets: map[string]string{
			"claude_code":   fmt.Sprintf("claude mcp add --transport http subspace %s --header \"Authorization: Bearer %s\"", mcp, tk),
			"codex_toml":    fmt.Sprintf("[mcp_servers.subspace]\nurl = %q\nhttp_headers = { Authorization = \"Bearer %s\" }\n", mcp, tk),
			"opencode_json": fmt.Sprintf(`{"mcp":{"subspace":{"type":"remote","url":%q,"headers":{"Authorization":"Bearer %s"}}}}`, mcp, tk),
		},
		Token: token,
	}
}

type AgentInput struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Provider string `json:"provider"`

	ExpiresIn string `json:"expires_in"`

	expiresIn time.Duration
}

func ParseExpiresIn(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, model.BadRequest("expires_in must be a positive duration such as 720h")
	}
	return d, nil
}

type Registered struct {
	Agent   *model.Agent `json:"agent"`
	Connect Connect      `json:"connect"`
}

func (in *AgentInput) validate() error {
	if !agentIDRe.MatchString(in.ID) {
		return model.BadRequest("agent id must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	if !model.ValidAgentRole(in.Role) {
		return model.BadRequest("role must be leader, worker, tester or reviewer")
	}
	if in.Kind == "" {
		in.Kind = model.KindOther
	}
	if in.Provider == "" {
		in.Provider = model.ProviderForKind(in.Kind)
	}
	var err error
	in.expiresIn, err = ParseExpiresIn(in.ExpiresIn)
	if err != nil {
		return err
	}
	return checkProvider(in.Kind, in.Provider)
}

func checkProvider(kind, provider string) error {
	if !providerRe.MatchString(provider) {
		return model.BadRequest("provider must match [a-z0-9][a-z0-9._-]{0,62}")
	}
	switch kind {
	case model.KindClaudeCode, model.KindCodex:
		if want := model.ProviderForKind(kind); provider != want {
			return model.BadRequest("provider for kind %s must be %s", kind, want)
		}
	}
	return nil
}

func (b *Board) newAgent(in AgentInput, spawned bool) (*model.Agent, string, error) {
	tok, err := auth.NewToken()
	if err != nil {
		return nil, "", err
	}
	a := &model.Agent{ID: in.ID, Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider,
		Status: model.AgentActive, CreatedAt: b.now(), Spawned: spawned, TokenHash: auth.Hash(tok)}
	if in.expiresIn > 0 {
		exp := b.now().Add(in.expiresIn)
		a.TokenExpiresAt = &exp
	}
	return a, tok, nil
}

func (b *Board) RegisterAgent(ctx context.Context, in AgentInput) (*Registered, error) {
	rs, err := b.registerMany(ctx, []AgentInput{in})
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

type BulkInput struct {
	Prefix   string `json:"prefix"`
	Count    int    `json:"count"`
	Role     string `json:"role"`
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Provider string `json:"provider"`

	ExpiresIn string `json:"expires_in"`
}

const MaxBulk = 1000

func (b *Board) RegisterBulk(ctx context.Context, in BulkInput) ([]*Registered, error) {
	if in.Count < 1 || in.Count > MaxBulk {
		return nil, model.BadRequest("count must be 1..%d", MaxBulk)
	}
	width := len(fmt.Sprint(in.Count))
	if width < 3 {
		width = 3
	}
	ins := make([]AgentInput, in.Count)
	for i := range ins {
		ins[i] = AgentInput{ID: fmt.Sprintf("%s-%0*d", in.Prefix, width, i+1), Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider, ExpiresIn: in.ExpiresIn}
	}
	return b.registerMany(ctx, ins)
}

func (b *Board) registerMany(ctx context.Context, ins []AgentInput) ([]*Registered, error) {
	out := make([]*Registered, 0, len(ins))
	for i := range ins {
		if err := ins[i].validate(); err != nil {
			return nil, err
		}
	}
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		out = out[:0]
		for _, in := range ins {
			if _, err := tx.GetAgent(in.ID); err == nil {
				return model.Conflict("agent %s already exists", in.ID)
			}
			a, tok, err := b.newAgent(in, false)
			if err != nil {
				return err
			}
			if err := tx.InsertAgent(a); err != nil {
				return err
			}
			w.agent(a)
			out = append(out, &Registered{Agent: a, Connect: b.ConnectInfo(a.ID, tok)})
		}
		return w.audit(tx, model.Owner, "register_agents", "", nil, map[string]any{"count": len(ins), "first": ins[0].ID}, "")
	})
	return out, err
}

func (b *Board) ListAgents(ctx context.Context, role, status string) ([]*model.Agent, error) {
	p, err := b.ListAgentsPage(ctx, role, status, "", 0)
	return p.Items, err
}

func (b *Board) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	var out *model.Agent
	err := b.read(ctx, func(tx store.Tx) error {
		a, err := tx.GetAgent(id)
		if err == nil {
			out = b.overlayAgent(a)
		}
		return err
	})
	return out, err
}

func (b *Board) LoadByTokenHash(hash string) (*model.Agent, error) {
	var out *model.Agent
	err := b.st.View(context.Background(), func(tx store.Tx) (e error) { out, e = tx.GetAgentByTokenHash(hash); return })
	if isErr(err, store.ErrNotFound) {
		return nil, nil
	}
	return out, err
}

func (b *Board) setAgent(ctx context.Context, actor model.Actor, tool, id string, fn func(tx store.Tx, w *wctx, a *model.Agent) error) (*model.Agent, error) {
	var out *model.Agent
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		a, err := tx.GetAgent(id)
		if err != nil {
			return model.NotFound("agent %s not found", id)
		}
		before := a.Status
		if err := fn(tx, w, a); err != nil {
			return err
		}
		if err := tx.UpdateAgent(a); err != nil {
			return err
		}
		w.agent(a)
		out = a
		return w.audit(tx, actor, tool, "", map[string]any{"agent": id, "status": before}, map[string]any{"status": a.Status}, "")
	})
	return out, err
}

func (b *Board) RevokeAgent(ctx context.Context, id, reason string) (*model.Agent, error) {
	return b.setAgent(ctx, model.Owner, "revoke_agent", id, func(tx store.Tx, w *wctx, a *model.Agent) error {
		a.Status, a.PauseReason = model.AgentRevoked, strp(reason)
		return b.releaseAgentTasks(tx, w, a.ID, "agent revoked")
	})
}

func (b *Board) releaseAgentTasks(tx store.Tx, w *wctx, agentID, note string) error {
	ts, err := tx.ListTasks(store.TaskFilter{Assignee: agentID, Status: []string{model.StatusInProgress}})
	if err != nil {
		return err
	}
	for _, t := range ts {
		if err := b.releaseLease(tx, w, model.Actor{ID: "system", Role: "system"}, t, note); err != nil {
			return err
		}
	}
	return nil
}

func (b *Board) ResumeAgent(ctx context.Context, id string) (*model.Agent, error) {
	return b.setAgent(ctx, model.Owner, "resume_agent", id, func(tx store.Tx, w *wctx, a *model.Agent) error {
		if a.Status == model.AgentRevoked {
			return model.Conflict("agent is revoked")
		}
		a.Status, a.PauseReason = model.AgentActive, nil
		return nil
	})
}

func (b *Board) PauseAgent(ctx context.Context, id, reason string) error {
	_, err := b.setAgent(ctx, model.Actor{ID: "system", Role: "system"}, "pause_agent", id, func(tx store.Tx, w *wctx, a *model.Agent) error {
		if a.Status != model.AgentActive {
			return nil
		}
		a.Status, a.PauseReason = model.AgentPaused, strp(reason)
		if err := w.post(tx, model.ThreadAgent, id, model.Actor{ID: "system", Role: "system"}, model.MsgSystem, "Agent paused: "+reason); err != nil {
			return err
		}
		return b.releaseAgentTasks(tx, w, id, "agent paused: "+reason)
	})
	return err
}

func (b *Board) RotateToken(ctx context.Context, id string) (*Registered, error) {
	return b.RotateTokenFor(ctx, id, 0)
}

func (b *Board) RotateTokenFor(ctx context.Context, id string, expiresIn time.Duration) (*Registered, error) {
	var tok string
	a, err := b.setAgent(ctx, model.Owner, "rotate_token", id, func(tx store.Tx, w *wctx, a *model.Agent) error {
		if a.Status == model.AgentRevoked {
			return model.Conflict("agent is revoked")
		}
		switch {
		case expiresIn > 0:
			exp := b.now().Add(expiresIn)
			a.TokenExpiresAt = &exp
		case a.TokenExpiresAt != nil && !a.TokenExpiresAt.After(b.now()):
			return model.BadRequest("token already expired; set expires_in")
		}
		var err error
		tok, err = auth.NewToken()
		a.TokenHash = auth.Hash(tok)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Registered{Agent: a, Connect: b.ConnectInfo(id, tok)}, nil
}

func (b *Board) Connect(ctx context.Context, id string) (*Connect, error) {
	if _, err := b.GetAgent(ctx, id); err != nil {
		return nil, err
	}
	c := b.ConnectInfo(id, "")
	return &c, nil
}
