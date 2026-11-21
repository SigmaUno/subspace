package insights_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/insights"
	"github.com/SigmaUno/subspace/internal/testdb"
)

func TestRecorderRetentionIsBounded(t *testing.T) {
	st := testdb.Open(t, testdb.Path(t, "t.db")).(insights.Store)
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	rec := insights.NewRecorder(st, insights.RecorderOptions{
		Now: func() time.Time { return clock }, Retention: time.Hour, MaxRows: 5,
	})
	ctx := context.Background()

	clock = clock.Add(-2 * time.Hour)
	for range 3 {
		rec.Observe("a", "claim_task", "not_assigned", "old", []byte(`{"task_id":"T-1"}`))
	}
	clock = clock.Add(2 * time.Hour)
	for range 8 {
		rec.Observe("a", "claim_task", "not_assigned", "new", []byte(`{"task_id":"T-2"}`))
	}
	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListToolErrors(ctx, clock.Add(-24*time.Hour), 100)
	if err != nil || len(all) != 11 || all[0].TaskID != "T-1" || all[10].Fingerprint != insights.Fingerprint("claim_task", "not_assigned", "new") {
		t.Fatalf("stored: %d %v", len(all), err)
	}

	if err := rec.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := st.ListToolErrors(ctx, clock.Add(-24*time.Hour), 100)
	if err != nil || len(left) != 5 {
		t.Fatalf("after prune: %d %v", len(left), err)
	}
	for _, e := range left {
		if e.TaskID != "T-2" || e.ID <= all[5].ID {
			t.Fatalf("kept the wrong rows: %+v", left)
		}
	}
}

func TestRecorderDropsOversizedTaskIDs(t *testing.T) {
	st := testdb.Open(t, testdb.Path(t, "t.db")).(insights.Store)
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	rec := insights.NewRecorder(st, insights.RecorderOptions{Now: func() time.Time { return clock }})
	ctx := context.Background()

	huge := strings.Repeat("A", 512<<10)
	rec.Observe("a", "claim_task", "not_found", "task not found", []byte(`{"task_id":"`+huge+`"}`))
	rec.Observe("a", "claim_task", "not_found", "task not found", []byte(`{"task_id":"T-7"}`))
	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListToolErrors(ctx, clock.Add(-time.Hour), 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("stored: %d %v", len(all), err)
	}
	if len(all[0].TaskID) > 64 || all[1].TaskID != "T-7" {
		t.Fatalf("task ids: %d bytes, %q", len(all[0].TaskID), all[1].TaskID)
	}
}
