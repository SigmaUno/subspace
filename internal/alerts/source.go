package alerts

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const auditPage = 1000

var activeStatuses = []string{model.StatusReady, model.StatusInProgress, model.StatusSubmitted}

type StoreSource struct {
	Store          store.Store
	DBPath         string
	BackupsEnabled bool
	Backups        func(ctx context.Context) ([]backup.Info, error)
	Paused         func() bool
	SSE            *SSEMonitor
	Started        time.Time

	mu      sync.Mutex
	cursors map[string]int64
	lastAt  time.Time
}

var assignTools = []string{"assign", "reassign"}

func (s *StoreSource) Snapshot(ctx context.Context, now time.Time) (Snapshot, error) {
	snap := Snapshot{Now: now, BackupsEnabled: s.BackupsEnabled}
	err := s.Store.View(ctx, func(tx store.Tx) error {
		var err error
		if snap.Tasks, err = tx.ListTasks(store.TaskFilter{Status: activeStatuses, Limit: 1 << 20}); err != nil {
			return err
		}
		all, err := tx.ListTasks(store.TaskFilter{Limit: 1 << 20})
		if err != nil {
			return err
		}
		for _, t := range all {
			if t.UpdatedAt.After(snap.LastTaskChange) {
				snap.LastTaskChange = t.UpdatedAt
			}
		}
		if snap.Agents, err = tx.ListAgents(store.AgentFilter{Limit: 1 << 20}); err != nil {
			return err
		}
		snap.LastAssignment, err = s.lastAssignment(tx)
		return err
	})
	if err != nil {
		return snap, err
	}
	if s.Paused != nil {
		snap.Paused = s.Paused()
	}
	s.files(ctx, &snap)
	if s.SSE != nil {
		snap.SSEStreams, snap.SSELagged = s.SSE.Take()
	}
	return snap, nil
}

func (s *StoreSource) lastAssignment(tx store.Tx) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursors == nil {
		s.cursors = map[string]int64{}
	}
	for _, tool := range assignTools {
		for {
			evs, err := tx.ListAudit(store.AuditFilter{Tool: tool, SinceID: s.cursors[tool], Limit: auditPage})
			if err != nil {
				return time.Time{}, err
			}
			for _, e := range evs {
				s.cursors[tool] = e.ID
				if e.TS.After(s.lastAt) {
					s.lastAt = e.TS
				}
			}
			if len(evs) < auditPage {
				break
			}
		}
	}
	return s.lastAt, nil
}

func (s *StoreSource) files(ctx context.Context, snap *Snapshot) {
	if fi, err := os.Stat(s.DBPath); err == nil {
		snap.DBBytes = fi.Size()
	}
	if fi, err := os.Stat(s.DBPath + "-wal"); err == nil {
		snap.WALBytes = fi.Size()
	}
	if free, err := diskFree(filepath.Dir(s.DBPath)); err == nil {
		snap.DiskKnown, snap.DiskFree = true, free
	}
	if !s.BackupsEnabled || s.Backups == nil {
		return
	}
	snap.LastBackup = s.Started
	list, err := s.Backups(ctx)
	if err != nil || len(list) == 0 {
		return
	}
	snap.LastBackup = list[0].CreatedAt
	snap.BackupFailing = !list[0].Verified
}
