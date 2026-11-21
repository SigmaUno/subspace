package spawner

import (
	"context"
	"sync"
)

type Fake struct {
	mu      sync.Mutex
	running map[string]bool
}

func (*Fake) Name() string { return "fake" }

func (f *Fake) Spawn(_ context.Context, r Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running == nil {
		f.running = map[string]bool{}
	}
	f.running[r.AgentID] = true
	return nil
}

func (f *Fake) Stop(_ context.Context, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, agentID)
	return nil
}

func (f *Fake) Status(_ context.Context, agentID string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[agentID] {
		return StatusRunning, nil
	}
	return StatusExited, nil
}
