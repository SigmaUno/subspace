package postgres

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS connected_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS idle_since BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS stopped_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS process TEXT NOT NULL DEFAULT '';
ALTER TABLE spawns ADD COLUMN IF NOT EXISTS process_checked_at BIGINT NOT NULL DEFAULT 0;`)
}

func (s *Store) SpawnLifecycles(ctx context.Context) (map[string]store.SpawnLifecycle, error) {
	rows, err := s.rdb.QueryContext(ctx,
		`SELECT id, connected_at, idle_since, stopped_at, stop_reason, process, process_checked_at FROM spawns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]store.SpawnLifecycle{}
	for rows.Next() {
		var (
			l                                 store.SpawnLifecycle
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

func (s *Store) SaveSpawnLifecycle(ctx context.Context, l store.SpawnLifecycle, sp *model.Spawn, ev *model.AuditEvent) error {
	return s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		if sp != nil {
			if err := t.UpdateSpawn(sp); err != nil {
				return err
			}
		}
		err := t.update(`UPDATE spawns SET connected_at=$2, idle_since=$3, stopped_at=$4, stop_reason=$5, process=$6,
  process_checked_at=$7 WHERE id=$1`, l.SpawnID, store.Millis(l.ConnectedAt), store.Millis(l.IdleSince), store.Millis(l.StoppedAt), l.StopReason, l.Process,
			store.Millis(l.ProcessCheckedAt))
		if err != nil {
			return err
		}
		if ev != nil {
			return t.InsertAudit(ev)
		}
		return nil
	})
}
