package mcp

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/search"
)

const (
	MaxInfoItems   = 200
	infoSearchRows = 100
)

type InfoSource interface {
	Forecast(ctx context.Context, briefID string) (any, error)
	Report(ctx context.Context, briefID string) (report.Report, error)
	Search(ctx context.Context, q, kind string, limit int) ([]search.Result, error)
	Decisions(ctx context.Context) ([]*decisions.Decision, error)
	Components(ctx context.Context, briefID string) ([]components.Component, error)
	SetComponents(ctx context.Context, actor model.Actor, briefID string, specs []components.Spec) ([]components.Component, error)
	AgentStats(ctx context.Context, agentID string) ([]any, error)
}

var infoSources sync.Map

func BindLeaderInfo(b *board.Board, s InfoSource) { infoSources.Store(b, s) }

func truncate[T any](xs []T) ([]T, bool) {
	if xs == nil {
		return []T{}, false
	}
	if len(xs) > MaxInfoItems {
		return xs[:MaxInfoItems], true
	}
	return xs, false
}

type decisionTitle struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	BriefID string `json:"brief_id,omitempty"`
	TaskID  string `json:"task_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

func RegisterLeaderInfo(r *Registry, b *board.Board) {
	source := func() (InfoSource, error) {
		v, _ := infoSources.Load(b)
		s, _ := v.(InfoSource)
		if s == nil {
			return nil, model.Errf("internal", "leader information tools are not available")
		}
		return s, nil
	}
	visible := func(ctx context.Context, c *Call, briefID string) (bool, error) {
		if briefID == "" {
			return true, nil
		}
		return b.BriefVisible(ctx, c.Actor, briefID)
	}
	visibleIn := func(ctx context.Context, c *Call) func(string) (bool, error) {
		seen := map[string]bool{}
		return func(briefID string) (bool, error) {
			if ok, known := seen[briefID]; known {
				return ok, nil
			}
			ok, err := visible(ctx, c, briefID)
			seen[briefID] = ok
			return ok, err
		}
	}
	briefArg := func(ctx context.Context, c *Call) (string, error) {
		var in struct {
			BriefID string `json:"brief_id"`
		}
		if err := decode(c, &in); err != nil {
			return "", err
		}
		if in.BriefID == "" {
			return "", model.BadRequest("brief_id is required")
		}
		ok, err := visible(ctx, c, in.BriefID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", model.NotFound("brief %s not found", in.BriefID)
		}
		return in.BriefID, nil
	}
	briefSchema := obj(map[string]any{"brief_id": str("brief id, e.g. B-1")}, "brief_id")

	r.Register(&Tool{
		Name:        "get_forecast",
		Description: "Monte Carlo forecast of a brief: remaining cost and schedule ranges, burn rate and risks.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      briefSchema,
		Handler: func(ctx context.Context, c *Call) (any, error) {
			id, err := briefArg(ctx, c)
			if err != nil {
				return nil, err
			}
			s, err := source()
			if err != nil {
				return nil, err
			}
			f, err := s.Forecast(ctx, id)
			if err != nil {
				return nil, err
			}
			return map[string]any{"forecast": f}, nil
		},
	})

	r.Register(&Tool{
		Name:        "get_report",
		Description: "Delivery report of a brief: per-task timings and reviews, cost, throughput, critical path and outliers. Lists are cut at 200 items (truncated).",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      briefSchema,
		Handler: func(ctx context.Context, c *Call) (any, error) {
			id, err := briefArg(ctx, c)
			if err != nil {
				return nil, err
			}
			s, err := source()
			if err != nil {
				return nil, err
			}
			rep, err := s.Report(ctx, id)
			if err != nil {
				return nil, err
			}
			var t1, t2, t3 bool
			rep.Tasks, t1 = truncate(rep.Tasks)
			rep.Outliers, t2 = truncate(rep.Outliers)
			rep.Throughput.Hourly, t3 = truncate(rep.Throughput.Hourly)
			return map[string]any{"report": rep, "truncated": t1 || t2 || t3}, nil
		},
	})

	r.Register(&Tool{
		Name:        "search",
		Description: "Full-text search over tasks, messages, agents and briefs. Returns snippets with «matches».",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema: obj(map[string]any{
			"query": str("text to find, at most 200 characters"),
			"kind":  enum("restrict to one kind", "task", "message", "agent", "brief"),
			"limit": intg("maximum results, 1-100, default 20"),
		}, "query"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			Query string `json:"query"`
			Kind  string `json:"kind"`
			Limit int    `json:"limit"`
		}) (any, error) {
			if in.Limit < 0 || in.Limit > infoSearchRows {
				return nil, model.BadRequest("limit must be between 1 and %d", infoSearchRows)
			}
			s, err := source()
			if err != nil {
				return nil, err
			}
			rs, err := s.Search(ctx, in.Query, in.Kind, in.Limit)
			if err != nil {
				return nil, err
			}
			rs, cut := truncate(rs)
			return map[string]any{"results": rs, "truncated": cut}, nil
		}),
	})

	r.Register(&Tool{
		Name:        "agent_stats",
		Description: "Performance per agent: completed tasks, durations, review rounds, approval rate, cost and score. Omit agent_id for all agents, best score first.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      obj(map[string]any{"agent_id": str("a single agent id")}),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			AgentID string `json:"agent_id"`
		}) (any, error) {
			src, err := source()
			if err != nil {
				return nil, err
			}
			stats, err := src.AgentStats(ctx, in.AgentID)
			if err != nil {
				return nil, err
			}
			out, cut := truncate(stats)
			return map[string]any{"agents": out, "truncated": cut}, nil
		}),
	})

	r.Register(&Tool{
		Name:        "list_decisions",
		Description: "Decisions waiting for the owner (questions, plans, merges, unblocks, stale agents), titles only, most important first.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      obj(map[string]any{}),
		Handler: func(ctx context.Context, c *Call) (any, error) {
			s, err := source()
			if err != nil {
				return nil, err
			}
			ds, err := s.Decisions(ctx)
			if err != nil {
				return nil, err
			}
			titles := make([]decisionTitle, 0, len(ds))
			seen := visibleIn(ctx, c)
			for _, d := range ds {
				ok, err := seen(d.BriefID)
				if err != nil {
					return nil, err
				}
				if ok {
					titles = append(titles, decisionTitle{ID: d.ID, Kind: d.Kind, Title: d.Title, BriefID: d.BriefID, TaskID: d.TaskID, AgentID: d.AgentID})
				}
			}
			out, cut := truncate(titles)
			return map[string]any{"decisions": out, "truncated": cut}, nil
		},
	})

	r.Register(&Tool{
		Name:        "list_spawns",
		Description: "Agent spawns requested so far, optionally for one brief.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      obj(map[string]any{"brief_id": str("only spawns of this brief")}),
		Handler: handle(func(ctx context.Context, c *Call, in briefRef) (any, error) {
			all, err := b.ListSpawns(ctx, in.BriefID)
			if err != nil {
				return nil, err
			}
			sps := make([]*model.Spawn, 0, len(all))
			seen := visibleIn(ctx, c)
			for _, sp := range all {
				ok, err := seen(sp.BriefID)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				if sp.Error != nil {
					cp := *sp
					msg := "spawn failed; the owner can see the details"
					cp.Error = &msg
					sp = &cp
				}
				sps = append(sps, sp)
			}
			out, cut := truncate(sps)
			return map[string]any{"spawns": out, "truncated": cut}, nil
		}),
	})

	r.Register(&Tool{
		Name:        "get_components",
		Description: "Architecture components of a brief with their paths and dependencies.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      briefSchema,
		Handler: func(ctx context.Context, c *Call) (any, error) {
			id, err := briefArg(ctx, c)
			if err != nil {
				return nil, err
			}
			s, err := source()
			if err != nil {
				return nil, err
			}
			cs, err := s.Components(ctx, id)
			if err != nil {
				return nil, err
			}
			out, cut := truncate(cs)
			return map[string]any{"components": out, "truncated": cut}, nil
		},
	})

	r.Register(&Tool{
		Name: "set_components",
		Description: "Replace the architecture component plan of a brief. Components are numbered C-1, C-2, ... in list order; depends_on names other components of the same list. " +
			"Tasks link to a component with component_id. Rejects an empty plan, duplicate names, unknown, self or cyclic dependencies and absolute or escaping paths.",
		Roles: LeaderOnly,
		Schema: obj(map[string]any{
			"brief_id": str("brief id, e.g. B-1"),
			"components": map[string]any{"type": "array", "description": "the complete component plan", "items": obj(map[string]any{
				"name":        str("unique component name"),
				"description": str("what the component is responsible for"),
				"paths":       strs("repository-relative paths the component owns"),
				"depends_on":  strs("names of components this one depends on"),
			}, "name")},
		}, "brief_id", "components"),
		Handler: func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				Components []components.Spec `json:"components"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			id, err := briefArg(ctx, c)
			if err != nil {
				return nil, err
			}
			s, err := source()
			if err != nil {
				return nil, err
			}
			cs, err := s.SetComponents(ctx, c.Actor, id, in.Components)
			if err != nil {
				return nil, err
			}
			return map[string]any{"components": cs}, nil
		},
	})
}
