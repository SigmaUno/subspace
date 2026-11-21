package openapi

import "github.com/SigmaUno/subspace/internal/about"

func init() {
	Register(
		get("/api/v1/about", "Version, uptime, database size and backup state", about.Info{}),
	)
}
