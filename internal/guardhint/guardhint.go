package guardhint

import (
	"fmt"
	"strings"
)

type Guard int

const (
	OpenAPIGolden Guard = iota
	ToolLists
	ActivityTemplates
	CLIParity
	TSTypes
	SDK
	SecretScan
)

type Step struct {
	Guard   Guard
	Name    string
	Command string
	Manual  string
}

var steps = []Step{
	{Guard: OpenAPIGolden, Name: "OpenAPI goldens", Command: "go test ./internal/openapi -run Golden -update", Manual: "register new operations in a new file under internal/openapi (ops_<feature>.go)"},
	{Guard: ToolLists, Name: "tool lists in docs/api.md", Command: "go run ./cmd/gen-api-docs | go run ./cmd/gen-sync", Manual: "document new routes and tools in docs/api.md"},
	{Guard: ActivityTemplates, Name: "activity templates", Manual: "add a template for each audited tool to internal/activity/rules.go"},
	{Guard: CLIParity, Name: "subspacectl parity", Manual: "add the subspacectl command and a covered entry, or an exemption with a reason, in cmd/subspacectl/parity_test.go"},
	{Guard: TSTypes, Name: "web TypeScript types", Command: "make gen-ts", Manual: "describe new shapes in cmd/gen-ts/registry.go"},
	{Guard: SDK, Name: "TypeScript SDK", Command: "cd sdk/ts && pnpm install --frozen-lockfile && pnpm gen"},
	{Guard: SecretScan, Name: "secretscan allowlist", Manual: "remove the secret, or add an entry with a reason to deploy/secretscan.allow"},
}

type Reporter interface {
	Helper()
	Cleanup(func())
	Failed() bool
	Log(args ...any)
}

func Steps() []Step {
	return append([]Step(nil), steps...)
}

func Of(g Guard) Step {
	for _, s := range steps {
		if s.Guard == g {
			return s
		}
	}
	return Step{}
}

func Checklist(failed Guard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "guard failed: %s\n", Of(failed).Name)
	b.WriteString("run `make sync` to regenerate everything derivable, then finish the manual steps:\n")
	for _, s := range steps {
		mark := " "
		if s.Guard == failed {
			mark = ">"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, s.Name)
		if s.Command != "" {
			fmt.Fprintf(&b, "    run:    %s\n", s.Command)
		}
		if s.Manual != "" {
			fmt.Fprintf(&b, "    manual: %s\n", s.Manual)
		}
	}
	return b.String()
}

func Manual() string {
	var b strings.Builder
	b.WriteString("still manual:\n")
	for _, s := range steps {
		if s.Manual != "" {
			fmt.Fprintf(&b, "  - %s: %s\n", s.Name, s.Manual)
		}
	}
	return b.String()
}

func Watch(t Reporter, g Guard) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("\n" + Checklist(g))
		}
	})
}
