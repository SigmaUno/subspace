package auth

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type fakeDB struct {
	mu     sync.Mutex
	agents map[string]model.Agent
	calls  atomic.Int64
}

func newFakeDB() *fakeDB { return &fakeDB{agents: map[string]model.Agent{}} }

func (d *fakeDB) put(token string, a model.Agent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agents[Hash(token)] = a
}

func (d *fakeDB) load(hash string) (*model.Agent, error) {
	d.calls.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.agents[hash]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func TestCachePositiveAndExpiry(t *testing.T) {
	db := newFakeDB()
	db.put("tok", model.Agent{ID: "a1", Role: "worker", Status: model.AgentActive})
	c := NewCache(db.load, time.Minute)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		a, err := c.LookupToken("tok")
		if err != nil || a == nil || a.ID != "a1" {
			t.Fatalf("lookup %d: %v %v", i, a, err)
		}
	}
	if n := db.calls.Load(); n != 1 {
		t.Fatalf("loads = %d, want 1", n)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.LookupToken("tok"); err != nil {
		t.Fatal(err)
	}
	if n := db.calls.Load(); n != 2 {
		t.Fatalf("loads after ttl = %d, want 2", n)
	}
}

func TestCacheReturnsCopy(t *testing.T) {
	db := newFakeDB()
	db.put("tok", model.Agent{ID: "a1", Status: model.AgentActive})
	c := NewCache(db.load, time.Minute)
	a, _ := c.LookupToken("tok")
	a.Role = "mutated"
	b, _ := c.LookupToken("tok")
	if b.Role == "mutated" {
		t.Fatal("cache entry shared with caller")
	}
}

func TestCacheRevokedAndUnknown(t *testing.T) {
	db := newFakeDB()
	db.put("rev", model.Agent{ID: "a2", Status: model.AgentRevoked})
	c := NewCache(db.load, time.Minute)
	if a, err := c.LookupToken("rev"); a != nil || err != nil {
		t.Fatalf("revoked: %v %v", a, err)
	}
	if a, err := c.LookupToken("nope"); a != nil || err != nil {
		t.Fatalf("unknown: %v %v", a, err)
	}
	if a, err := c.LookupToken(""); a != nil || err != nil {
		t.Fatalf("empty: %v %v", a, err)
	}
	before := db.calls.Load()
	_, _ = c.LookupToken("nope")
	_, _ = c.LookupToken("rev")
	if db.calls.Load() != before {
		t.Fatal("negative results not cached")
	}
}

func TestCacheNotFoundErrorIsNil(t *testing.T) {
	c := NewCache(func(string) (*model.Agent, error) { return nil, fmt.Errorf("x: %w", store.ErrNotFound) }, time.Minute)
	if a, err := c.LookupToken("t"); a != nil || err != nil {
		t.Fatalf("got %v %v", a, err)
	}
}

func TestCacheLoadErrorNotCached(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	boom := errors.New("boom")
	c := NewCache(func(string) (*model.Agent, error) {
		if fail.Load() {
			return nil, boom
		}
		return &model.Agent{ID: "a", Status: model.AgentActive}, nil
	}, time.Minute)
	if _, err := c.LookupToken("t"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	fail.Store(false)
	if a, err := c.LookupToken("t"); err != nil || a == nil {
		t.Fatalf("after recovery: %v %v", a, err)
	}
}

func TestCacheNegativeExpires(t *testing.T) {
	db := newFakeDB()
	c := NewCache(db.load, time.Minute)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	_, _ = c.LookupToken("late")
	db.put("late", model.Agent{ID: "a3", Status: model.AgentActive})
	if a, _ := c.LookupToken("late"); a != nil {
		t.Fatal("negative entry should still apply")
	}
	now = now.Add(negativeCacheTTL + time.Millisecond)
	if a, _ := c.LookupToken("late"); a == nil {
		t.Fatal("negative entry should have expired")
	}
}

func TestCacheNegativeBounded(t *testing.T) {
	db := newFakeDB()
	c := NewCache(db.load, time.Minute)
	for i := 0; i < negativeCacheMax+500; i++ {
		_, _ = c.LookupToken(fmt.Sprintf("bad-%d", i))
	}
	c.mu.Lock()
	n := len(c.neg)
	c.mu.Unlock()
	if n > negativeCacheMax {
		t.Fatalf("negative cache size %d > %d", n, negativeCacheMax)
	}
}

func TestCacheInvalidate(t *testing.T) {
	db := newFakeDB()
	db.put("t1", model.Agent{ID: "a1", Status: model.AgentActive})
	db.put("t1b", model.Agent{ID: "a1", Status: model.AgentActive})
	db.put("t2", model.Agent{ID: "a2", Status: model.AgentActive})
	c := NewCache(db.load, time.Hour)
	for _, tk := range []string{"t1", "t1b", "t2"} {
		_, _ = c.LookupToken(tk)
	}
	if db.calls.Load() != 3 {
		t.Fatal("setup")
	}

	db.put("t1", model.Agent{ID: "a1", Status: model.AgentRevoked})
	db.put("t1b", model.Agent{ID: "a1", Status: model.AgentRevoked})
	if a, _ := c.LookupToken("t1"); a == nil {
		t.Fatal("expected cached entry before invalidation")
	}
	c.Invalidate("a1")
	for _, tk := range []string{"t1", "t1b"} {
		if a, _ := c.LookupToken(tk); a != nil {
			t.Fatalf("%s still resolves after Invalidate", tk)
		}
	}
	if a, _ := c.LookupToken("t2"); a == nil || db.calls.Load() != 5 {
		t.Fatalf("a2 should stay cached (calls=%d)", db.calls.Load())
	}

	c.InvalidateAll()
	before := db.calls.Load()
	_, _ = c.LookupToken("t2")
	if db.calls.Load() != before+1 {
		t.Fatal("InvalidateAll did not clear positive entries")
	}
}

func TestCacheInvalidateDuringLoadNotCached(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := NewCache(func(string) (*model.Agent, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return &model.Agent{ID: "a1", Status: model.AgentActive}, nil
	}, time.Hour)
	done := make(chan struct{})
	go func() {
		_, _ = c.LookupToken("t")
		close(done)
	}()
	<-started
	c.Invalidate("a1")
	close(release)
	<-done
	_, _ = c.LookupToken("t")
	if calls.Load() != 2 {
		t.Fatalf("stale load was cached (calls=%d)", calls.Load())
	}
}

func TestCacheConcurrent(t *testing.T) {
	db := newFakeDB()
	const agents = 20
	for i := 0; i < agents; i++ {
		db.put(fmt.Sprintf("tok-%d", i), model.Agent{ID: fmt.Sprintf("a%d", i), Status: model.AgentActive})
	}
	c := NewCache(db.load, 5*time.Millisecond)
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				k := (g + i) % (agents + 5)
				a, err := c.LookupToken(fmt.Sprintf("tok-%d", k))
				if err != nil {
					t.Error(err)
					return
				}
				if k < agents && (a == nil || a.ID != fmt.Sprintf("a%d", k)) {
					t.Errorf("tok-%d resolved to %v", k, a)
					return
				}
				if k >= agents && a != nil {
					t.Errorf("tok-%d should be unknown", k)
					return
				}
				switch i % 97 {
				case 0:
					c.Invalidate(fmt.Sprintf("a%d", k))
				case 1:
					if g == 0 {
						c.InvalidateAll()
					}
				}
			}
		}(g)
	}
	wg.Wait()
}
