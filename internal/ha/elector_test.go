package ha

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

type simDB struct {
	mu     sync.Mutex
	holder string
	down   map[string]bool
}

type simLock struct {
	db *simDB
	id string
}

var errDown = errors.New("connection lost")

func (l *simLock) Acquire(context.Context) (bool, error) {
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	if l.db.down[l.id] {
		return false, errDown
	}
	if l.db.holder == "" {
		l.db.holder = l.id
	}
	return l.db.holder == l.id, nil
}

func (l *simLock) Renew(context.Context) error {
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	if l.db.down[l.id] || l.db.holder != l.id {
		return errDown
	}
	return nil
}

func (l *simLock) Holder(context.Context) (string, error) {
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	return l.db.holder, nil
}

func (l *simLock) Release() {
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	if l.db.holder == l.id {
		l.db.holder = ""
	}
}

func (db *simDB) partition(id string, down bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.down[id] = down
	if down && db.holder == id {
		db.holder = ""
	}
}

func TestElectorSimulatedPartitions(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*7))
		db := &simDB{down: map[string]bool{}}
		ids := []string{"a", "b", "c"}
		els := map[string]*Elector{}
		for _, id := range ids {
			els[id] = New(Options{ID: id, Backend: "sim", Lock: &simLock{db: db, id: id}})
		}
		ctx := context.Background()
		stale := map[string]bool{}
		terms := 0
		for range 400 {
			id := ids[rng.IntN(len(ids))]
			if rng.IntN(10) == 0 {
				db.partition(id, !db.down[id])
				if els[id].IsLeader() {
					stale[id] = true
				}
				continue
			}
			before := els[id].IsLeader()
			els[id].step(ctx)
			after := els[id].IsLeader()
			if stale[id] && after {
				t.Fatalf("seed %d: %s kept leadership after losing its lock", seed, id)
			}
			delete(stale, id)
			if !before && after {
				terms++
			}
			holders := 0
			for _, other := range ids {
				if els[other].IsLeader() && !stale[other] {
					holders++
					db.mu.Lock()
					owner := db.holder
					db.mu.Unlock()
					if owner != other {
						t.Fatalf("seed %d: %s leads without holding the lock (holder %q)", seed, other, owner)
					}
				}
			}
			if holders > 1 {
				t.Fatalf("seed %d: %d leaders", seed, holders)
			}
		}
		if terms == 0 {
			t.Fatalf("seed %d: nobody ever became leader", seed)
		}
	}
}

func TestLeadRunsOnlyDuringTerms(t *testing.T) {
	db := &simDB{down: map[string]bool{}}
	e := New(Options{ID: "a", Lock: &simLock{db: db, id: "a"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Lead(ctx, func(run context.Context) { started <- run; <-run.Done() })
	}()
	hub := e.Gate(events.NewHub())
	sub := hub.Subscribe([]string{"board"})
	defer sub.Close()

	hub.Publish([]string{"board"}, "task.updated", 1)
	select {
	case <-started:
		t.Fatal("ran without leadership")
	case <-sub.C():
		t.Fatal("follower received a gated event")
	case <-time.After(50 * time.Millisecond):
	}

	e.step(ctx)
	first := <-started
	hub.Publish([]string{"board"}, "task.updated", 2)
	if ev := <-sub.C(); ev.Data != 2 {
		t.Fatalf("leader got %v", ev.Data)
	}

	db.partition("a", true)
	e.step(ctx)
	<-first.Done()
	db.partition("a", false)
	e.step(ctx)
	second := <-started
	if second.Err() != nil || e.Status().Term != 2 {
		t.Fatalf("second term not running: %v term %d", second.Err(), e.Status().Term)
	}
	cancel()
	<-done
}
