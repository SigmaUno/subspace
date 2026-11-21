package acceptance

import (
	"context"
	"sync"
)

type bus struct {
	mu sync.Mutex
	ch chan struct{}
}

func newBus() *bus { return &bus{ch: make(chan struct{})} }

func (b *bus) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ch
}

func (b *bus) fire() {
	b.mu.Lock()
	close(b.ch)
	b.ch = make(chan struct{})
	b.mu.Unlock()
}

func (b *bus) until(ctx context.Context, ok func() bool) error {
	for {
		ch := b.wait()
		if ok() {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type group struct {
	wg     sync.WaitGroup
	cancel context.CancelFunc
	mu     sync.Mutex
	errs   []error
}

func newGroup(cancel context.CancelFunc) *group { return &group{cancel: cancel} }

func (g *group) run(ctx context.Context, name string, fn func(context.Context) error) {
	g.wg.Go(func() {
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			g.mu.Lock()
			g.errs = append(g.errs, &namedError{name, err})
			g.mu.Unlock()
			g.cancel()
		}
	})
}

func (g *group) wait() []error {
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]error(nil), g.errs...)
}

type namedError struct {
	name string
	err  error
}

func (e *namedError) Error() string { return e.name + ": " + e.err.Error() }

func (e *namedError) Unwrap() error { return e.err }
