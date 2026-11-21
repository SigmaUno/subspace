package schedule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
)

type BoardExecutor struct {
	Board *board.Board
	Plan  func(ctx context.Context, briefID string, specs []components.Spec) ([]components.Component, error)
	Tasks *prompts.Library
}

func (e *BoardExecutor) Validate(ctx context.Context, s *Schedule) error {
	if s.Action == ActionCreateBrief {
		_, err := e.parse(s)
		return err
	}
	br, err := e.Board.GetBrief(ctx, s.BriefID)
	if err != nil {
		return err
	}
	for i, t := range s.Tasks {
		if len(br.Repos) > 0 && !slices.Contains(br.Repos, t.Repo) {
			return model.BadRequest("tasks[%d]: repo %q is not part of brief %s", i, t.Repo, br.ID)
		}
		if _, err := e.Tasks.RenderTask(t.TemplateID, t.Vars); err != nil {
			return err
		}
	}
	return nil
}

func (e *BoardExecutor) parse(s *Schedule) (*briefs.Doc, error) {
	src := s.Brief.Markdown
	if s.Brief.TemplateID != "" {
		var err error
		if src, err = briefs.Render(s.Brief.TemplateID, s.Brief.Vars); err != nil {
			return nil, err
		}
	}
	if len(src) > briefs.MaxDocumentBytes {
		return nil, model.BadRequest("brief document is larger than %d bytes", briefs.MaxDocumentBytes)
	}
	doc, err := briefs.Parse([]byte(src))
	var pe *briefs.ParseError
	if errors.As(err, &pe) {
		return nil, model.BadRequest("brief document: %s", pe.Error())
	}
	return doc, err
}

func (e *BoardExecutor) CreateBrief(ctx context.Context, s *Schedule, at time.Time) (string, error) {
	doc, err := e.parse(s)
	if err != nil {
		return "", err
	}
	br, err := e.Board.CreateBrief(ctx, doc.BriefInput())
	if err != nil {
		return "", err
	}
	if len(doc.Components) > 0 {
		if _, err := e.Plan(ctx, br.ID, doc.Components); err != nil {
			return br.ID, err
		}
	}
	return br.ID, e.post(ctx, br.ID, fmt.Sprintf("Created by schedule %s (%s) for %s.", s.ID, s.Name, at.UTC().Format(time.RFC3339)))
}

func (e *BoardExecutor) AddTasks(ctx context.Context, s *Schedule, at time.Time) ([]string, error) {
	var ids []string
	for _, t := range s.Tasks {
		rt, err := e.Tasks.RenderTask(t.TemplateID, t.Vars)
		if err != nil {
			return ids, err
		}
		task, err := e.Board.CreateTask(ctx, system, board.TaskInput{
			BriefID: s.BriefID, Title: rt.Title, Spec: rt.Spec, AcceptanceCriteria: rt.AcceptanceCriteria,
			Repo: t.Repo, Role: rt.Role, Budget: rt.Budget, Priority: t.Priority,
		})
		if err != nil {
			return ids, err
		}
		ids = append(ids, task.ID)
	}
	return ids, e.post(ctx, s.BriefID, fmt.Sprintf("Schedule %s (%s) added %s for %s.", s.ID, s.Name, strings.Join(ids, ", "), at.UTC().Format(time.RFC3339)))
}

func (e *BoardExecutor) post(ctx context.Context, briefID, body string) error {
	_, err := e.Board.AppendMessage(ctx, model.ThreadBrief, briefID, model.Message{
		AuthorID: system.ID, AuthorRole: system.Role, Kind: model.MsgSystem, Body: body,
	})
	return err
}
