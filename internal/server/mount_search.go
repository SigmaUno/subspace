package server

import "github.com/SigmaUno/subspace/internal/search"

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "/api/v1/search/", search.New(d.Store, d.Log).Handler())
	})
}
