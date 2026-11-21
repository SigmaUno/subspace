package openapi

import "github.com/SigmaUno/subspace/internal/leakguard"

func init() {
	Register(
		post("/api/v1/token-leaks/{id}/reissue", "Re-issue a token that leaked: a fresh agent token or API token, shown once", Free{}, leakguard.Reissued{}),
	)
}
