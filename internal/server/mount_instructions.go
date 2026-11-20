package server

import (
	"github.com/SigmaUno/subspace/internal/instructions"
)

func init() {
	registerMount(func(d Deps) {
		svc := instructions.New(d.Store, instructions.StoreAuditor{Store: d.Store}, nil)
		d.API.Mount(d.Mux, "/api/v1/instructions/", instructions.Handler(svc, d.Log))
	})
}
