package usage

import (
	"context"
	"sync"
	"time"
)

const (
	SourceMCP      = "mcp"
	SourceOpenCode = "opencode"
	SourceClaude   = "claude"
	SourceCodex    = "codex"
)

type Record struct {
	TS          time.Time `json:"ts"`
	AgentID     string    `json:"agent_id"`
	TaskID      string    `json:"task_id"`
	BriefID     string    `json:"brief_id"`
	ComponentID string    `json:"component_id"`
	Kind        string    `json:"kind"`
	Role        string    `json:"role"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	TokensIn    int64     `json:"tokens_in"`
	TokensOut   int64     `json:"tokens_out"`
	CacheRead   int64     `json:"cache_read"`
	CacheWrite  int64     `json:"cache_write"`
	CostUSD     float64   `json:"cost_usd"`
	Source      string    `json:"source"`
	RateLimited bool      `json:"rate_limited,omitempty"`
	Session     string    `json:"session,omitempty"`

	Key string `json:"-"`
}

func (r *Record) add(o Record) {
	r.TokensIn += o.TokensIn
	r.TokensOut += o.TokensOut
	r.CacheRead += o.CacheRead
	r.CacheWrite += o.CacheWrite
	r.CostUSD += o.CostUSD
}

type Query struct {
	From, To time.Time
	BriefID  string
	AgentID  string
	TaskID   string
	Provider string
	Model    string
	Source   string
}

func (q Query) match(r *Record) bool {
	switch {
	case !q.From.IsZero() && r.TS.Before(q.From):
		return false
	case !q.To.IsZero() && !r.TS.Before(q.To):
		return false
	case q.BriefID != "" && r.BriefID != q.BriefID,
		q.AgentID != "" && r.AgentID != q.AgentID,
		q.TaskID != "" && r.TaskID != q.TaskID,
		q.Provider != "" && r.Provider != q.Provider,
		q.Model != "" && r.Model != q.Model,
		q.Source != "" && r.Source != q.Source:
		return false
	}
	return true
}

type Store interface {
	Put(ctx context.Context, recs []Record) error
	Query(ctx context.Context, q Query) ([]Record, error)
	SessionTotal(ctx context.Context, source, session string) (Record, error)
	Cursor(ctx context.Context, name string) (string, error)
	SetCursor(ctx context.Context, name, value string) error
}

type MemStore struct {
	mu      sync.RWMutex
	recs    []Record
	byKey   map[string]int
	cursors map[string]string
}

func NewMemStore() *MemStore {
	return &MemStore{byKey: map[string]int{}, cursors: map[string]string{}}
}

func (s *MemStore) Put(_ context.Context, recs []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		if i, ok := s.byKey[r.Key]; ok && r.Key != "" {
			s.recs[i] = r
			continue
		}
		if r.Key != "" {
			s.byKey[r.Key] = len(s.recs)
		}
		s.recs = append(s.recs, r)
	}
	return nil
}

func (s *MemStore) Query(_ context.Context, q Query) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for i := range s.recs {
		if q.match(&s.recs[i]) {
			out = append(out, s.recs[i])
		}
	}
	return out, nil
}

func (s *MemStore) SessionTotal(_ context.Context, source, session string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := Record{Source: source, Session: session}
	for _, r := range s.recs {
		if r.Source == source && r.Session == session {
			t.add(r)
		}
	}
	return t, nil
}

func (s *MemStore) Cursor(_ context.Context, name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cursors[name], nil
}

func (s *MemStore) SetCursor(_ context.Context, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[name] = value
	return nil
}
