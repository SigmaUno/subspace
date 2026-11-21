package client_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func TestOwnerPagesFollowCursors(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx, o := e.ctx, e.owner

	for i := range 25 {
		must(o.RegisterAgent(ctx, board.AgentInput{ID: fmt.Sprintf("bee-%03d", i), Role: model.RoleWorker, Kind: model.KindCodex, Model: "m"}))
	}

	var ids []string
	cursor := ""
	pages := 0
	for {
		p := must(o.AgentsPage(ctx, "", "", cursor, 10))
		pages++
		for _, a := range p.Items {
			ids = append(ids, a.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	all := must(o.ListAgents(ctx, "", ""))
	want := make([]string, len(all))
	for i, a := range all {
		want[i] = a.ID
	}
	if pages != 3 || !slices.Equal(ids, want) || len(ids) != 25 {
		t.Fatalf("pages=%d ids=%v", pages, ids)
	}

	var seen []int64
	cursor = ""
	for {
		p := must(o.AuditPage(ctx, store.AuditFilter{Limit: 7}, cursor))
		for _, ev := range p.Items {
			seen = append(seen, ev.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) < 25 || !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != len(seen) {
		t.Fatalf("audit ids %v", seen)
	}
}
