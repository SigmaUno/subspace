package board_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type gatedStore struct {
	store.Store
	mu      sync.Mutex
	hold    chan struct{}
	entered chan struct{}
}

func (g *gatedStore) arm() (entered, release chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hold, g.entered = make(chan struct{}), make(chan struct{})
	return g.entered, g.hold
}

func (g *gatedStore) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	g.mu.Lock()
	hold, entered := g.hold, g.entered
	g.hold, g.entered = nil, nil
	g.mu.Unlock()
	if hold != nil {
		close(entered)
		<-hold
	}
	return g.Store.Atomic(ctx, fn)
}

func TestPauseAllWinsAgainstInFlightClaimAndSpawn(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g := &gatedStore{Store: st}
	b, err := board.New(ctx, g, board.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lead := model.Actor{ID: "rex", Role: model.RoleLeader}
	if _, err := b.RegisterAgent(ctx, board.AgentInput{ID: "w1", Role: model.RoleWorker, Kind: model.KindClaudeCode}); err != nil {
		t.Fatal(err)
	}
	br, err := b.CreateBrief(ctx, board.BriefInput{Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.ProposePlan(ctx, lead, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{{Title: "t", Spec: "s", Repo: "o/r"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApprovePlan(ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	tid := p.Tasks[0].ID
	if _, err := b.Assign(ctx, lead, tid, "w1", ""); err != nil {
		t.Fatal(err)
	}

	race := func(op func() error) error {
		entered, release := g.arm()
		errc := make(chan error, 1)
		go func() { errc <- op() }()
		<-entered
		if err := b.PauseAll(ctx, "stop"); err != nil {
			t.Fatal(err)
		}
		close(release)
		return <-errc
	}

	claimErr := race(func() error {
		_, err := b.ClaimTask(ctx, model.Actor{ID: "w1", Role: model.RoleWorker}, tid)
		return err
	})
	if model.AsError(claimErr).Code != "paused" {
		t.Fatalf("claim during pause_all: err=%v", claimErr)
	}
	tk, err := b.GetTask(ctx, model.Owner, tid)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != model.StatusAssigned {
		t.Fatalf("task is %s while paused", tk.Status)
	}

	if err := b.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}
	spawnErr := race(func() error {
		_, err := b.RequestSpawn(ctx, lead, board.SpawnInput{BriefID: br.ID, Kind: model.KindCodex, Model: "m", N: 1})
		return err
	})
	if model.AsError(spawnErr).Code != "paused" {
		t.Fatalf("spawn during pause_all: err=%v", spawnErr)
	}
}
