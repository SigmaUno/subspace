package openapi

import "github.com/SigmaUno/subspace/internal/policy"

type PolicyFacts struct {
	Facts   []policy.FactInfo `json:"facts"`
	Actions []string          `json:"actions"`
	Effects []string          `json:"effects"`
}

type PolicyLint struct {
	OK       bool             `json:"ok"`
	Findings []policy.Finding `json:"findings"`
}

type PolicyHoldDecision struct {
	Note string `json:"note,omitempty"`
}

func init() {
	Register(
		get("/api/v1/policies", "List owner policies with their lint warnings", list[policy.Policy]("policies")),
		post("/api/v1/policies", "Create a policy; documents with lint errors are rejected", policy.Document{}, policy.Policy{}).S(created),
		get("/api/v1/policies/facts", "Facts, actions and effects available to policy rules", PolicyFacts{}),
		post("/api/v1/policies/lint", "Lint a policy document without saving it", policy.Document{}, PolicyLint{}),
		post("/api/v1/policies/evaluate", "Dry-run the stored policies, or a draft, against a sample action; nothing is recorded", policy.DryRunInput{}, policy.DryRunResult{}),
		get("/api/v1/policies/decisions", "Policy decisions taken on real actions, newest first", list[policy.Decision]("decisions")).Q("outcome action task_id before:integer limit:integer"),
		get("/api/v1/policies/{id}", "Get a policy", policy.Policy{}),
		put("/api/v1/policies/{id}", "Replace a policy; the version increases by one", policy.Document{}, policy.Policy{}),
		del("/api/v1/policies/{id}", "Delete a policy and its versions", nil).S(gone),
		get("/api/v1/policies/{id}/versions", "Stored versions of a policy, newest first", list[policy.Version]("versions")),
		get("/api/v1/policy-holds", "Actions held for owner approval by a policy, newest first", list[policy.Hold]("holds")).Q("status limit:integer"),
		get("/api/v1/policy-holds/{id}", "Get a policy hold", policy.Hold{}),
		post("/api/v1/policy-holds/{id}/approve", "Approve a held action; assign and approve_plan are carried out at once, other actions pass on the next try", PolicyHoldDecision{}, policy.Hold{}),
		post("/api/v1/policy-holds/{id}/reject", "Reject a held action", PolicyHoldDecision{}, policy.Hold{}),
	)
}
