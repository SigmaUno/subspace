package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type decisionActions struct{ b *board.Board }

func (d decisionActions) AnswerQuestion(ctx context.Context, id, answer string) (*model.Question, error) {
	return d.b.Answer(ctx, id, answer)
}

func (d decisionActions) ApprovePlan(ctx context.Context, id, note string) (*model.Plan, error) {
	return d.b.ApprovePlan(ctx, id, note)
}

func (d decisionActions) RejectPlan(ctx context.Context, id, reason string) (*model.Plan, error) {
	return d.b.RejectPlan(ctx, id, reason)
}

func (d decisionActions) ApproveMerge(ctx context.Context, taskID, note string) (*model.Task, error) {
	return d.b.MarkDone(ctx, taskID, note)
}

func (d decisionActions) Unblock(ctx context.Context, taskID, to, note string) (*model.Task, error) {
	return d.b.Reopen(ctx, taskID, to, note)
}

func (decisionActions) Skip(context.Context, string, time.Time) error { return nil }

type chatDirectory struct{ b *board.Board }

func (d chatDirectory) Agent(ctx context.Context, id string) (*model.Agent, error) {
	return d.b.GetAgent(ctx, id)
}

func (chatDirectory) ByComponent(context.Context, string) ([]string, error) { return nil, nil }

func (d chatDirectory) ByBrief(ctx context.Context, id string) ([]string, error) {
	return d.b.ActiveAgentsOfBrief(ctx, id)
}

func (d chatDirectory) Leader(ctx context.Context) (string, error) {
	as, err := d.b.ListAgents(ctx, model.RoleLeader, model.AgentActive)
	if err != nil {
		return "", err
	}
	if len(as) == 0 {
		return "", model.NotFound("no active leader agent")
	}
	return as[0].ID, nil
}

type chatThreads struct{ b *board.Board }

func (t chatThreads) Append(ctx context.Context, agentID, convID string, m model.Message) (model.Message, error) {
	m.Body = fmt.Sprintf("[conv %s] %s", convID, m.Body)
	return t.b.AppendMessage(ctx, model.ThreadAgent, agentID, m)
}

func (t chatThreads) Messages(ctx context.Context, agentID string, since int64) ([]chat.Entry, error) {
	ms, err := t.b.OwnerThread(ctx, model.ThreadAgent, agentID, since, 1000)
	if err != nil {
		return nil, err
	}
	var out []chat.Entry
	for _, m := range ms {
		rest, ok := strings.CutPrefix(m.Body, "[conv ")
		if !ok {
			continue
		}
		id, body, ok := strings.Cut(rest, "] ")
		if !ok {
			continue
		}
		m.Body = body
		out = append(out, chat.Entry{Message: *m, ConvID: id})
	}
	return out, nil
}

type componentSource struct {
	b  *board.Board
	st store.Store
}

func (c componentSource) Brief(ctx context.Context, id string) (*model.Brief, error) {
	return c.b.GetBrief(ctx, id)
}

func (c componentSource) Tasks(ctx context.Context, briefID string) ([]components.Task, error) {
	ts, err := c.b.ListTasks(ctx, board.TaskQuery{BriefID: briefID, Limit: 100000})
	if err != nil {
		return nil, err
	}
	out := make([]components.Task, len(ts))
	for i, t := range ts {
		out[i] = components.Task{Task: t}
		if t.ComponentID != nil {
			out[i].ComponentID = *t.ComponentID
		}
	}
	return out, nil
}

func (c componentSource) Agents(ctx context.Context) ([]*model.Agent, error) {
	return c.b.ListAgents(ctx, "", "")
}

var errNoRunner = errors.New("hillclimb runner is not configured")

type noRunner struct{}

func (noRunner) Run(context.Context, string, string, string) (string, error) { return "", errNoRunner }
