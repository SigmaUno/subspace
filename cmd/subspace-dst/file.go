package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/dst"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxTraceLines = 200

type filer struct {
	mcp   *client.MCP
	brief string
	repo  string
	filed map[string]bool
}

func newFiler(ctx context.Context, url, token, briefID, repo string) (*filer, error) {
	url = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(url), "/"), "/mcp")
	if url == "" || strings.TrimSpace(token) == "" {
		return nil, errors.New("-file-brief needs -url (or SUBSPACE_URL) and SUBSPACE_TOKEN_FILE or SUBSPACE_TOKEN")
	}
	m := client.NewMCP(url+"/mcp", strings.TrimSpace(token), client.WithClientName("subspace-dst"))
	if err := m.Initialize(ctx); err != nil {
		return nil, err
	}
	br, err := m.GetBrief(ctx, briefID)
	if err != nil {
		return nil, err
	}
	if repo == "" && len(br.Repos) > 0 {
		repo = br.Repos[0]
	}
	if repo == "" {
		return nil, fmt.Errorf("brief %s has no repo, pass -file-repo", briefID)
	}
	cols, err := m.Board(ctx, briefID)
	if err != nil {
		return nil, err
	}
	f := &filer{mcp: m, brief: briefID, repo: repo, filed: map[string]bool{}}
	for _, ts := range cols {
		for _, t := range ts {
			f.filed[t.Title] = true
		}
	}
	return f, nil
}

func findingTitle(mode string, seed int64, inv string) string {
	return fmt.Sprintf("DST %s seed %d violates %s", mode, seed, inv)
}

func (f *filer) file(ctx context.Context, mode string, steps int, o *dst.Outcome) (*model.Task, error) {
	title := findingTitle(mode, o.Seed, o.Violation.Invariant)
	if f.filed[title] {
		return nil, nil
	}
	replay := fmt.Sprintf("go run ./cmd/subspace-dst -mode %s -seeds 1 -start %d -steps %d", mode, o.Seed, steps)
	var trace strings.Builder
	o.WriteTrace(&trace)
	lines := strings.Split(strings.TrimRight(trace.String(), "\n"), "\n")
	if len(lines) > maxTraceLines {
		lines = append(lines[:maxTraceLines/2], append([]string{"..."}, lines[len(lines)-maxTraceLines/2:]...)...)
	}
	spec := fmt.Sprintf("The deterministic simulation found an invariant violation.\n\nViolation: %s\n\nReplay:\n\n    %s\n\nMinimal trace (%d steps):\n\n```\n%s\n```\n",
		o.Violation, replay, len(o.Steps), strings.Join(lines, "\n"))
	t, err := f.mcp.CreateTask(ctx, board.TaskInput{
		BriefID: f.brief,
		Title:   title,
		Spec:    spec,
		Repo:    f.repo,
		AcceptanceCriteria: []string{
			"Root cause of the violation is fixed in the system under test, not in the checker",
			fmt.Sprintf("`%s` passes", replay),
			fmt.Sprintf("`%d %d %s` is kept in testdata/dst/regressions.txt so TestRegressions replays it", o.Seed, steps, mode),
		},
	})
	if err != nil {
		return nil, err
	}
	f.filed[title] = true
	return t, nil
}
