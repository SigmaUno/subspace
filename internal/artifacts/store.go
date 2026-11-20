package artifacts

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

type Artifact struct {
	ID          string    `json:"id"`
	Scope       string    `json:"scope"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	ContentType string    `json:"content_type"`
	UploadedBy  string    `json:"uploaded_by"`
	Project     string    `json:"project"`
	CreatedAt   time.Time `json:"created_at"`
}

type Filter struct {
	Scopes []string
	Limit  int
}

type Store interface {
	Insert(ctx context.Context, a *Artifact) error
	Get(ctx context.Context, id string) (*Artifact, error)
	List(ctx context.Context, f Filter) ([]*Artifact, error)
	Delete(ctx context.Context, id string) error
	ProjectUsage(ctx context.Context, project string) (int64, error)
	CountBySHA(ctx context.Context, sha string) (int, error)
}

type MemStore struct {
	mu    sync.Mutex
	seq   int
	byID  map[string]Artifact
	order []string
}

func NewMemStore() *MemStore {
	return &MemStore{byID: map[string]Artifact{}}
}

func (s *MemStore) Insert(_ context.Context, a *Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	a.ID = fmt.Sprintf("A-%d", s.seq)
	s.byID[a.ID] = *a
	s.order = append(s.order, a.ID)
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (*Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &a, nil
}

func (s *MemStore) List(_ context.Context, f Filter) ([]*Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Artifact
	for _, id := range s.order {
		a := s.byID[id]
		if len(f.Scopes) > 0 && !slices.Contains(f.Scopes, a.Scope) {
			continue
		}
		out = append(out, &a)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

func (s *MemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.byID, id)
	s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
	return nil
}

func (s *MemStore) ProjectUsage(_ context.Context, project string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, a := range s.byID {
		if a.Project == project {
			n += a.Size
		}
	}
	return n, nil
}

func (s *MemStore) CountBySHA(_ context.Context, sha string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.byID {
		if a.SHA256 == sha {
			n++
		}
	}
	return n, nil
}
