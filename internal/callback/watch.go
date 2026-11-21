package callback

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxTracked = 100000

type TaskOwners interface {
	Owners(ctx context.Context, taskID string) (assignee, reviewer string, ok bool)
}

type taskState struct{ status, assignee, reviewer string }

type Watcher struct {
	D     *Dispatcher
	Tasks TaskOwners

	once sync.Once
	seen map[string]taskState
}

func (w *Watcher) Activate(ctx context.Context, hub events.Hub) {
	w.once.Do(func() {
		sub := hub.Subscribe([]string{events.TopicAll})
		w.seen = map[string]taskState{}
		go w.loop(ctx, sub)
	})
}

func (w *Watcher) loop(ctx context.Context, sub events.Subscription) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			w.Handle(ctx, ev)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (w *Watcher) Handle(ctx context.Context, ev events.Event) {
	if w.seen == nil {
		w.seen = map[string]taskState{}
	}
	switch d := ev.Data.(type) {
	case *model.Task:
		w.task(ctx, d)
	case *model.Message:
		w.message(ctx, d)
	case *model.Question:
		if ev.Type == "question.answered" {
			w.D.Enqueue(ctx, d.AgentID, Reason{Type: ReasonAnswer, QuestionID: d.ID, TaskID: deref(d.TaskID)})
		}
	}
}

func (w *Watcher) task(ctx context.Context, t *model.Task) {
	if len(w.seen) >= maxTracked {
		clear(w.seen)
	}
	prev := w.seen[t.ID]
	cur := taskState{status: t.Status, assignee: deref(t.Assignee), reviewer: deref(t.Reviewer)}
	w.seen[t.ID] = cur
	if cur.assignee != "" {
		switch {
		case cur.status == model.StatusAssigned && (prev.status != cur.status || prev.assignee != cur.assignee):
			w.D.Enqueue(ctx, cur.assignee, Reason{Type: ReasonAssigned, TaskID: t.ID})
		case cur.status == model.StatusChangesRequested && prev.status != cur.status:
			w.D.Enqueue(ctx, cur.assignee, Reason{Type: ReasonChangesRequested, TaskID: t.ID})
		}
	}
	if cur.reviewer != "" && cur.reviewer != prev.reviewer {
		w.D.Enqueue(ctx, cur.reviewer, Reason{Type: ReasonAssigned, TaskID: t.ID})
	}
}

func (w *Watcher) message(ctx context.Context, m *model.Message) {
	if m.AuthorRole != model.RoleLeader && m.AuthorRole != model.RoleOwner {
		return
	}
	if m.Kind != model.MsgMessage {
		return
	}
	thread := m.ThreadKind + ":" + m.ThreadID
	switch m.ThreadKind {
	case model.ThreadAgent:
		if m.ThreadID != m.AuthorID {
			w.D.Enqueue(ctx, m.ThreadID, Reason{Type: ReasonMessage, Thread: thread})
		}
	case model.ThreadTask:
		if w.Tasks == nil {
			return
		}
		assignee, reviewer, ok := w.Tasks.Owners(ctx, m.ThreadID)
		if !ok {
			return
		}
		for _, id := range []string{assignee, reviewer} {
			if id != "" && id != m.AuthorID {
				w.D.Enqueue(ctx, id, Reason{Type: ReasonMessage, TaskID: m.ThreadID, Thread: thread})
			}
		}
	}
}
