package sqlite

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterColumn("spawns", "connected_at", "INTEGER NOT NULL DEFAULT 0")
	RegisterColumn("spawns", "idle_since", "INTEGER NOT NULL DEFAULT 0")
	RegisterColumn("spawns", "stopped_at", "INTEGER NOT NULL DEFAULT 0")
	RegisterColumn("spawns", "stop_reason", "TEXT NOT NULL DEFAULT ''")
	RegisterColumn("spawns", "process", "TEXT NOT NULL DEFAULT ''")
	RegisterColumn("spawns", "process_checked_at", "INTEGER NOT NULL DEFAULT 0")
}

type SpawnLifecycle = store.SpawnLifecycle

func (s *Store) SpawnLifecycles(ctx context.Context) (map[string]SpawnLifecycle, error) {
	rows, err := s.rdb.QueryContext(ctx,
		`SELECT id, connected_at, idle_since, stopped_at, stop_reason, process, process_checked_at FROM spawns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]SpawnLifecycle{}
	for rows.Next() {
		var (
			l                                 SpawnLifecycle
			connected, idle, stopped, checked int64
		)
		if err := rows.Scan(&l.SpawnID, &connected, &idle, &stopped, &l.StopReason, &l.Process, &checked); err != nil {
			return nil, err
		}
		l.ConnectedAt, l.IdleSince, l.StoppedAt, l.ProcessCheckedAt = store.FromMillis(connected), store.FromMillis(idle), store.FromMillis(stopped), store.FromMillis(checked)
		out[l.SpawnID] = l
	}
	return out, rows.Err()
}

func (s *Store) SaveSpawnLifecycle(ctx context.Context, l SpawnLifecycle, sp *model.Spawn, ev *model.AuditEvent) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		tt := t.(*tx)
		if sp != nil {
			if err := tt.UpdateSpawn(sp); err != nil {
				return err
			}
		}
		res, err := tt.q.ExecContext(tt.ctx,
			`UPDATE spawns SET connected_at=?, idle_since=?, stopped_at=?, stop_reason=?, process=?, process_checked_at=? WHERE id=?`,
			store.Millis(l.ConnectedAt), store.Millis(l.IdleSince), store.Millis(l.StoppedAt), l.StopReason, l.Process,
			store.Millis(l.ProcessCheckedAt), l.SpawnID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return store.ErrNotFound
		}
		if ev != nil {
			return tt.InsertAudit(ev)
		}
		return nil
	})
}
