package alerts_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/backup/offsite"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func str(s string) *string { return &s }

func keys(fs []alerts.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Key)
	}
	slices.Sort(out)
	return out
}

func ready(id string, since time.Duration) *model.Task {
	return &model.Task{ID: id, Status: model.StatusReady, UpdatedAt: t0.Add(-since)}
}

func worker(id string, busy bool) *model.Agent {
	a := &model.Agent{ID: id, Role: model.RoleWorker, Status: model.AgentActive}
	if busy {
		a.CurrentTaskID = str("T-1")
	}
	return a
}

func TestRules(t *testing.T) {
	th := alerts.Defaults()
	cases := []struct {
		name string
		snap alerts.Snapshot
		want []string
	}{
		{"quiet", alerts.Snapshot{Now: t0}, nil},
		{"stalled without agents", alerts.Snapshot{Now: t0, Tasks: []*model.Task{ready("T-1", 11*time.Minute)}}, []string{"swarm_stalled"}},
		{"ready task still fresh", alerts.Snapshot{Now: t0, Tasks: []*model.Task{ready("T-1", 9*time.Minute)}}, nil},
		{"stalled with idle agent", alerts.Snapshot{Now: t0, Tasks: []*model.Task{ready("T-1", time.Hour)}, Agents: []*model.Agent{worker("w", false)}}, []string{"swarm_stalled"}},
		{"all agents busy", alerts.Snapshot{Now: t0, Tasks: []*model.Task{ready("T-1", time.Hour)}, Agents: []*model.Agent{worker("w", true)}}, nil},
		{"recent assignment", alerts.Snapshot{Now: t0, LastAssignment: t0.Add(-time.Minute), Tasks: []*model.Task{ready("T-1", time.Hour)}}, nil},
		{"paused swarm", alerts.Snapshot{Now: t0, Paused: true, Tasks: []*model.Task{ready("T-1", time.Hour)}}, nil},
		{"no progress", alerts.Snapshot{Now: t0, LastTaskChange: t0.Add(-31 * time.Minute), Tasks: []*model.Task{{ID: "T-1", Status: model.StatusInProgress}}}, []string{"no_progress"}},
		{"no progress without work", alerts.Snapshot{Now: t0, LastTaskChange: t0.Add(-time.Hour)}, nil},
		{"review stuck", alerts.Snapshot{Now: t0, Tasks: []*model.Task{{ID: "T-1", Status: model.StatusSubmitted, UpdatedAt: t0.Add(-time.Hour)}}}, []string{"review_stuck"}},
		{"review has reviewer", alerts.Snapshot{Now: t0, Tasks: []*model.Task{{ID: "T-1", Status: model.StatusSubmitted, Reviewer: str("r"), UpdatedAt: t0.Add(-time.Hour)}}}, nil},
		{"backups off", alerts.Snapshot{Now: t0, LastBackup: t0.Add(-1000 * time.Hour)}, nil},
		{"backup old and failing", alerts.Snapshot{Now: t0, BackupsEnabled: true, BackupFailing: true, LastBackup: t0.Add(-49 * time.Hour)}, []string{"backup_failing", "backup_old"}},
		{"offsite off", alerts.Snapshot{Now: t0, LastOffsite: t0.Add(-1000 * time.Hour)}, nil},
		{"offsite fresh", alerts.Snapshot{Now: t0, OffsiteEnabled: true, LastOffsite: t0.Add(-47 * time.Hour)}, nil},
		{"offsite stale", alerts.Snapshot{Now: t0, OffsiteEnabled: true, LastOffsite: t0.Add(-49 * time.Hour), OffsiteError: "upload: boom"}, []string{"offsite_stale"}},
		{"disk low", alerts.Snapshot{Now: t0, DiskKnown: true, DiskFree: 1 << 20}, []string{"disk_low"}},
		{"disk unknown", alerts.Snapshot{Now: t0}, nil},
		{"db and wal large", alerts.Snapshot{Now: t0, DBBytes: 9 << 30, WALBytes: 600 << 20}, []string{"db_size", "wal_size"}},
		{"sse lagging", alerts.Snapshot{Now: t0, SSEStreams: 4, SSELagged: 1}, []string{"sse_lagging"}},
		{"sse healthy", alerts.Snapshot{Now: t0, SSEStreams: 10, SSELagged: 1}, nil},
		{"agents paused", alerts.Snapshot{Now: t0, Agents: []*model.Agent{
			{ID: "a", Status: model.AgentPaused}, {ID: "b", Status: model.AgentPaused}, {ID: "c", Status: model.AgentPaused},
		}}, []string{"agents_paused"}},
		{"flapping", alerts.Snapshot{Now: t0, Agents: []*model.Agent{{ID: "a", Status: model.AgentPaused, PauseReason: str("flapping: 3 lease expiries")}}}, []string{"agent_flapping"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keys(alerts.Evaluate(c.snap, th)); !slices.Equal(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestThresholdZeroDisablesRule(t *testing.T) {
	th := alerts.Defaults()
	th.StallAfter = 0
	snap := alerts.Snapshot{Now: t0, Tasks: []*model.Task{ready("T-1", 24*time.Hour)}}
	if got := alerts.Evaluate(snap, th); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

type script struct {
	mu   sync.Mutex
	snap alerts.Snapshot
}

func (s *script) set(f func(*alerts.Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.snap)
}

func (s *script) Snapshot(_ context.Context, now time.Time) (alerts.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snap
	out.Now = now
	return out, nil
}

type recorder struct{ types []string }

func (r *recorder) Publish(_ []string, typ string, _ any) { r.types = append(r.types, typ) }

func TestEngineDeduplicatesAndResolves(t *testing.T) {
	now := t0
	src := &script{}
	rec := &recorder{}
	eng := alerts.New(alerts.Options{Source: src, Thresholds: alerts.Defaults(), Publisher: rec, Now: func() time.Time { return now }})
	ctx := context.Background()
	eval := func(advance time.Duration) {
		t.Helper()
		now = now.Add(advance)
		if err := eng.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	eval(time.Minute)
	readyAt := now
	stalled := func(s *alerts.Snapshot) {
		s.Tasks = []*model.Task{{ID: "T-1", Status: model.StatusReady, UpdatedAt: readyAt}}
	}
	src.set(stalled)
	eval(9 * time.Minute)
	if got := eng.List("", 0); len(got) != 0 {
		t.Fatalf("raised too early: %v", got)
	}
	eval(2 * time.Minute)
	raisedAt := now
	eval(time.Minute)
	eval(time.Minute)
	got := eng.List(alerts.StatusActive, 0)
	if len(got) != 1 || got[0].ID != "swarm_stalled" || !got[0].RaisedAt.Equal(raisedAt) {
		t.Fatalf("active = %+v", got)
	}
	if !slices.Equal(rec.types, []string{alerts.EventRaised}) {
		t.Fatalf("events = %v", rec.types)
	}
	ds, _ := eng.Decisions(ctx)
	if len(ds) != 1 || ds[0].ID != "alert:swarm_stalled" {
		t.Fatalf("decisions = %+v", ds)
	}

	src.set(func(s *alerts.Snapshot) { s.Tasks = nil })
	eval(time.Minute)
	if len(eng.List(alerts.StatusActive, 0)) != 0 {
		t.Fatal("alert did not resolve")
	}
	hist := eng.List(alerts.StatusResolved, 0)
	if len(hist) != 1 || hist[0].ResolvedAt == nil || !hist[0].ResolvedAt.Equal(now) {
		t.Fatalf("resolved = %+v", hist)
	}

	src.set(stalled)
	eval(time.Minute)
	if !slices.Equal(rec.types, []string{alerts.EventRaised, alerts.EventResolved, alerts.EventRaised}) {
		t.Fatalf("events = %v", rec.types)
	}
	if all := eng.List("", 0); len(all) != 2 || all[0].Status != alerts.StatusActive || all[1].Status != alerts.StatusResolved {
		t.Fatalf("list = %+v", all)
	}
}

func TestStoreSourceReadsOffsiteState(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	src := &alerts.StoreSource{Store: st, DBPath: filepath.Join(t.TempDir(), "s.db"), Offsite: true, Started: t0.Add(-72 * time.Hour)}

	snap, err := src.Snapshot(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.OffsiteEnabled || !snap.LastOffsite.Equal(src.Started) {
		t.Fatalf("without uploads the server start counts: %+v", snap)
	}
	if got := keys(alerts.Evaluate(snap, alerts.Defaults())); !slices.Equal(got, []string{"offsite_stale"}) {
		t.Fatalf("got %v", got)
	}

	uploaded := t0.Add(-time.Hour)
	state := offsite.State{
		Records: map[string]offsite.Record{
			"subspace-1.db": {Verified: true, UploadedAt: uploaded},
			"subspace-2.db": {Verified: false, UploadedAt: t0},
		},
		LastError: "destination unreachable",
	}
	raw, _ := json.Marshal(state)
	if err := st.Atomic(context.Background(), func(tx store.Tx) error { return tx.SetKV(offsite.StateKey, string(raw)) }); err != nil {
		t.Fatal(err)
	}
	snap, err = src.Snapshot(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.LastOffsite.Equal(uploaded) || snap.OffsiteError != "destination unreachable" {
		t.Fatalf("unverified uploads must not count: %+v", snap)
	}
	if got := keys(alerts.Evaluate(snap, alerts.Defaults())); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
