package instructions

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type StoreProjects struct {
	Store store.Store
}

func (p StoreProjects) ProjectOf(ctx context.Context, agentID string) (string, error) {
	var tasks []*model.Task
	err := p.Store.View(ctx, func(tx store.Tx) error {
		var err error
		tasks, err = tx.ListTasks(store.TaskFilter{Assignee: agentID, Status: []string{model.StatusInProgress, model.StatusAssigned, model.StatusChangesRequested}})
		return err
	})
	if err != nil {
		return "", err
	}
	best := ""
	rank := -1
	for _, t := range tasks {
		r := 0
		if t.Status == model.StatusInProgress {
			r = 1
		}
		if r > rank {
			best, rank = t.BriefID, r
		}
	}
	return best, nil
}

type StoreAuditor struct {
	Store store.Store
	Now   func() time.Time
}

func (a StoreAuditor) Record(ctx context.Context, e Event) error {
	ts := time.Now()
	if a.Now != nil {
		ts = a.Now()
	}
	note := e.Scope + " " + e.Kind
	if e.Message != "" {
		note += ": " + e.Message
	}
	if e.Actor.Role == model.RoleOwner {
		note = auth.AuditNote(ctx, note)
	}
	return a.Store.Atomic(ctx, func(tx store.Tx) error {
		return tx.InsertAudit(&model.AuditEvent{
			TS: ts.UTC(), AgentID: e.Actor.ID, Role: e.Actor.Role, Tool: e.Tool,
			Before: map[string]any{"version": e.Before}, After: map[string]any{"version": e.After}, Note: &note,
		})
	})
}
