package loadtest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
)

const duplicateClaimers = 3

type claimBoard struct {
	mu       sync.Mutex
	winners  map[string][]string
	assignee map[string]string
	attempts atomic.Int64
	rejected atomic.Int64
	failed   atomic.Int64
}

func (cb *claimBoard) won(id, agent string) {
	cb.mu.Lock()
	cb.winners[id] = append(cb.winners[id], agent)
	cb.mu.Unlock()
}

func contention(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	workers, reviewers, err := r.setupTeam(ctx, true)
	if err != nil {
		return nil, SSEReport{}, err
	}
	clients := make([]*client.MCP, len(workers))
	if err := r.parallel(ctx, len(workers), func(i int) error {
		clients[i] = r.mkSoft(workers[i])
		return clients[i].Initialize(ctx)
	}); err != nil {
		return nil, SSEReport{}, fmt.Errorf("worker initialize: %w", err)
	}

	sctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()
	p := r.startPipeline(sctx, reviewers)
	cb := &claimBoard{winners: map[string][]string{}, assignee: map[string]string{}}
	var all []string
	for wave := 0; wave < contentionWaves && sctx.Err() == nil; wave++ {
		specs := make([]board.TaskInput, r.cfg.Tasks)
		for i := range specs {
			specs[i] = r.specFor(r.brief, r.seq.Add(1))
		}
		ids, err := r.plan(sctx, r.brief, specs)
		if err != nil {
			return nil, SSEReport{}, err
		}
		atomic.AddInt64(&r.tasks.Created, int64(len(ids)))
		offset := wave * len(ids)
		if err := r.assignAll(sctx, ids, workers, offset); err != nil {
			return nil, SSEReport{}, err
		}
		for j, id := range ids {
			cb.assignee[id] = workers[(offset+j)%len(workers)].id
		}
		all = append(all, ids...)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r.contend(sctx, cb, workers[i], clients[i], ids, i, p)
			}()
		}
		close(start)
		wg.Wait()
	}
	finished := r.awaitDone(sctx, int64(len(all)))
	cancel()
	p.stop()

	return r.contentionInvariants(ctx, cb, all, finished), SSEReport{}, nil
}

func (r *runner) contend(ctx context.Context, cb *claimBoard, w agentCreds, c *client.MCP, ids []string, offset int, p *pipeline) {
	for k := range ids {
		id := ids[(offset+k)%len(ids)]
		claimers := []*client.MCP{c}
		if cb.assignee[id] == w.id {
			for range duplicateClaimers {
				claimers = append(claimers, r.mkSoft(w))
			}
		}
		var wg sync.WaitGroup
		for _, cl := range claimers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cb.attempts.Add(1)
				_, err := cl.ClaimTask(ctx, id)
				switch {
				case err == nil:
					cb.won(id, w.id)
					atomic.AddInt64(&r.tasks.Claimed, 1)
					if r.complete(ctx, cl, id) {
						p.submit(ctx, id)
					}
				case rejected(err):
					cb.rejected.Add(1)
				case ctx.Err() == nil:
					cb.failed.Add(1)
				}
			}()
		}
		wg.Wait()
	}
}

func (r *runner) contentionInvariants(ctx context.Context, cb *claimBoard, ids []string, finished bool) []Invariant {
	var multi, none, wrong int
	for _, id := range ids {
		ws := cb.winners[id]
		switch {
		case len(ws) == 0:
			none++
		case len(ws) > 1:
			multi++
		case ws[0] != cb.assignee[id]:
			wrong++
		}
	}
	invs := []Invariant{
		check("exactly one claim winner per task", multi == 0 && none == 0, "%d tasks, %d without winner, %d with several", len(ids), none, multi),
		check("only the assignee wins", wrong == 0, "%d wrong winners", wrong),
		check("claims either win or are rejected", cb.failed.Load() == 0, "%d claims, %d rejected, %d failed",
			cb.attempts.Load(), cb.rejected.Load(), cb.failed.Load()),
	}
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bad, err := r.onePerTask(vctx, "claim_task", ids)
	invs = append(invs, errOr(err, check("audit has one claim per task", bad == 0, "%d tasks with another count", bad)))
	inv, err := r.allDone(vctx, len(ids), r.brief)
	return append(invs, errOr(err, inv), check("drained in time", finished, "%d tasks", len(ids)))
}

func errOr(err error, inv Invariant) Invariant {
	if err != nil {
		return Invariant{Name: inv.Name, Detail: err.Error()}
	}
	return inv
}
