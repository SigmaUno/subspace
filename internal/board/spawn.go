package board

import (
	"context"
	"encoding/json"
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
	tx = unscoped(tx)
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
		a = b.OverlayAgent(a)
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
	cfg := b.capCfg
	cfg.Gate = b.capGate
	return cfg
}

func (b *Board) SetCapacityGate(g capacity.Gate) {
	b.mu.Lock()
	b.capGate = g
	b.mu.Unlock()
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
	b.capOp.Lock()
	defer b.capOp.Unlock()
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		raw := jsonString(cfg)
		if err := tx.SetKV("capacity", raw); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "set_capacity", "", nil, map[string]any{"global_max_agents": cfg.GlobalMaxAgents, "limits": len(cfg.Limits)}, "")
	})
	if err != nil {
		b.resync(ctx, b.loadCapacity)
		return nil, err
	}
	b.mu.Lock()
	b.capCfg = cfg
	b.capDB = true
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

func (in *SpawnInput) validate() error {
	if in.N < 1 || in.N > 50 {
		return model.BadRequest("n must be 1..50")
	}
	switch in.Kind {
	case model.KindClaudeCode, model.KindCodex, model.KindOpenCode:
	default:
		return model.BadRequest("worker_kind must be claude-code, codex or opencode")
	}
	if in.Role == "" {
		in.Role = model.RoleWorker
	}
	if in.Role != model.RoleWorker && in.Role != model.RoleTester {
		return model.BadRequest("role must be worker or tester")
	}
	if !modelRe.MatchString(in.Model) {
		return model.BadRequest("model must match [A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}")
	}
	if in.Provider == "" {
		in.Provider = model.ProviderForKind(in.Kind)
	}
	return checkProvider(in.Kind, in.Provider)
}

func (b *Board) RequestSpawn(ctx context.Context, a model.Actor, in SpawnInput) ([]*model.Spawn, error) {
	if p, why := b.Paused(); p {
		return nil, model.Errf("paused", "Subspace is paused: %s", why)
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	var ls []launch
	err := b.writeAs(ctx, a, func(tx store.Tx, w *wctx) error {
		ls = ls[:0]
		br, err := b.checkSpawn(tx, w, a, in)
		if err != nil {
			return err
		}
		for i := 0; i < in.N; i++ {
			l, err := b.createSpawn(tx, w, a, br, in)
			if err != nil {
				return err
			}
			ls = append(ls, l)
		}
		return w.audit(tx, a, "request_spawn", "", nil, map[string]any{"brief_id": br.ID, "n": in.N, "kind": in.Kind}, in.Model)
	})
	if err != nil {
		if len(ls) > 0 {
			b.abandonSpawns(ctx, in.BriefID, ls)
		}
		return nil, err
	}
	out := make([]*model.Spawn, 0, len(ls))
	for _, l := range ls {
		out = append(out, b.launch(ctx, l))
	}
	return out, nil
}

func (b *Board) spawnCapacity(tx store.Tx) (capacity.Config, error) {
	cfg := b.capConfig()
	v, ok, err := tx.GetKV("capacity")
	if err != nil || !ok {
		return cfg, err
	}
	var c capacity.Config
	if json.Unmarshal([]byte(v), &c) == nil {
		c.Gate = cfg.Gate
		cfg = c
	}
	return cfg, nil
}

func (b *Board) checkSpawn(tx store.Tx, w *wctx, a model.Actor, in SpawnInput) (*model.Brief, error) {
	if err := pausedTx(tx); err != nil {
		return nil, err
	}
	cfg, err := b.spawnCapacity(tx)
	if err != nil {
		return nil, err
	}
	br, err := tx.GetBrief(in.BriefID)
	if err != nil {
		return nil, err
	}
	if br.Status != "open" {
		return nil, model.Conflict("brief %s is closed", br.ID)
	}
	if !b.providerAllowed(br, in.Provider) {
		return nil, model.Forbidden("provider %q is not allowed for brief %s", in.Provider, br.ID)
	}
	if err := b.checkBudget(tx, &model.Task{}, br); err != nil {
		return nil, err
	}
	sp := in
	if err := b.checkPolicy(tx, w, PolicyRequest{Action: "request_spawn", Actor: a, Brief: br, Spawn: &sp}); err != nil {
		return nil, err
	}
	u, busy, err := b.usage(tx)
	if err != nil {
		return nil, err
	}
	if err := cfg.Check(in.Provider, in.Model, in.N, u); err != nil {
		return nil, err
	}
	if br.Budget.MaxParallelAgent > 0 {
		if err := b.checkSpawnParallel(tx, br, in.N, busy); err != nil {
			return nil, err
		}
	}
	return br, nil
}

func (b *Board) checkSpawnParallel(tx store.Tx, br *model.Brief, n int, busy map[string]bool) error {
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
	if len(act)+recent+n > br.Budget.MaxParallelAgent {
		return model.Errf("capacity", "brief %s max_parallel_agents %d would be exceeded (%d active, %d pending)", br.ID, br.Budget.MaxParallelAgent, len(act), recent)
	}
	return nil
}

func (b *Board) createSpawn(tx store.Tx, w *wctx, a model.Actor, br *model.Brief, in SpawnInput) (launch, error) {
	seq, err := tx.NextID("sp-" + br.ID)
	if err != nil {
		return launch{}, err
	}
	n, _ := strconv.Atoi(seq[strings.LastIndex(seq, "-")+1:])
	agentID := fmt.Sprintf("%s-%s-%d", in.Kind, strings.ToLower(br.ID), n)
	ag, tok, err := b.newAgent(AgentInput{ID: agentID, Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider}, true)
	if err != nil {
		return launch{}, err
	}
	if err := tx.InsertAgent(ag); err != nil {
		return launch{}, err
	}
	w.agent(ag)
	sid, err := tx.NextID("S")
	if err != nil {
		return launch{}, err
	}
	sp := &model.Spawn{ID: sid, BriefID: br.ID, AgentID: agentID, Kind: in.Kind, Model: in.Model, Provider: in.Provider,
		Status: "requested", Spawner: b.o.Spawner.Name(), RequestedBy: a.ID, CreatedAt: b.now()}
	if err := tx.InsertSpawn(sp); err != nil {
		return launch{}, err
	}
	w.spawn(sp)
	return launch{sp: sp, tok: tok, ag: ag, repo: soleRepo(br)}, nil
}

func (w *wctx) spawn(sp *model.Spawn) {
	cp := *sp
	w.emit([]string{"agents", "brief:" + sp.BriefID}, "spawn.updated", &cp)
}

func soleRepo(br *model.Brief) string {
	if len(br.Repos) == 1 {
		return br.Repos[0]
	}
	return ""
}

func (b *Board) launch(ctx context.Context, l launch) *model.Spawn {
	c := b.ConnectInfo(l.ag.ID, l.tok)
	env, secret := b.agentCredentials(ctx, l.repo)
	serr := b.o.Spawner.Spawn(ctx, spawner.Request{AgentID: l.ag.ID, Kind: l.ag.Kind, Model: l.ag.Model, Provider: l.ag.Provider,
		JoinLine: c.JoinLine, MCPURL: c.MCPURL, Token: l.tok, Env: env})
	sp := *l.sp
	ag := *l.ag
	if serr != nil {
		msg := serr.Error()
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
		sp.Status, sp.Error = "failed", strp(msg)
		ag.Status, ag.PauseReason = model.AgentRevoked, strp("spawn failed")
	} else {
		sp.Status = "spawned"
	}
	var werr error
	for range 3 {
		werr = b.write(context.WithoutCancel(ctx), func(tx store.Tx, w *wctx) error {
			if serr != nil {
				if err := tx.UpdateAgent(&ag); err != nil {
					return err
				}
				w.agent(&ag)
			}
			if err := tx.UpdateSpawn(&sp); err != nil {
				return err
			}
			w.spawn(&sp)
			return nil
		})
		if werr == nil {
			break
		}
	}
	if werr != nil {
		b.log.Error("record spawn outcome", "spawn", sp.ID, "status", sp.Status, "err", werr)
		sp = *l.sp
	}
	return &sp
}

type launch struct {
	sp   *model.Spawn
	tok  string
	ag   *model.Agent
	repo string
}

func (b *Board) abandonSpawns(ctx context.Context, briefID string, ls []launch) {
	want := make(map[string]launch, len(ls))
	for _, l := range ls {
		want[l.sp.ID] = l
	}
	err := b.write(context.WithoutCancel(ctx), func(tx store.Tx, w *wctx) error {
		sps, err := tx.ListSpawns(store.SpawnFilter{BriefID: briefID})
		if err != nil {
			return err
		}
		for _, sp := range sps {
			l, ok := want[sp.ID]
			if !ok || sp.Status != "requested" || sp.AgentID != l.ag.ID {
				continue
			}
			ag, err := tx.GetAgent(sp.AgentID)
			if err != nil {
				return err
			}
			if ag.TokenHash != l.ag.TokenHash {
				continue
			}
			sp.Status, sp.Error = "failed", strp("spawn request outcome unknown")
			if err := tx.UpdateSpawn(sp); err != nil {
				return err
			}
			ag.Status, ag.PauseReason = model.AgentRevoked, strp("spawn failed")
			if err := tx.UpdateAgent(ag); err != nil {
				return err
			}
			w.agent(ag)
			w.spawn(sp)
		}
		return nil
	})
	if err != nil {
		b.log.Error("abandon spawns", "brief", briefID, "err", err)
	}
}

func (b *Board) ListSpawns(ctx context.Context, briefID string) ([]*model.Spawn, error) {
	p, err := b.ListSpawnsPage(ctx, briefID, "", 0)
	return p.Items, err
}
