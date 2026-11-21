package auth

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestRevokedTokenNeverServedAfterInvalidate(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= 500; seed++ {
		r := rand.New(rand.NewSource(seed))
		db := newFakeDB()
		now := start
		c := NewCache(db.load, time.Duration(1+r.Intn(60))*time.Second)
		c.now = func() time.Time { return now }

		owner := map[string]string{}
		revoked := map[string]bool{}
		agents := []string{"a0", "a1", "a2", "a3"}
		tokens := 0
		register := func(agent string) string {
			tokens++
			tk := fmt.Sprintf("tok-%d", tokens)
			db.put(tk, model.Agent{ID: agent, Status: model.AgentActive})
			owner[tk] = agent
			return tk
		}
		for _, a := range agents {
			register(a)
		}

		for step := 0; step < 300; step++ {
			switch op := r.Intn(8); {
			case op == 0:
				now = now.Add(time.Duration(r.Intn(40)) * time.Second)
			case op == 1:
				agent := agents[r.Intn(len(agents))]
				revoked[agent] = true
				for tk, a := range owner {
					if a == agent {
						db.put(tk, model.Agent{ID: agent, Status: model.AgentRevoked})
					}
				}
				c.Invalidate(agent)
			case op == 2:
				agent := agents[r.Intn(len(agents))]
				if !revoked[agent] {
					for tk, a := range owner {
						if a == agent {
							db.mu.Lock()
							delete(db.agents, Hash(tk))
							db.mu.Unlock()
							delete(owner, tk)
						}
					}
					register(agent)
					c.Invalidate(agent)
				}
			case op == 3 && r.Intn(5) == 0:
				c.InvalidateAll()
			default:
				for tk, agent := range owner {
					got, err := c.LookupToken(tk)
					if err != nil {
						t.Fatalf("seed %d step %d: %v", seed, step, err)
					}
					if revoked[agent] && got != nil {
						t.Fatalf("seed %d step %d: revoked agent %s served via %s", seed, step, agent, tk)
					}
					if !revoked[agent] && (got == nil || got.ID != agent) {
						t.Fatalf("seed %d step %d: live token %s of %s resolved to %+v", seed, step, tk, agent, got)
					}
				}
			}
		}
	}
}
