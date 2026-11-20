package auth

import (
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const negMax = 10000

type entry struct {
	a   *model.Agent
	exp time.Time
}

// Cache resolves tokens to agents with a positive and a bounded negative cache.
type Cache struct {
	load func(hash string) (*model.Agent, error)
	ttl  time.Duration
	mu   sync.Mutex
	pos  map[string]entry // by token hash
	neg  map[string]time.Time
}

var _ AgentLookup = (*Cache)(nil)

// NewCache builds a cache around load (hash → agent, nil if unknown).
func NewCache(load func(hash string) (*model.Agent, error), ttl time.Duration) *Cache {
	return &Cache{load: load, ttl: ttl, pos: map[string]entry{}, neg: map[string]time.Time{}}
}

// LookupToken implements AgentLookup. Revoked agents resolve to nil.
func (c *Cache) LookupToken(token string) (*model.Agent, error) {
	h := Hash(token)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.pos[h]; ok && now.Before(e.exp) {
		c.mu.Unlock()
		return e.a, nil
	}
	if exp, ok := c.neg[h]; ok && now.Before(exp) {
		c.mu.Unlock()
		return nil, nil
	}
	c.mu.Unlock()
	a, err := c.load(h)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a == nil || a.Status == model.AgentRevoked {
		if len(c.neg) >= negMax {
			c.neg = map[string]time.Time{}
		}
		c.neg[h] = now.Add(5 * time.Second)
		return nil, nil
	}
	c.pos[h] = entry{a, now.Add(c.ttl)}
	return a, nil
}

// Invalidate drops cached entries for an agent id.
func (c *Cache) Invalidate(agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h, e := range c.pos {
		if e.a.ID == agentID {
			delete(c.pos, h)
		}
	}
	c.neg = map[string]time.Time{}
}

// InvalidateAll empties the cache.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pos, c.neg = map[string]entry{}, map[string]time.Time{}
}
