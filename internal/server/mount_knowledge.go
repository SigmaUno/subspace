package server

import (
	"os"
	"strings"

	"github.com/SigmaUno/subspace/internal/knowledge"
	"github.com/SigmaUno/subspace/internal/mcp"
)

func init() {
	registerMount(func(d Deps) {
		moderation := !strings.EqualFold(strings.TrimSpace(os.Getenv("SUBSPACE_KNOWLEDGE_MODERATION")), "off")
		svc := knowledge.New(d.Store, knowledge.Options{Now: d.Cfg.Now, Moderation: moderation})
		h := svc.Handler(d.Log)
		d.API.Mount(d.Mux, "/api/v1/knowledge", h)
		d.API.Mount(d.Mux, "/api/v1/knowledge/", h)
		mcp.BindKnowledge(d.Board, svc)
	})
}
