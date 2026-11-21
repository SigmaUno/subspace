package storetest

import (
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func scenarioTimeseries(t *testing.T, s store.Store) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return base.Add(d) }
	m := time.Minute
	h := time.Hour

	audit := func(tx store.Tx, ts time.Time, agent, role, tool, task string, after map[string]any) {
		t.Helper()
		ev := &model.AuditEvent{TS: ts, AgentID: agent, Role: role, Tool: tool, After: after}
		if task != "" {
			ev.TaskID = ptr(task)
		}
		if err := tx.InsertAudit(ev); err != nil {
			t.Fatal(err)
		}
	}
	cost := func(c float64) map[string]any { return map[string]any{"progress_pct": float64(10), "cost_used": c} }
	status := func(s string) map[string]any { return map[string]any{"status": s} }

	write(t, s, func(tx store.Tx) error {
		for _, id := range []string{"B-1", "B-2"} {
			if err := tx.InsertBrief(testBrief(id, base)); err != nil {
				return err
			}
		}
		for _, f := range []struct {
			id, brief string
			created   time.Time
		}{
			{"T-0", "B-1", at(-h)}, {"T-1", "B-1", at(10 * m)}, {"T-2", "B-1", at(70 * m)}, {"T-3", "B-2", at(20 * m)},
		} {
			task := taskMin(f.id, f.brief)
			task.CreatedAt, task.UpdatedAt = f.created, f.created
			if err := tx.InsertTask(task); err != nil {
				return err
			}
		}
		audit(tx, at(-30*m), "a1", "worker", "update_status", "T-1", cost(0.5))
		audit(tx, at(5*m), "a1", "worker", "update_status", "T-1", cost(1))
		audit(tx, at(15*m), "a2", "reviewer", "review", "T-1", status("changes_requested"))
		audit(tx, at(20*m), "a2", "reviewer", "review", "T-3", status("approved"))
		audit(tx, at(25*m), "a3", "worker", "update_status", "T-3", cost(4))
		audit(tx, at(30*m), "owner", "owner", "mark_done", "T-3", status("done"))
		audit(tx, at(-10*m), "owner", "owner", "mark_done", "T-0", status("done"))
		audit(tx, at(80*m), "a1", "worker", "update_status", "T-1", cost(2.5))
		audit(tx, at(85*m), "a1", "worker", "update_status", "T-1", cost(2.5))
		audit(tx, at(90*m), "owner", "owner", "mark_done", "T-1", status("done"))
		audit(tx, at(130*m), "a2", "reviewer", "review", "T-1", status("needs_owner"))
		audit(tx, at(150*m), "owner", "owner", "mark_done", "T-2", status("done"))
		audit(tx, at(160*m), "a4", "leader", "ask_owner", "", map[string]any{"question_id": "Q-1"})
		audit(tx, at(4*h), "owner", "owner", "mark_done", "T-2", status("done"))
		for _, u := range []store.UsageRecord{
			{Key: "u1", TS: at(m), BriefID: "B-1", AgentID: "a1", TokensIn: 100, TokensOut: 50},
			{Key: "u2", TS: at(2 * m), BriefID: "B-2", AgentID: "a3", TokensIn: 1, TokensOut: 1},
			{Key: "u2", TS: at(2 * m), BriefID: "B-2", AgentID: "a3", TokensIn: 10, TokensOut: 5},
			{Key: "u3", TS: at(3 * h), BriefID: "B-1", AgentID: "a1", TokensIn: 7, TokensOut: 3},
			{Key: "u4", TS: at(4 * h), BriefID: "B-1", AgentID: "a1", TokensIn: 1000, TokensOut: 1000},
		} {
			if err := tx.(store.TimeseriesTx).PutUsage(u); err != nil {
				return err
			}
		}
		return nil
	})

	for _, c := range []struct {
		metric, brief string
		want          [4]float64
	}{
		{store.MetricTasksCreated, "", [4]float64{2, 1, 0, 0}},
		{store.MetricTasksCreated, "B-1", [4]float64{1, 1, 0, 0}},
		{store.MetricTasksDone, "", [4]float64{1, 1, 1, 0}},
		{store.MetricTasksDone, "B-1", [4]float64{0, 1, 1, 0}},
		{store.MetricTasksDone, "B-2", [4]float64{1, 0, 0, 0}},
		{store.MetricReviewRounds, "", [4]float64{1, 0, 1, 0}},
		{store.MetricReviewRounds, "B-2", [4]float64{0, 0, 0, 0}},
		{store.MetricCostEUR, "", [4]float64{4.5, 1.5, 0, 0}},
		{store.MetricCostEUR, "B-1", [4]float64{0.5, 1.5, 0, 0}},
		{store.MetricCostEUR, "B-2", [4]float64{4, 0, 0, 0}},
		{store.MetricActiveAgents, "", [4]float64{3, 1, 2, 0}},
		{store.MetricActiveAgents, "B-1", [4]float64{2, 1, 1, 0}},
		{store.MetricActiveAgents, "B-2", [4]float64{2, 0, 0, 0}},
		{store.MetricTokens, "", [4]float64{165, 0, 0, 10}},
		{store.MetricTokens, "B-1", [4]float64{150, 0, 0, 10}},
		{store.MetricTokens, "B-2", [4]float64{15, 0, 0, 0}},
	} {
		var pts []store.TimeseriesPoint
		read(t, s, func(tx store.Tx) (err error) {
			pts, err = tx.(store.TimeseriesTx).Timeseries(store.TimeseriesQuery{
				Metric: c.metric, Since: base, Until: at(4 * h), Bucket: h, BriefID: c.brief,
			})
			return
		})
		var got [4]float64
		for _, p := range pts {
			i := int(p.At.Sub(base) / h)
			if i < 0 || i >= len(got) || !p.At.Equal(at(time.Duration(i)*h)) {
				t.Fatalf("%s/%s: unexpected bucket %v", c.metric, c.brief, p.At)
			}
			got[i] = p.Value
		}
		if got != c.want {
			t.Fatalf("%s/%s: got %v want %v", c.metric, c.brief, got, c.want)
		}
	}

	var fine []store.TimeseriesPoint
	read(t, s, func(tx store.Tx) (err error) {
		fine, err = tx.(store.TimeseriesTx).Timeseries(store.TimeseriesQuery{
			Metric: store.MetricTokens, Since: base, Until: at(h), Bucket: 5 * m,
		})
		return
	})
	if len(fine) != 1 || !fine[0].At.Equal(base) || fine[0].Value != 165 {
		t.Fatalf("5m buckets: %+v", fine)
	}
}
