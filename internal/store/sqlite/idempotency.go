package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	extraSchemas = append(extraSchemas, `
CREATE TABLE IF NOT EXISTS idempotency_keys (
  agent_id TEXT NOT NULL, key TEXT NOT NULL, tool TEXT NOT NULL, args_hash TEXT NOT NULL,
  result TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, key)
);
CREATE INDEX IF NOT EXISTS idempotency_keys_expires ON idempotency_keys(expires_at);`)
}

var _ idempotency.Store = (*Store)(nil)

const idemSweepEvery = time.Minute

func (s *Store) GetIdempotency(ctx context.Context, agentID, key string, now time.Time) (*idempotency.Record, error) {
	var r idempotency.Record
	var res string
	var exp int64
	err := s.rdb.QueryRowContext(ctx, `SELECT tool, args_hash, result, expires_at FROM idempotency_keys WHERE agent_id = ? AND key = ? AND expires_at > ?`,
		agentID, key, now.UnixMilli()).Scan(&r.Tool, &r.ArgsHash, &res, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Result, r.ExpiresAt = []byte(res), time.UnixMilli(exp).UTC()
	return &r, nil
}

type idemPut struct {
	agentID, key string
	r            *idempotency.Record
	now          int64
}

type idemFlush struct {
	done chan struct{}
	err  error
}

type idemQueue struct {
	mu    sync.Mutex
	puts  []idemPut
	flush *idemFlush
	sweep atomic.Int64
}

func (s *Store) PutIdempotency(ctx context.Context, agentID, key string, r *idempotency.Record, now time.Time) error {
	q := &s.idem
	q.mu.Lock()
	q.puts = append(q.puts, idemPut{agentID: agentID, key: key, r: r, now: now.UnixMilli()})
	f, lead := q.flush, q.flush == nil
	if lead {
		f = &idemFlush{done: make(chan struct{})}
		q.flush = f
	}
	q.mu.Unlock()
	if !lead {
		<-f.done
		return f.err
	}
	f.err = s.Atomic(ctx, func(t store.Tx) error {
		q.mu.Lock()
		puts := q.puts
		q.puts, q.flush = nil, nil
		q.mu.Unlock()
		return s.writeIdempotency(t.(*tx), puts, now.UnixMilli())
	})
	q.mu.Lock()
	if q.flush == f {
		q.puts, q.flush = nil, nil
	}
	q.mu.Unlock()
	close(f.done)
	return f.err
}

func (s *Store) writeIdempotency(t *tx, puts []idemPut, now int64) error {
	if last := s.idem.sweep.Load(); now-last >= idemSweepEvery.Milliseconds() || now < last {
		if s.idem.sweep.CompareAndSwap(last, now) {
			if _, err := t.q.ExecContext(t.ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ?`, now); err != nil {
				return err
			}
		}
	}
	for _, p := range puts {
		_, err := t.q.ExecContext(t.ctx, `INSERT INTO idempotency_keys (agent_id, key, tool, args_hash, result, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(agent_id, key) DO UPDATE SET tool = excluded.tool, args_hash = excluded.args_hash, result = excluded.result,
			created_at = excluded.created_at, expires_at = excluded.expires_at`,
			p.agentID, p.key, p.r.Tool, p.r.ArgsHash, string(p.r.Result), p.now, p.r.ExpiresAt.UnixMilli())
		if err != nil {
			return err
		}
	}
	return nil
}
