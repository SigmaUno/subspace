package autopilot

import (
	"slices"
	"sort"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	ActAssign = "assign"
	ActReview = "review"
	ActSpawn  = "spawn"
)

const maxSpawnBatch = 50

type SpawnConfig struct {
	Kind     string `json:"worker_kind"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

type Snapshot struct {
	Paused   bool
	Briefs   []*model.Brief
	Enabled  map[string]bool
	Tasks    []*model.Task
	Agents   []*model.Agent
	Capacity board.CapacityView
	Spawn    map[string]SpawnConfig
	Conflict conflict.Mode
}

type Action struct {
	Kind    string            `json:"kind"`
	BriefID string            `json:"brief_id"`
	TaskID  string            `json:"task_id,omitempty"`
	AgentID string            `json:"agent_id,omitempty"`
	Spawn   *board.SpawnInput `json:"spawn,omitempty"`
}

type needKey struct{ brief, role string }

func Plan(s Snapshot) []Action {
	if s.Paused {
		return nil
	}
	briefs := map[string]*model.Brief{}
	for _, b := range s.Briefs {
		if s.Enabled[b.ID] && b.Status == "open" && !overBudget(b) {
			briefs[b.ID] = b
		}
	}
	if len(briefs) == 0 {
		return nil
	}

	agents := map[string]*model.Agent{}
	load := map[string]int{}
	for _, a := range s.Agents {
		agents[a.ID] = a
		load[a.ID] = a.AssignedCount
	}
	revLoad := map[string]int{}
	busy := map[string]map[string]bool{}
	tasks := append([]*model.Task(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority > tasks[j].Priority
		}
		return idLess(tasks[i].ID, tasks[j].ID)
	})
	for _, t := range tasks {
		if t.Reviewer != nil && (t.Status == model.StatusSubmitted || t.Status == model.StatusInReview) {
			revLoad[*t.Reviewer]++
		}
		if t.Assignee != nil && (t.Status == model.StatusAssigned || t.Status == model.StatusInProgress) {
			if busy[t.BriefID] == nil {
				busy[t.BriefID] = map[string]bool{}
			}
			busy[t.BriefID][*t.Assignee] = true
		}
	}

	avoid := s.Conflict != conflict.Off
	var held []*model.Task
	for _, t := range tasks {
		if t.Assignee != nil && slices.Contains(conflict.Held, t.Status) {
			held = append(held, t)
		}
	}

	var acts []Action
	need := map[needKey]int{}
	for _, t := range tasks {
		br := briefs[t.BriefID]
		if br == nil || t.Status != model.StatusReady {
			continue
		}
		eligible := func(a *model.Agent) bool { return a.Role == t.Role && allowed(br, a.Provider) }
		owner, ok := overlapOwner(t, held, avoid)
		if !ok {
			continue
		}
		var a *model.Agent
		if owner != "" {
			a = agents[owner]
			if a == nil || a.Status != model.AgentActive || !eligible(a) || load[a.ID] > 0 {
				continue
			}
		} else {
			a = pick(s.Agents, load, eligible)
			if a == nil {
				need[needKey{br.ID, t.Role}]++
				continue
			}
		}
		load[a.ID]++
		acts = append(acts, Action{Kind: ActAssign, BriefID: br.ID, TaskID: t.ID, AgentID: a.ID})
		if avoid && len(t.Paths) > 0 {
			cp := *t
			cp.Assignee, cp.Status = &a.ID, model.StatusAssigned
			held = append(held, &cp)
		}
	}

	for _, t := range tasks {
		br := briefs[t.BriefID]
		if br == nil || t.Status != model.StatusSubmitted || t.Reviewer != nil {
			continue
		}
		var worker string
		if t.Assignee != nil && agents[*t.Assignee] != nil {
			worker = agents[*t.Assignee].Provider
		}
		a := pick(s.Agents, revLoad, func(a *model.Agent) bool {
			return a.Role == model.RoleReviewer && allowed(br, a.Provider) && (worker == "" || a.Provider != worker)
		})
		if a == nil {
			continue
		}
		revLoad[a.ID]++
		acts = append(acts, Action{Kind: ActReview, BriefID: br.ID, TaskID: t.ID, AgentID: a.ID})
	}

	keys := make([]needKey, 0, len(need))
	for k := range need {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].brief != keys[j].brief {
			return idLess(keys[i].brief, keys[j].brief)
		}
		return keys[i].role < keys[j].role
	})
	c := newCapRoom(s.Capacity)
	room := map[string]int{}
	for _, k := range keys {
		br := briefs[k.brief]
		cfg := s.Spawn[br.ID]
		if cfg.Kind == "" || cfg.Model == "" {
			continue
		}
		prov := cfg.Provider
		if prov == "" {
			prov = model.ProviderForKind(cfg.Kind)
		}
		if !allowed(br, prov) {
			continue
		}
		n := min(need[k], maxSpawnBatch, c.free(prov, cfg.Model))
		if br.Budget.MaxParallelAgent > 0 {
			r, ok := room[br.ID]
			if !ok {
				r = br.Budget.MaxParallelAgent - len(busy[br.ID])
			}
			n = min(n, r)
			room[br.ID] = r - max(n, 0)
		}
		if n <= 0 {
			continue
		}
		c.take(prov, cfg.Model, n)
		acts = append(acts, Action{Kind: ActSpawn, BriefID: br.ID, Spawn: &board.SpawnInput{
			BriefID: br.ID, Kind: cfg.Kind, Model: cfg.Model, N: n, Role: k.role, Provider: prov}})
	}
	return acts
}

func overlapOwner(t *model.Task, held []*model.Task, avoid bool) (string, bool) {
	if !avoid {
		return "", true
	}
	owner := ""
	for _, h := range conflict.Conflicts(t, "", held) {
		if owner != "" && owner != *h.Assignee {
			return "", false
		}
		owner = *h.Assignee
	}
	return owner, true
}

func pick(agents []*model.Agent, load map[string]int, ok func(*model.Agent) bool) *model.Agent {
	var best *model.Agent
	for _, a := range agents {
		if a.Status != model.AgentActive || !ok(a) {
			continue
		}
		if best == nil || load[a.ID] < load[best.ID] || (load[a.ID] == load[best.ID] && idLess(a.ID, best.ID)) {
			best = a
		}
	}
	return best
}

func allowed(b *model.Brief, provider string) bool {
	if len(b.AllowedProviders) == 0 {
		return true
	}
	for _, p := range b.AllowedProviders {
		if p == provider {
			return true
		}
	}
	return false
}

func overBudget(b *model.Brief) bool {
	return b.Budget.MaxCostEUR > 0 && b.CostUsed >= b.Budget.MaxCostEUR
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

type capRoom struct {
	v     board.CapacityView
	taken map[[2]string]int
	total int
}

func newCapRoom(v board.CapacityView) *capRoom {
	return &capRoom{v: v, taken: map[[2]string]int{}}
}

func match(pat, v string) bool { return pat == "*" || pat == v }

func (c *capRoom) free(provider, mdl string) int {
	n := maxSpawnBatch
	if c.v.GlobalMaxAgents > 0 {
		n = min(n, c.v.GlobalMaxAgents-c.v.AgentsInUse-c.total)
	}
	for _, l := range c.v.Limits {
		if l.MaxConcurrency == 0 || !match(l.Provider, provider) || !match(l.Model, mdl) {
			continue
		}
		used := l.InUse
		for k, t := range c.taken {
			if match(l.Provider, k[0]) && match(l.Model, k[1]) {
				used += t
			}
		}
		n = min(n, l.MaxConcurrency-used)
	}
	return n
}

func (c *capRoom) take(provider, mdl string, n int) {
	c.taken[[2]string{provider, mdl}] += n
	c.total += n
}
