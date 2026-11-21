package board

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

type CapacityView struct {
	GlobalMaxAgents int               `json:"global_max_agents"`
	Limits          []capacity.Report `json:"limits"`
	AgentsInUse     int               `json:"agents_in_use"`
	Paused          bool              `json:"paused"`
}

const spawnGrace = 10 * time.Minute

func (b *Board) usage(tx store.Tx) (capacity.Usage, map[string]bool, error) {
	ag, err := tx.ListAgents(store.AgentFilter{Limit: 100000})
	if err != nil {
		return nil, nil, err
	}
	ts, err := tx.ListTasks(store.TaskFilter{Status: []string{model.StatusInProgress}, Limit: 100000})
	if err != nil {
		return nil, nil, err
	}
	busy := map[string]bool{}
	for _, t := range ts {
		busy[deref(t.Assignee)] = true
	}
	now := b.now()
	u := capacity.Usage{}
	for _, a := range ag {
		if a.Status == model.AgentRevoked {
			continue
		}
		a = b.overlayAgent(a)
		live := a.Spawned && (now.Sub(a.CreatedAt) < spawnGrace || (a.LastSeenAt != nil && now.Sub(*a.LastSeenAt) < 2*b.o.Lease))
		if busy[a.ID] || live {
			u[[2]string{a.Provider, a.Model}]++
		}
	}
	return u, busy, nil
}

func (b *Board) capConfig() capacity.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.capCfg
}

func (b *Board) Capacity(ctx context.Context) (*CapacityView, error) {
	cfg := b.capConfig()
	v := &CapacityView{GlobalMaxAgents: cfg.GlobalMaxAgents}
	err := b.read(ctx, func(tx store.Tx) error {
		u, _, err := b.usage(tx)
		if err != nil {
			return err
		}
		v.Limits = cfg.Report(u)
		for _, c := range u {
			v.AgentsInUse += c
		}
		return nil
	})
	v.Paused, _ = b.Paused()
	return v, err
}

func (b *Board) SetCapacity(ctx context.Context, cfg capacity.Config) (*CapacityView, error) {
	if err := cfg.Validate(); err != nil {
		return nil, model.BadRequest("%v", err)
	}
	if cfg.Limits == nil {
		cfg.Limits = []capacity.Limit{}
	}
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		raw := jsonString(cfg)
		if err := tx.SetKV("capacity", raw); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "set_capacity", "", nil, map[string]any{"global_max_agents": cfg.GlobalMaxAgents, "limits": len(cfg.Limits)}, "")
	})
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.capCfg = cfg
	b.mu.Unlock()
	v, err := b.Capacity(ctx)
	if err == nil && b.o.Hub != nil {
		b.o.Hub.Publish([]string{"control"}, "capacity.changed", v)
	}
	return v, err
}

type SpawnInput struct {
	BriefID  string `json:"brief_id"`
	Kind     string `json:"worker_kind"`
	Model    string `json:"model"`
	N        int    `json:"n"`
	Role     string `json:"role"`
	Provider string `json:"provider"`
}

func (b *Board) RequestSpawn(ctx context.Context, a model.Actor, in SpawnInput) ([]*model.Spawn, error) {
	if p, why := b.Paused(); p {
		return nil, model.Errf("paused", "Subspace is paused: %s", why)
	}
	if in.N < 1 || in.N > 50 {
		return nil, model.BadRequest("n must be 1..50")
	}
	switch in.Kind {
	case model.KindClaudeCode, model.KindCodex, model.KindOpenCode:
	default:
		return nil, model.BadRequest("worker_kind must be claude-code, codex or opencode")
	}
	if in.Role == "" {
		in.Role = model.RoleWorker
	}
	if in.Role != model.RoleWorker && in.Role != model.RoleTester {
		return nil, model.BadRequest("role must be worker or tester")
	}
	if in.Model == "" {
		return nil, model.BadRequest("model is required")
	}
	if in.Provider == "" {
		in.Provider = model.ProviderForKind(in.Kind)
	}
	if err := checkProvider(in.Kind, in.Provider); err != nil {
		return nil, err
	}
	type launch struct {
		sp  *model.Spawn
		tok string
		ag  *model.Agent
	}
	var ls []launch
	cfg := b.capConfig()
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		ls = ls[:0]
		if err := pausedTx(tx); err != nil {
			return err
		}
		br, err := tx.GetBrief(in.BriefID)
		if err != nil {
			return err
		}
		if br.Status != "open" {
			return model.Conflict("brief %s is closed", br.ID)
		}
		if !b.providerAllowed(br, in.Provider) {
			return model.Forbidden("provider %q is not allowed for brief %s", in.Provider, br.ID)
		}
		if err := b.checkBudget(tx, &model.Task{}, br); err != nil {
			return err
		}
		u, busy, err := b.usage(tx)
		if err != nil {
			return err
		}
		if err := cfg.Check(in.Provider, in.Model, in.N, u); err != nil {
			return err
		}
		if br.Budget.MaxParallelAgent > 0 {
			ts, err := tx.ListTasks(store.TaskFilter{BriefID: br.ID, Status: []string{model.StatusInProgress, model.StatusAssigned}, Limit: 100000})
			if err != nil {
				return err
			}
			act := map[string]bool{}
			for _, t := range ts {
				act[deref(t.Assignee)] = true
			}
			recent := 0
			sps, err := tx.ListSpawns(store.SpawnFilter{BriefID: br.ID})
			if err != nil {
				return err
			}
			for _, s := range sps {
				if s.Status != "failed" && b.now().Sub(s.CreatedAt) < spawnGrace && !act[s.AgentID] && !busy[s.AgentID] {
					recent++
				}
			}
			if len(act)+recent+in.N > br.Budget.MaxParallelAgent {
				return model.Errf("capacity", "brief %s max_parallel_agents %d would be exceeded (%d active, %d pending)", br.ID, br.Budget.MaxParallelAgent, len(act), recent)
			}
		}
		for i := 0; i < in.N; i++ {
			seq, err := tx.NextID("sp-" + br.ID)
			if err != nil {
				return err
			}
			n, _ := strconv.Atoi(seq[strings.LastIndex(seq, "-")+1:])
			agentID := fmt.Sprintf("%s-%s-%d", in.Kind, strings.ToLower(br.ID), n)
			ag, tok, err := b.newAgent(AgentInput{ID: agentID, Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider}, true)
			if err != nil {
				return err
			}
			if err := tx.InsertAgent(ag); err != nil {
				return err
			}
			w.agent(ag)
			sid, err := tx.NextID("S")
			if err != nil {
				return err
			}
			sp := &model.Spawn{ID: sid, BriefID: br.ID, AgentID: agentID, Kind: in.Kind, Model: in.Model, Provider: in.Provider,
				Status: "requested", Spawner: b.o.Spawner.Name(), RequestedBy: a.ID, CreatedAt: b.now()}
			if err := tx.InsertSpawn(sp); err != nil {
				return err
			}
			cp := *sp
			w.emit([]string{"agents", "brief:" + br.ID}, "spawn.updated", &cp)
			ls = append(ls, launch{sp, tok, ag})
		}
		return w.audit(tx, a, "request_spawn", "", nil, map[string]any{"brief_id": br.ID, "n": in.N, "kind": in.Kind}, in.Model)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.Spawn, 0, len(ls))
	for _, l := range ls {
		c := b.ConnectInfo(l.ag.ID, l.tok)
		serr := b.o.Spawner.Spawn(ctx, spawner.Request{AgentID: l.ag.ID, Kind: l.ag.Kind, Model: l.ag.Model, Provider: l.ag.Provider,
			JoinLine: c.JoinLine, MCPURL: c.MCPURL, Token: l.tok})
		sp := l.sp
		_ = b.write(ctx, func(tx store.Tx, w *wctx) error {
			if serr != nil {
				sp.Status, sp.Error = "failed", strp(serr.Error())
				l.ag.Status, l.ag.PauseReason = model.AgentRevoked, strp("spawn failed")
				if err := tx.UpdateAgent(l.ag); err != nil {
					return err
				}
				w.agent(l.ag)
			} else {
				sp.Status = "spawned"
			}
			if err := tx.UpdateSpawn(sp); err != nil {
				return err
			}
			cp := *sp
			w.emit([]string{"agents", "brief:" + sp.BriefID}, "spawn.updated", &cp)
			return nil
		})
		out = append(out, sp)
	}
	return out, nil
}

func (b *Board) ListSpawns(ctx context.Context, briefID string) ([]*model.Spawn, error) {
	p, err := b.ListSpawnsPage(ctx, briefID, "", 0)
	return p.Items, err
}
