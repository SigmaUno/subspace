package mergequeue

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/SigmaUno/subspace/internal/store"
)

type Store interface {
	Insert(ctx context.Context, c *Candidate) error
	Update(ctx context.Context, c *Candidate) error
	Get(ctx context.Context, id string) (*Candidate, error)
	List(ctx context.Context, repo string) ([]*Candidate, error)
}

type MemStore struct {
	mu    sync.Mutex
	seq   int
	byID  map[string]Candidate
	order []string

	trainSeq int
	trains   []*Train
}

func NewMemStore() *MemStore {
	return &MemStore{byID: map[string]Candidate{}}
}

func (s *MemStore) Insert(_ context.Context, c *Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	c.ID = fmt.Sprintf("MQ-%d", s.seq)
	s.byID[c.ID] = *c
	s.order = append(s.order, c.ID)
	return nil
}

func (s *MemStore) Update(_ context.Context, c *Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[c.ID]; !ok {
		return store.ErrNotFound
	}
	s.byID[c.ID] = *c
	return nil
}

func (s *MemStore) Get(_ context.Context, id string) (*Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &c, nil
}

func (s *MemStore) List(_ context.Context, repo string) ([]*Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Candidate
	for _, id := range s.order {
		if c := s.byID[id]; repo == "" || c.Repo == repo {
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *MemStore) InsertTrain(_ context.Context, t *Train) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trainSeq++
	t.ID = fmt.Sprintf("MQT-%d", s.trainSeq)
	s.trains = append(s.trains, cloneTrain(t))
	return nil
}

func (s *MemStore) UpdateTrain(_ context.Context, t *Train) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.trains {
		if x.ID == t.ID {
			s.trains[i] = cloneTrain(t)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *MemStore) GetTrain(_ context.Context, id string) (*Train, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.trains {
		if x.ID == id {
			return cloneTrain(x), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *MemStore) ListTrains(_ context.Context, repo string) ([]*Train, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Train
	for _, x := range s.trains {
		if repo == "" || x.Repo == repo {
			out = append(out, cloneTrain(x))
		}
	}
	return out, nil
}

func cloneTrain(t *Train) *Train {
	c := *t
	c.Members = slices.Clone(t.Members)
	return &c
}
