package mcp

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

var artifactHandlers sync.Map

func BindArtifacts(b *board.Board, tools []*Tool) (unbind func()) {
	h := make(map[string]Handler, len(tools))
	for _, t := range tools {
		h[t.Name] = t.Handler
	}
	artifactHandlers.Store(b, h)
	return func() { artifactHandlers.Delete(b) }
}

func RegisterArtifacts(r *Registry, b *board.Board) {
	scope := str("agent:<id>, task:<id> or project:<id>")
	delegate := func(name string) Handler {
		return func(ctx context.Context, c *Call) (any, error) {
			h, _ := artifactHandlers.Load(b)
			handlers, _ := h.(map[string]Handler)
			fn := handlers[name]
			if fn == nil {
				return nil, model.Errf("internal", "artifacts are not available")
			}
			return fn(ctx, c)
		}
	}
	r.Register(&Tool{
		Name:        "list_artifacts",
		Description: "List artifacts visible to you: your own agent scope, your assigned tasks and their projects. Optionally filter by scope.",
		Roles:       AllRoles,
		ReadOnly:    true,
		Schema:      obj(map[string]any{"scope": scope, "limit": intg("maximum number of artifacts, 1-1000")}),
		Handler:     delegate("list_artifacts"),
	})
	r.Register(&Tool{
		Name:        "get_artifact",
		Description: "Fetch an artifact. Small files are returned base64-encoded in content_base64; larger ones as a short-lived download url.",
		Roles:       AllRoles,
		ReadOnly:    true,
		Schema:      obj(map[string]any{"id": str("artifact id, e.g. A-1")}, "id"),
		Handler:     delegate("get_artifact"),
	})
	r.Register(&Tool{
		Name:        "put_artifact",
		Description: "Store a file as an artifact in a scope you can access. Content is base64-encoded.",
		Roles:       AllRoles,
		Schema: obj(map[string]any{
			"scope":          scope,
			"name":           str("file name"),
			"content_base64": str("file content, base64-encoded"),
			"content_type":   str("media type; sniffed from the content when omitted"),
		}, "scope", "name", "content_base64"),
		Handler: delegate("put_artifact"),
	})
}
