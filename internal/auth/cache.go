package auth

import (
	"errors"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	negativeCacheMax = 10000
	negativeCacheTTL = 5 * time.Second
)

type posEntry struct {
	agent   model.Agent
	expires time.Time
}

type Cache struct {
	load func(hash string) (*model.Agent, error)
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	gen     uint64
	pos     map[string]posEntry
	byAgent map[string]map[string]struct{}
	neg     map[string]time.Time
}

var _ AgentLookup = (*Cache)(nil)

func NewCache(load func(hash string) (*model.Agent, error), ttl time.Duration) *Cache {
	return &Cache{
		load:    load,
		ttl:     ttl,
		now:     time.Now,
		pos:     make(map[string]posEntry),
		byAgent: make(map[string]map[string]struct{}),
		neg:     make(map[string]time.Time),
	}
}

func (c *Cache) LookupToken(token string) (*model.Agent, error) {
	if token == "" {
		return nil, nil
	}
	hash := Hash(token)
	now := c.now()

	c.mu.Lock()
	if e, ok := c.pos[hash]; ok {
		if now.Before(e.expires) {
			a := e.agent
			c.mu.Unlock()
			return &a, nil
		}
		c.removePosLocked(hash, e.agent.ID)
	}
	if exp, ok := c.neg[hash]; ok {
		if now.Before(exp) {
			c.mu.Unlock()
			return nil, nil
		}
		delete(c.neg, hash)
	}
	gen := c.gen
	c.mu.Unlock()

	a, err := c.load(hash)
	if errors.Is(err, store.ErrNotFound) {
		a, err = nil, nil
	}
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	fresh := gen == c.gen
	if a == nil || a.Status == model.AgentRevoked {
		if fresh {
			c.putNegLocked(hash, now)
		}
		return nil, nil
	}
	cp := *a
	if fresh && c.ttl > 0 {
		c.removePosLocked(hash, "")
		c.pos[hash] = posEntry{agent: cp, expires: now.Add(c.ttl)}
		set := c.byAgent[cp.ID]
		if set == nil {
			set = make(map[string]struct{})
			c.byAgent[cp.ID] = set
		}
		set[hash] = struct{}{}
	}
	out := cp
	return &out, nil
}

func (c *Cache) Invalidate(agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for hash := range c.byAgent[agentID] {
		delete(c.pos, hash)
	}
	delete(c.byAgent, agentID)
}

func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.pos = make(map[string]posEntry)
	c.byAgent = make(map[string]map[string]struct{})
	c.neg = make(map[string]time.Time)
}

func (c *Cache) removePosLocked(hash, agentID string) {
	if agentID == "" {
		e, ok := c.pos[hash]
		if !ok {
			return
		}
		agentID = e.agent.ID
	}
	delete(c.pos, hash)
	if set := c.byAgent[agentID]; set != nil {
		delete(set, hash)
		if len(set) == 0 {
			delete(c.byAgent, agentID)
		}
	}
}

func (c *Cache) putNegLocked(hash string, now time.Time) {
	if len(c.neg) >= negativeCacheMax {
		for h, exp := range c.neg {
			if !now.Before(exp) {
				delete(c.neg, h)
			}
		}
		for h := range c.neg {
			if len(c.neg) < negativeCacheMax {
				break
			}
			delete(c.neg, h)
		}
	}
	c.neg[hash] = now.Add(negativeCacheTTL)
}
