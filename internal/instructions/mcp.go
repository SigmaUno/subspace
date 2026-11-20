package instructions

import (
	"context"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/model"
)

type ProjectResolver interface {
	ProjectOf(ctx context.Context, agentID string) (string, error)
}

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	Handler     func(ctx context.Context, actor model.Actor, args json.RawMessage) (any, error)
}

type Tools struct {
	svc      *Service
	projects ProjectResolver
}

func NewTools(svc *Service, projects ProjectResolver) *Tools {
	return &Tools{svc: svc, projects: projects}
}

func (t *Tools) GetInstructions() Tool {
	return Tool{
		Name:        "get_instructions",
		Description: "Return the effective instructions (rules, style, templates) for your project. Call this first.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		ReadOnly:    true,
		Handler: func(ctx context.Context, actor model.Actor, _ json.RawMessage) (any, error) {
			project, err := t.projects.ProjectOf(ctx, actor.ID)
			if err != nil {
				return nil, err
			}
			e, err := t.svc.Effective(ctx, project)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"instructions": e.Text,
				"version":      e.Version,
				"project":      project,
				"role":         actor.Role,
				"agent_id":     actor.ID,
			}, nil
		},
	}
}

func (t *Tools) InstructionsVersion(ctx context.Context, actor model.Actor) (string, error) {
	project, err := t.projects.ProjectOf(ctx, actor.ID)
	if err != nil {
		return "", err
	}
	return t.svc.Version(ctx, project)
}
