package server

import "github.com/SigmaUno/subspace/internal/prompts"

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/task-templates", prompts.New(d.Cfg.PromptsDir).TaskHandler())
	})
}
