package decisions

import (
	"context"
	"log/slog"
	"sync"

	"github.com/SigmaUno/subspace/internal/store"
)

type Contributor interface {
	Decisions(ctx context.Context) ([]*Decision, error)
}

type registration struct{ c Contributor }

var (
	hookMu sync.Mutex
	hooks  = map[store.Store][]*registration{}
)

func Attach(st store.Store, c Contributor) (detach func()) {
	r := &registration{c: c}
	hookMu.Lock()
	hooks[st] = append(hooks[st], r)
	hookMu.Unlock()
	return func() {
		hookMu.Lock()
		defer hookMu.Unlock()
		rs := hooks[st]
		for i, x := range rs {
			if x == r {
				hooks[st] = append(rs[:i:i], rs[i+1:]...)
				break
			}
		}
		if len(hooks[st]) == 0 {
			delete(hooks, st)
		}
	}
}

func contributed(ctx context.Context, st store.Store) []*Decision {
	hookMu.Lock()
	rs := append([]*registration(nil), hooks[st]...)
	hookMu.Unlock()
	var out []*Decision
	for _, r := range rs {
		ds, err := r.c.Decisions(ctx)
		if err != nil {
			slog.Warn("decision contributor failed", "err", err)
			continue
		}
		out = append(out, ds...)
	}
	return out
}
