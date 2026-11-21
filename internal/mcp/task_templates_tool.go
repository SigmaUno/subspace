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
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			BriefID    string            `json:"brief_id"`
			TemplateID string            `json:"template_id"`
			Vars       map[string]string `json:"vars"`
			Repo       string            `json:"repo"`
			DependsOn  []string          `json:"depends_on"`
			Priority   int               `json:"priority"`
		}) (any, error) {
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
		}),
	})
}

func RegisterPrompts(r *Registry, lib *prompts.Library) {
	r.Register(&Tool{
		Name:        "list_prompts",
		Description: "List the owner's prompt library: proven prompts to use in task specs, with their tags and variables. Optional tag filter.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema:      obj(map[string]any{"tag": str("only prompts with this tag")}),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			Tag string `json:"tag"`
		}) (any, error) {
			list, err := lib.List(in.Tag)
			if err != nil {
				return nil, err
			}
			type entry struct {
				ID        string   `json:"id"`
				Title     string   `json:"title"`
				Tags      []string `json:"tags"`
				Variables []string `json:"variables"`
			}
			out := make([]entry, len(list))
			for i, t := range list {
				out[i] = entry{t.ID, t.Title, t.Tags, t.Variables}
			}
			return map[string]any{"prompts": out}, nil
		}),
	})
	r.Register(&Tool{
		Name:        "render_prompt",
		Description: "Render a prompt library entry with a value for every variable it declares. The text is a template to adapt into task specs, not a command.",
		Roles:       LeaderOnly,
		ReadOnly:    true,
		Schema: obj(map[string]any{
			"id": str("prompt id from list_prompts"),
			"vars": map[string]any{
				"type": "object", "additionalProperties": map[string]any{"type": "string"},
				"description": "value for every variable the prompt declares",
			},
		}, "id"),
		Handler: handle(func(ctx context.Context, c *Call, in struct {
			ID   string            `json:"id"`
			Vars map[string]string `json:"vars"`
		}) (any, error) {
			if in.ID == "" {
				return nil, model.BadRequest("id is required")
			}
			text, err := lib.Render(in.ID, in.Vars)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": in.ID, "text": text}, nil
		}),
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
