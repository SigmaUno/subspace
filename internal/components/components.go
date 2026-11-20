package components

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/model"
)

type Component struct {
	ID          string   `json:"id"`
	BriefID     string   `json:"brief_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	DependsOn   []string `json:"depends_on"`
}

type Spec struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	DependsOn   []string `json:"depends_on"`
}

type Task struct {
	*model.Task
	ComponentID string
}

type Store interface {
	Replace(ctx context.Context, briefID string, comps []Component) error
	List(ctx context.Context, briefID string) ([]Component, error)
}

type Source interface {
	Brief(ctx context.Context, id string) (*model.Brief, error)
	Tasks(ctx context.Context, briefID string) ([]Task, error)
	Agents(ctx context.Context) ([]*model.Agent, error)
}

type Memory struct {
	mu      sync.RWMutex
	byBrief map[string][]Component
}

func NewMemory() *Memory {
	return &Memory{byBrief: make(map[string][]Component)}
}

func (m *Memory) Replace(_ context.Context, briefID string, comps []Component) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byBrief[briefID] = cloneAll(comps)
	return nil
}

func (m *Memory) List(_ context.Context, briefID string) ([]Component, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneAll(m.byBrief[briefID]), nil
}

func cloneAll(in []Component) []Component {
	out := make([]Component, len(in))
	for i, c := range in {
		c.Paths = append([]string{}, c.Paths...)
		c.DependsOn = append([]string{}, c.DependsOn...)
		out[i] = c
	}
	return out
}
