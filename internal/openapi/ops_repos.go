package openapi

import (
	"github.com/SigmaUno/subspace/internal/repos"
)

type RepoInput struct {
	Name              string   `json:"name"`
	Host              string   `json:"host"`
	BaseURL           string   `json:"base_url,omitempty"`
	DefaultBranch     string   `json:"default_branch,omitempty"`
	ProtectedBranches []string `json:"protected_branches,omitempty"`
	MergeTestCommand  []string `json:"merge_test_command,omitempty"`
	MergeWorkDir      string   `json:"merge_work_dir,omitempty"`
	TokenEnv          string   `json:"token_env,omitempty"`
	AgentTokenEnv     string   `json:"agent_token_env,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
}

func init() {
	Register(
		get("/api/v1/repos", "List registered repositories", list[repos.Repo]("repos")),
		post("/api/v1/repos", "Register a repository", RepoInput{}, repos.Repo{}).S(created),
		get("/api/v1/repos/{owner}/{name}", "Get a registered repository", repos.Repo{}),
		put("/api/v1/repos/{owner}/{name}", "Replace a registered repository", RepoInput{}, repos.Repo{}),
		del("/api/v1/repos/{owner}/{name}", "Remove a repository from the registry", nil).S(gone),
		get("/api/v1/repos/{owner}/{name}/health", "Read-only reachability, token and default branch check of a repository", repos.Health{}),
	)
}
