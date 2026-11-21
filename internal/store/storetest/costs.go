package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func scenarioCosts(t *testing.T, s store.Store) {
	base := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return base.Add(d) }
	h := time.Hour

	write(t, s, func(tx store.Tx) error {
		for _, r := range []store.CostRecord{
			{Key: "c1", TS: at(0), BriefID: "B-1", TaskID: "T-1", ComponentID: "C-1", AgentID: "w1", Kind: "claude-code", Role: "worker",
				Provider: "anthropic", Model: "sonnet", TokensIn: 100, TokensOut: 50, CacheRead: 10, CacheWrite: 5, CostEUR: 1},
			{Key: "c2", TS: at(30 * time.Minute), BriefID: "B-1", TaskID: "T-1", ComponentID: "C-1", AgentID: "w1", Kind: "claude-code", Role: "worker",
				Provider: "anthropic", Model: "sonnet", TokensIn: 10, TokensOut: 5, CostEUR: 0.5},
			{Key: "c3", TS: at(2 * h), BriefID: "B-1", TaskID: "T-2", ComponentID: "C-2", AgentID: "r1", Kind: "codex", Role: "reviewer",
				Provider: "openai", Model: "gpt", TokensIn: 20, TokensOut: 10, CostEUR: 2},
			{Key: "c4", TS: at(3 * h), BriefID: "B-2", TaskID: "T-3", AgentID: "w2", Kind: "opencode", Role: "worker",
				Provider: "deepseek", Model: "chat", TokensIn: 1, TokensOut: 1, CostEUR: 0.25},
			{Key: "c4", TS: at(3 * h), BriefID: "B-2", TaskID: "T-3", AgentID: "w2", Kind: "opencode", Role: "worker",
				Provider: "deepseek", Model: "chat", TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
			{Key: "c5", TS: at(10 * h), BriefID: "B-1", TaskID: "T-1", ComponentID: "C-1", AgentID: "w1", Kind: "claude-code", Role: "worker",
				Provider: "anthropic", Model: "sonnet", TokensIn: 1000, CostEUR: 100},
		} {
			if err := tx.(store.CostsTx).PutCost(r); err != nil {
				return err
			}
		}
		return nil
	})

	since, until := base, at(4*h)
	for _, c := range []struct {
		name  string
		query store.CostQuery
		want  []store.CostRow
	}{
		{"brief", store.CostQuery{GroupBy: store.CostByBrief}, []store.CostRow{
			{Key: "B-1", Requests: 3, TokensIn: 130, TokensOut: 65, CacheRead: 10, CacheWrite: 5, CostEUR: 3.5},
			{Key: "B-2", Requests: 1, TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
		}},
		{"component", store.CostQuery{GroupBy: store.CostByComponent}, []store.CostRow{
			{Key: "", Requests: 1, TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
			{Key: "C-1", Requests: 2, TokensIn: 110, TokensOut: 55, CacheRead: 10, CacheWrite: 5, CostEUR: 1.5},
			{Key: "C-2", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
		}},
		{"agent in brief", store.CostQuery{GroupBy: store.CostByAgent, BriefID: "B-1"}, []store.CostRow{
			{Key: "r1", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
			{Key: "w1", Requests: 2, TokensIn: 110, TokensOut: 55, CacheRead: 10, CacheWrite: 5, CostEUR: 1.5},
		}},
		{"role", store.CostQuery{GroupBy: store.CostByRole}, []store.CostRow{
			{Key: "reviewer", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
			{Key: "worker", Requests: 3, TokensIn: 117, TokensOut: 58, CacheRead: 10, CacheWrite: 5, CostEUR: 2},
		}},
		{"provider split by kind", store.CostQuery{GroupBy: store.CostByProvider, SplitBy: store.CostByKind}, []store.CostRow{
			{Key: "anthropic", Sub: "claude-code", Requests: 2, TokensIn: 110, TokensOut: 55, CacheRead: 10, CacheWrite: 5, CostEUR: 1.5},
			{Key: "deepseek", Sub: "opencode", Requests: 1, TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
			{Key: "openai", Sub: "codex", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
		}},
		{"day split by model", store.CostQuery{GroupBy: store.CostByDay, SplitBy: store.CostByModel}, []store.CostRow{
			{Key: "2026-11-20", Sub: "sonnet", Requests: 2, TokensIn: 110, TokensOut: 55, CacheRead: 10, CacheWrite: 5, CostEUR: 1.5},
			{Key: "2026-11-21", Sub: "chat", Requests: 1, TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
			{Key: "2026-11-21", Sub: "gpt", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
		}},
		{"window", store.CostQuery{GroupBy: store.CostByTask, Since: at(h), Until: at(3 * h)}, []store.CostRow{
			{Key: "T-2", Requests: 1, TokensIn: 20, TokensOut: 10, CostEUR: 2},
		}},
		{"empty", store.CostQuery{GroupBy: store.CostByModel, BriefID: "B-9"}, nil},
	} {
		q := c.query
		if q.Since.IsZero() {
			q.Since = since
		}
		if q.Until.IsZero() {
			q.Until = until
		}
		var got []store.CostRow
		read(t, s, func(tx store.Tx) (err error) {
			got, err = tx.(store.CostsTx).Costs(q)
			return
		})
		if !reflect.DeepEqual(got, c.want) && len(got)+len(c.want) > 0 {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}

	var recs []store.CostRecord
	read(t, s, func(tx store.Tx) (err error) {
		recs, err = tx.(store.CostsTx).ListCosts(at(h), at(10*h))
		return
	})
	want := []store.CostRecord{
		{Key: "c3", TS: at(2 * h), BriefID: "B-1", TaskID: "T-2", ComponentID: "C-2", AgentID: "r1", Kind: "codex", Role: "reviewer",
			Provider: "openai", Model: "gpt", TokensIn: 20, TokensOut: 10, CostEUR: 2},
		{Key: "c4", TS: at(3 * h), BriefID: "B-2", TaskID: "T-3", AgentID: "w2", Kind: "opencode", Role: "worker",
			Provider: "deepseek", Model: "chat", TokensIn: 7, TokensOut: 3, CostEUR: 0.5},
	}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("list costs: got %+v want %+v", recs, want)
	}

	read(t, s, func(tx store.Tx) error {
		if _, err := tx.(store.CostsTx).Costs(store.CostQuery{GroupBy: "bogus", Since: since, Until: until}); err == nil {
			t.Fatal("unknown dimension accepted")
		}
		return nil
	})
}
