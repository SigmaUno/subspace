package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func intg(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}

func decode(c *Call, v any) error {
	if len(c.Args) == 0 || string(c.Args) == "null" {
		return nil
	}
	if err := json.Unmarshal(c.Args, v); err != nil {
		return model.BadRequest("invalid arguments")
	}
	return nil
}

const maxReadThreadLimit = 1000

var (
	taskIDSchema = obj(map[string]any{"task_id": str("Task id, e.g. T-3")}, "task_id")
	taskBudget   = obj(map[string]any{"max_minutes": intg("max minutes"), "max_cost_eur": num("max cost in EUR")})
	taskSpec     = map[string]any{
		"ref": str("local reference so other tasks of the plan can depend on it"), "title": str("short title"), "spec": str("what to do"),
		"acceptance_criteria": strs("checkable acceptance criteria"), "repo": str("owner/name of the repo"),
		"depends_on": strs("task ids or refs"), "role": enum("worker or tester", "worker", "tester"), "budget": taskBudget,
	}
)

func NewRegistry(b *board.Board) *Registry {
	r := NewRegistry0()
	add := func(name, desc string, roles []string, ro bool, schema map[string]any, h Handler) {
		if schema == nil {
			schema = obj(map[string]any{})
		}
		r.Register(&Tool{Name: name, Description: desc, Roles: roles, ReadOnly: ro, Schema: schema, Handler: h})
	}

	add("get_instructions", "Return your rules and role instructions. Call this first.", AllRoles, true, nil,
		func(ctx context.Context, c *Call) (any, error) {
			s, err := b.Instructions(ctx, c.Actor)
			return map[string]any{"instructions": s, "role": c.Actor.Role, "agent_id": c.Actor.ID}, err
		})
	add("whoami", "Return your agent record.", AllRoles, true, nil, func(ctx context.Context, c *Call) (any, error) {
		a, err := b.GetAgent(ctx, c.Actor.ID)
		p, _ := b.Paused()
		return map[string]any{"agent": a, "paused": p, "server_time": time.Now().UTC()}, err
	})
	add("list_my_tasks", "List tasks where you are assignee or reviewer.", AllRoles, true,
		obj(map[string]any{"status": strs("filter by status")}), func(ctx context.Context, c *Call) (any, error) {
			var in struct{ Status []string }
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			ts, err := b.ListMyTasks(ctx, c.Actor, in.Status)
			return map[string]any{"tasks": ts}, err
		})
	add("get_task", "Get one task.", AllRoles, true, taskIDSchema, func(ctx context.Context, c *Call) (any, error) {
		var in struct {
			TaskID string `json:"task_id"`
		}
		if err := decode(c, &in); err != nil {
			return nil, err
		}
		t, err := b.GetTask(ctx, c.Actor, in.TaskID)
		return map[string]any{"task": t}, err
	})
	add("heartbeat", "Extend the lease of your in_progress tasks. Call every few minutes while working.", AllRoles, true,
		obj(map[string]any{"task_id": str("only this task"), "note": str("ignored, for humans")}),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID string `json:"task_id"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			return b.Heartbeat(ctx, c.Actor, in.TaskID)
		})
	add("update_status", "Report progress or block a task you are working on.", AllRoles, false,
		obj(map[string]any{"task_id": str("task"), "status": enum("new status", "in_progress", "blocked"),
			"progress_pct": intg("0-100"), "note": str("what changed"), "cost_eur": num("cumulative cost spent so far")}, "task_id", "status"),
		func(ctx context.Context, c *Call) (any, error) {
			var in board.StatusInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.UpdateStatus(ctx, c.Actor, in)
			return map[string]any{"task": t}, err
		})
	add("add_note", "Add a note to a task thread (or your own thread without task_id).", AllRoles, false,
		obj(map[string]any{"task_id": str("task"), "body": str("note")}, "body"), func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID string `json:"task_id"`
				Body   string `json:"body"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			m, err := b.AddNote(ctx, c.Actor, in.TaskID, in.Body)
			return map[string]any{"message": m}, err
		})
	add("ask_owner", "Ask the owner a specific question. The answer arrives in your thread.", AllRoles, false,
		obj(map[string]any{"task_id": str("related task"), "question": str("specific question"), "options": strs("proposed answers")}, "question"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID   string   `json:"task_id"`
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			q, err := b.AskOwner(ctx, c.Actor, in.TaskID, in.Question, in.Options)
			return map[string]any{"question": q}, err
		})
	add("post_message", "Post a message to a thread you may access (agent:me, task:<id>, brief:<id>).", AllRoles, false,
		obj(map[string]any{"thread": str("kind:id"), "body": str("message")}, "thread", "body"), func(ctx context.Context, c *Call) (any, error) {
			var in struct{ Thread, Body string }
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			m, err := b.PostMessage(ctx, c.Actor, in.Thread, in.Body, model.MsgMessage)
			return map[string]any{"message": m}, err
		})
	add("read_thread", "Read a thread you may access.", AllRoles, true,
		obj(map[string]any{"thread": str("kind:id"), "since": intg("only messages with id greater than this"), "limit": intg("max messages")}, "thread"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				Thread string
				Since  int64
				Limit  int
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			if in.Limit > maxReadThreadLimit {
				in.Limit = maxReadThreadLimit
			}
			ms, err := b.ReadThread(ctx, c.Actor, in.Thread, in.Since, in.Limit)
			return map[string]any{"messages": ms}, err
		})

	add("claim_task", "Claim a task assigned to you; grants a lease (heartbeat to keep it).", WorkTester, false, taskIDSchema,
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID string `json:"task_id"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.ClaimTask(ctx, c.Actor, in.TaskID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"task": t, "lease_expires_at": t.LeaseExpiresAt}, nil
		})
	add("release_task", "Give a claimed task back.", WorkTester, false,
		obj(map[string]any{"task_id": str("task"), "reason": str("why")}, "task_id"), func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID string `json:"task_id"`
				Reason string `json:"reason"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.ReleaseTask(ctx, c.Actor, in.TaskID, in.Reason)
			return map[string]any{"task": t}, err
		})
	add("submit", "Submit finished work (PR open) for review.", WorkTester, false,
		obj(map[string]any{"task_id": str("task"), "pr_url": str("pull request URL"), "summary": str("what was done")}, "task_id", "summary"),
		func(ctx context.Context, c *Call) (any, error) {
			var in board.SubmitInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.Submit(ctx, c.Actor, in)
			return map[string]any{"task": t}, err
		})
	add("report_tests", "Report test results for a task.", TesterOnly, false,
		obj(map[string]any{"task_id": str("task"), "passed": intg("passed"), "failed": intg("failed"), "log_excerpt": str("relevant log excerpt")}, "task_id", "passed", "failed"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID     string `json:"task_id"`
				Passed     int    `json:"passed"`
				Failed     int    `json:"failed"`
				LogExcerpt string `json:"log_excerpt"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.ReportTests(ctx, c.Actor, in.TaskID, in.Passed, in.Failed, in.LogExcerpt)
			return map[string]any{"task": t}, err
		})

	add("list_reviews", "List submitted tasks awaiting your review.", ReviewOnly, true, nil, func(ctx context.Context, c *Call) (any, error) {
		ts, err := b.ListReviews(ctx, c.Actor)
		return map[string]any{"tasks": ts}, err
	})
	add("review", "Approve or request changes on a submitted task.", ReviewOnly, false,
		obj(map[string]any{"task_id": str("task"), "verdict": enum("verdict", "approve", "changes_requested"), "comments": str("review comments")}, "task_id", "verdict", "comments"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct{ TaskID, Verdict, Comments string }
			var raw struct {
				TaskID   string `json:"task_id"`
				Verdict  string `json:"verdict"`
				Comments string `json:"comments"`
			}
			_ = in
			if err := decode(c, &raw); err != nil {
				return nil, err
			}
			t, err := b.Review(ctx, c.Actor, raw.TaskID, raw.Verdict, raw.Comments)
			return map[string]any{"task": t}, err
		})

	add("get_brief", "Get a brief.", LeaderOnly, true, obj(map[string]any{"brief_id": str("brief")}, "brief_id"), func(ctx context.Context, c *Call) (any, error) {
		var in struct {
			BriefID string `json:"brief_id"`
		}
		if err := decode(c, &in); err != nil {
			return nil, err
		}
		br, err := b.GetBrief(ctx, in.BriefID)
		return map[string]any{"brief": br}, err
	})
	add("list_briefs", "List briefs.", LeaderOnly, true, obj(map[string]any{"status": enum("filter", "open", "closed")}), func(ctx context.Context, c *Call) (any, error) {
		var in struct{ Status string }
		if err := decode(c, &in); err != nil {
			return nil, err
		}
		bs, err := b.ListBriefs(ctx, in.Status)
		return map[string]any{"briefs": bs}, err
	})
	add("propose_plan", "Propose a task breakdown for a brief. The owner must approve it.", LeaderOnly, false,
		obj(map[string]any{"brief_id": str("brief"), "note": str("rationale"),
			"tasks": map[string]any{"type": "array", "items": obj(taskSpec, "title", "spec", "repo")}}, "brief_id", "tasks"),
		func(ctx context.Context, c *Call) (any, error) {
			var in board.PlanInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			p, err := b.ProposePlan(ctx, c.Actor, in)
			return map[string]any{"plan": p}, err
		})
	ts := map[string]any{"brief_id": str("brief")}
	for k, v := range taskSpec {
		ts[k] = v
	}
	add("create_task", "Add a task to a brief (ready if the brief's plan is approved, else backlog).", LeaderOnly, false,
		obj(ts, "brief_id", "title", "spec", "repo"), func(ctx context.Context, c *Call) (any, error) {
			var in board.TaskInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.CreateTask(ctx, c.Actor, in)
			return map[string]any{"task": t}, err
		})
	add("update_task", "Edit a task; status may be blocked, ready (unblock) or cancelled.", LeaderOnly, false,
		obj(map[string]any{"task_id": str("task"), "title": str("title"), "spec": str("spec"), "acceptance_criteria": strs("criteria"),
			"depends_on": strs("deps"), "budget": taskBudget, "status": enum("status", "blocked", "ready", "cancelled")}, "task_id"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID string `json:"task_id"`
				board.TaskPatch
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.UpdateTask(ctx, c.Actor, in.TaskID, in.TaskPatch)
			return map[string]any{"task": t}, err
		})
	add("assign", "Assign a ready task to an agent, or a reviewer to a submitted task (as=reviewer).", LeaderOnly, false,
		obj(map[string]any{"task_id": str("task"), "agent_id": str("agent"), "as": enum("assignee (default) or reviewer", "assignee", "reviewer")}, "task_id", "agent_id"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID  string `json:"task_id"`
				AgentID string `json:"agent_id"`
				As      string `json:"as"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.Assign(ctx, c.Actor, in.TaskID, in.AgentID, in.As)
			return map[string]any{"task": t}, err
		})
	add("reassign", "Move a task to another agent (releases its lease).", LeaderOnly, false,
		obj(map[string]any{"task_id": str("task"), "agent_id": str("agent"), "reason": str("why")}, "task_id", "agent_id"),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID  string `json:"task_id"`
				AgentID string `json:"agent_id"`
				Reason  string `json:"reason"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			t, err := b.Reassign(ctx, c.Actor, in.TaskID, in.AgentID, in.Reason)
			return map[string]any{"task": t}, err
		})
	add("board", "Kanban columns, for one brief or all.", LeaderOnly, true, obj(map[string]any{"brief_id": str("brief")}), func(ctx context.Context, c *Call) (any, error) {
		var in struct {
			BriefID string `json:"brief_id"`
		}
		if err := decode(c, &in); err != nil {
			return nil, err
		}
		v, err := b.Board(ctx, in.BriefID)
		if v != nil {
			return map[string]any{"columns": v.Columns}, err
		}
		return nil, err
	})
	add("list_agents", "List registered agents.", LeaderOnly, true,
		obj(map[string]any{"role": enum("role", "leader", "worker", "tester", "reviewer"), "status": enum("status", "active", "paused", "revoked")}),
		func(ctx context.Context, c *Call) (any, error) {
			var in struct{ Role, Status string }
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			as, err := b.ListAgents(ctx, in.Role, in.Status)
			return map[string]any{"agents": as}, err
		})
	add("capacity", "Read global inference capacity and current usage.", LeaderOnly, true, nil, func(ctx context.Context, c *Call) (any, error) {
		return b.Capacity(ctx)
	})
	add("request_spawn", "Ask Subspace to launch N more agents (checked against capacity and max_parallel_agents).", LeaderOnly, false,
		obj(map[string]any{"brief_id": str("brief"), "worker_kind": enum("kind", "claude-code", "codex", "opencode"), "model": str("model name"),
			"n": intg("1-50"), "role": enum("worker (default) or tester", "worker", "tester"), "provider": str("override provider")},
			"brief_id", "worker_kind", "model", "n"),
		func(ctx context.Context, c *Call) (any, error) {
			var in board.SpawnInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			sp, err := b.RequestSpawn(ctx, c.Actor, in)
			return map[string]any{"spawns": sp}, err
		})
	return r
}

func DefaultRegistry() *Registry { return NewRegistry(nil) }
