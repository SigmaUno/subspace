package artifacts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

func (s *Service) Tools() []*mcp.Tool {
	scope := map[string]any{"type": "string", "description": "agent:<id>, task:<id> or project:<id>"}
	id := map[string]any{"type": "string", "description": "artifact id, e.g. A-1"}
	return []*mcp.Tool{
		{
			Name:        "list_artifacts",
			Description: "List artifacts visible to you: your own agent scope, your assigned tasks and their projects. Optionally filter by scope.",
			Roles:       mcp.AllRoles,
			ReadOnly:    true,
			Handler:     s.listTool,
			Schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"scope": scope, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}},
				"additionalProperties": false,
			},
		},
		{
			Name:        "get_artifact",
			Description: "Fetch an artifact. Small files are returned base64-encoded in content_base64; larger ones as a short-lived download url.",
			Roles:       mcp.AllRoles,
			ReadOnly:    true,
			Handler:     s.getTool,
			Schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"id": id},
				"required":             []string{"id"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "put_artifact",
			Description: "Store a file as an artifact in a scope you can access. Content is base64-encoded.",
			Roles:       mcp.AllRoles,
			Handler:     s.putTool,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope":          scope,
					"name":           map[string]any{"type": "string", "maxLength": maxNameLen},
					"content_base64": map[string]any{"type": "string"},
					"content_type":   map[string]any{"type": "string"},
				},
				"required":             []string{"scope", "name", "content_base64"},
				"additionalProperties": false,
			},
		},
	}
}

func (s *Service) listTool(ctx context.Context, c *mcp.Call) (any, error) {
	var in struct {
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := decode(c.Args, &in); err != nil {
		return nil, err
	}
	out, err := s.List(ctx, c.Actor, in.Scope, in.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifacts": out}, nil
}

func (s *Service) getTool(ctx context.Context, c *mcp.Call) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(c.Args, &in); err != nil {
		return nil, err
	}
	a, err := s.Get(ctx, c.Actor, in.ID)
	if err != nil {
		return nil, err
	}
	if a.Size > s.cfg.InlineMax {
		u, exp, err := s.SignedURL(a)
		if err != nil {
			return nil, err
		}
		return map[string]any{"artifact": a, "url": u, "url_expires_at": exp}, nil
	}
	f, err := s.openBlob(a)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, a.Size+1))
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": a, "content_base64": base64.StdEncoding.EncodeToString(raw)}, nil
}

func (s *Service) putTool(ctx context.Context, c *mcp.Call) (any, error) {
	var in struct {
		Scope         string `json:"scope"`
		Name          string `json:"name"`
		ContentBase64 string `json:"content_base64"`
		ContentType   string `json:"content_type"`
	}
	if err := decode(c.Args, &in); err != nil {
		return nil, err
	}
	a, err := s.Put(ctx, c.Actor, Upload{
		Scope:       in.Scope,
		Name:        in.Name,
		ContentType: in.ContentType,
		Body:        base64.NewDecoder(base64.StdEncoding, strings.NewReader(in.ContentBase64)),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": a}, nil
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return model.BadRequest("invalid arguments: %v", err)
	}
	return nil
}
