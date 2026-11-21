package openapi

import (
	"github.com/SigmaUno/subspace/internal/planlint"
	"github.com/SigmaUno/subspace/internal/playbooks"
)

type PlaybookInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	planlint.Rules
}

type PlaybookClone struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func init() {
	Register(
		get("/api/v1/playbooks", "List playbooks", list[playbooks.Playbook]("playbooks")),
		post("/api/v1/playbooks", "Create a playbook", PlaybookInput{}, playbooks.Playbook{}).S(created),
		get("/api/v1/playbooks/{name}", "Get a playbook", playbooks.Playbook{}),
		put("/api/v1/playbooks/{name}", "Replace a playbook; built-in playbooks can be edited too", PlaybookInput{}, playbooks.Playbook{}),
		del("/api/v1/playbooks/{name}", "Delete a custom playbook that no brief uses; built-in playbooks cannot be deleted", nil).S(gone),
		post("/api/v1/playbooks/{name}/clone", "Copy a playbook under a new name", PlaybookClone{}, playbooks.Playbook{}).S(created),
	)
}
