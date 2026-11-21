package mcp

import (
	"context"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxTrackedTasks = 100000

type taskState struct{ status, assignee, reviewer string }

type PushWatcher struct {
	Push *Push

	tasks  map[string]taskState
	agents map[string]string
}

func (w *PushWatcher) Activate(ctx context.Context, hub events.Hub) {
	sub := hub.Subscribe([]string{events.TopicAll})
	w.tasks, w.agents = map[string]taskState{}, map[string]string{}
	go func() {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.C():
				if !ok {
					return
				}
				if n := sub.TakeDropped(); n > 0 {
					w.Push.Resync(n)
				}
				w.handle(ctx, ev)
			}
		}
	}()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (w *PushWatcher) handle(ctx context.Context, ev events.Event) {
	switch d := ev.Data.(type) {
	case *model.Task:
		w.task(ctx, d)
	case *model.Message:
		w.message(ctx, d)
	case *model.Question:
		if ev.Type == "question.answered" {
			e := map[string]any{"type": "answer", "question_id": d.ID}
			if t := deref(d.TaskID); t != "" {
				e["task_id"] = t
			}
			w.Push.Event(d.AgentID, e)
		}
	case *model.Agent:
		w.agent(d)
	default:
		switch ev.Type {
		case "control.paused":
			w.Push.Broadcast(MethodSubspaceEvent, map[string]any{"type": "paused", "scope": "all"})
		case "control.resumed":
			w.Push.Broadcast(MethodSubspaceEvent, map[string]any{"type": "resumed", "scope": "all"})
		}
	}
}

func (w *PushWatcher) task(ctx context.Context, t *model.Task) {
	if len(w.tasks) >= maxTrackedTasks {
		clear(w.tasks)
	}
	prev := w.tasks[t.ID]
	cur := taskState{status: t.Status, assignee: deref(t.Assignee), reviewer: deref(t.Reviewer)}
	w.tasks[t.ID] = cur
	if cur.assignee != "" {
		switch {
		case cur.status == model.StatusAssigned && (prev.status != cur.status || prev.assignee != cur.assignee):
			w.Push.Event(cur.assignee, map[string]any{"type": "task_assigned", "task_id": t.ID})
		case cur.status == model.StatusChangesRequested && prev.status != cur.status:
			w.Push.Event(cur.assignee, map[string]any{"type": "changes_requested", "task_id": t.ID})
		}
	}
	if cur.reviewer != "" && cur.reviewer != prev.reviewer {
		w.Push.Event(cur.reviewer, map[string]any{"type": "task_assigned", "task_id": t.ID})
	}
	w.Push.Updated(ctx, TaskResource(t.ID))
}

func (w *PushWatcher) message(ctx context.Context, m *model.Message) {
	w.Push.Updated(ctx, ThreadResource(m.ThreadKind, m.ThreadID))
	if m.Kind != model.MsgMessage {
		return
	}
	thread := m.ThreadKind + ":" + m.ThreadID
	e := func(taskID string) map[string]any {
		out := map[string]any{"type": "message", "thread": thread, "message_id": m.ID}
		if taskID != "" {
			out["task_id"] = taskID
		}
		return out
	}
	switch m.ThreadKind {
	case model.ThreadAgent:
		if m.ThreadID != m.AuthorID {
			w.Push.Event(m.ThreadID, e(""))
		}
	case model.ThreadTask:
		if w.Push.Tasks == nil {
			return
		}
		assignee, reviewer, ok := w.Push.Tasks.Owners(ctx, m.ThreadID)
		if !ok {
			return
		}
		for _, id := range []string{assignee, reviewer} {
			if id != "" && id != m.AuthorID {
				w.Push.Event(id, e(m.ThreadID))
			}
		}
	}
}

func (w *PushWatcher) agent(a *model.Agent) {
	if len(w.agents) >= maxTrackedTasks {
		clear(w.agents)
	}
	prev := w.agents[a.ID]
	w.agents[a.ID] = a.Status
	switch {
	case a.Status == model.AgentRevoked:
		w.Push.DropAgent(a.ID)
	case a.Status == model.AgentPaused && prev != model.AgentPaused:
		w.Push.Event(a.ID, map[string]any{"type": "paused", "scope": "agent"})
	case a.Status == model.AgentActive && prev == model.AgentPaused:
		w.Push.Event(a.ID, map[string]any{"type": "resumed", "scope": "agent"})
	}
}
