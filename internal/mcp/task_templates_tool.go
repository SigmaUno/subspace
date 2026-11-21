package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
)

func RegisterTaskTemplates(r *Registry, b *board.Board, lib *prompts.Library) {
	r.Register(&Tool{
		Name:        "create_task_from_template",
		Description: "Add a task to a brief by rendering a task template into its title, spec, acceptance criteria, role and budget. Templates: " + templateSummary(lib),
		Roles:       LeaderOnly,
		Schema: obj(map[string]any{
			"brief_id":    str("brief"),
			"template_id": str("task template id"),
			"vars": map[string]any{
				"type": "object", "additionalProperties": map[string]any{"type": "string"},
				"description": "value for every variable the template declares",
			},
			"repo":       str("owner/name of the repo"),
			"depends_on": strs("task ids"),
			"priority":   intg("higher runs first, default 0"),
		}, "brief_id", "template_id", "vars", "repo"),
		Handler: func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				BriefID    string            `json:"brief_id"`
				TemplateID string            `json:"template_id"`
				Vars       map[string]string `json:"vars"`
				Repo       string            `json:"repo"`
				DependsOn  []string          `json:"depends_on"`
				Priority   int               `json:"priority"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			if in.BriefID == "" || in.TemplateID == "" {
				return nil, model.BadRequest("brief_id and template_id are required")
			}
			rt, err := lib.RenderTask(in.TemplateID, in.Vars)
			if err != nil {
				return nil, err
			}
			t, err := b.CreateTask(ctx, c.Actor, board.TaskInput{
				BriefID: in.BriefID, Title: rt.Title, Spec: rt.Spec, AcceptanceCriteria: rt.AcceptanceCriteria,
				Repo: in.Repo, DependsOn: in.DependsOn, Role: rt.Role, Budget: rt.Budget, Priority: in.Priority,
			})
			return map[string]any{"task": t}, err
		},
	})
}

func templateSummary(lib *prompts.Library) string {
	ts, err := lib.TaskTemplates()
	if err != nil || len(ts) == 0 {
		return "none"
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("%s(%s)", t.ID, strings.Join(t.Variables, ", "))
	}
	return strings.Join(parts, "; ")
}
