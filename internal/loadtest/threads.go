package loadtest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
)

const readPage = 100

type threadReader struct {
	ids  []int64
	ours int
	dups int
	hung bool
}

func threadHeavy(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	workers, _, err := r.setupTeam(ctx, false)
	if err != nil {
		return nil, SSEReport{}, err
	}
	n := r.cfg.Messages
	clients := make([]*client.MCP, len(workers))
	if err := r.parallel(ctx, len(workers), func(i int) error {
		clients[i] = r.mk(workers[i])
		return clients[i].Initialize(ctx)
	}); err != nil {
		return nil, SSEReport{}, fmt.Errorf("worker initialize: %w", err)
	}

	sctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()
	readers := make([]threadReader, len(workers))
	var posted atomic.Int64
	var wg sync.WaitGroup
	for i, w := range workers {
		prefix := r.runID + "-" + w.id + "-"
		ownerShare := n / 2
		wg.Add(3)
		go func() {
			defer wg.Done()
			for j := range ownerShare {
				if _, err := r.owner.PostThreadMessage(sctx, "agent", w.id, fmt.Sprint(prefix, j)); err != nil {
					return
				}
				posted.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			for j := ownerShare; j < n; j++ {
				if _, err := clients[i].AddNote(sctx, "", fmt.Sprint(prefix, j)); err != nil {
					return
				}
				posted.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			r.readThread(sctx, clients[i], "agent:"+w.id, prefix, n, &readers[i])
		}()
	}
	wg.Wait()
	cancel()

	vctx, vcancel := context.WithTimeout(ctx, 60*time.Second)
	defer vcancel()
	var hung, dups, mismatched, wrongLen int
	for i, w := range workers {
		stored, err := r.verify.ThreadMessages(vctx, "agent", w.id, 0, pageSize)
		if err != nil {
			return nil, SSEReport{}, err
		}
		for len(stored) > 0 && len(stored)%pageSize == 0 {
			more, err := r.verify.ThreadMessages(vctx, "agent", w.id, stored[len(stored)-1].ID, pageSize)
			if err != nil || len(more) == 0 {
				break
			}
			stored = append(stored, more...)
		}
		prefix := r.runID + "-" + w.id + "-"
		ours := 0
		rd := readers[i]
		var storedIDs []int64
		for _, m := range stored {
			if strings.HasPrefix(m.Body, prefix) {
				ours++
			}
			if len(rd.ids) > 0 && m.ID <= rd.ids[len(rd.ids)-1] {
				storedIDs = append(storedIDs, m.ID)
			}
		}
		if rd.hung {
			hung++
		}
		dups += rd.dups
		if !slices.Equal(rd.ids, storedIDs) {
			mismatched++
		}
		if ours != n {
			wrongLen++
		}
	}
	return []Invariant{
		check("every message was posted", posted.Load() == int64(len(workers)*n), "%d of %d", posted.Load(), len(workers)*n),
		check("each thread holds exactly its messages", wrongLen == 0, "%d threads x %d messages, %d threads differ", len(workers), n, wrongLen),
		check("readers saw each message exactly once, in order, nothing skipped", mismatched == 0 && dups == 0,
			"%d readers differ from the stored thread, %d duplicates", mismatched, dups),
		check("readers caught up in time", hung == 0, "%d readers did not finish", hung),
	}, SSEReport{}, nil
}

func (r *runner) readThread(ctx context.Context, c *client.MCP, thread, prefix string, want int, out *threadReader) {
	var cursor int64
	for out.ours < want {
		if ctx.Err() != nil {
			out.hung = true
			return
		}
		msgs, err := c.ReadThread(ctx, thread, cursor, readPage)
		if err != nil || len(msgs) == 0 {
			sleep(ctx, max(r.cfg.Think, 5*time.Millisecond))
			continue
		}
		for _, m := range msgs {
			if m.ID <= cursor {
				out.dups++
				continue
			}
			cursor = m.ID
			out.ids = append(out.ids, m.ID)
			if strings.HasPrefix(m.Body, prefix) {
				out.ours++
			}
		}
	}
}
