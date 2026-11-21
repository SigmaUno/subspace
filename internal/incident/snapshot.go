package incident

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/SigmaUno/subspace/internal/store"
)

const snapshotRows = 1_000_000

type line struct {
	Kind string `json:"kind"`
	Data any    `json:"data"`
}

func (s *Service) snapshot(ctx context.Context, inc *Incident) (string, error) {
	if s.o.Dir == "" {
		return "", errors.New("no data directory configured for snapshots")
	}
	dir := filepath.Join(s.o.Dir, "incidents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, inc.ID+".*.tmp")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	paused, why := s.o.Control.Paused()
	err = s.o.Store.View(ctx, func(tx store.Tx) error {
		head, err := tx.AuditHead(math.MaxInt64)
		if err != nil {
			return err
		}
		meta := map[string]any{"incident": inc.ID, "taken_at": s.now(), "paused": paused, "pause_reason": why, "audit_head": head}
		if err := enc.Encode(line{"meta", meta}); err != nil {
			return err
		}
		briefs, err := tx.ListBriefs(store.BriefFilter{})
		if err != nil {
			return err
		}
		for _, b := range briefs {
			if err := enc.Encode(line{"brief", b}); err != nil {
				return err
			}
		}
		agents, err := tx.ListAgents(store.AgentFilter{Limit: snapshotRows})
		if err != nil {
			return err
		}
		for _, a := range agents {
			if err := enc.Encode(line{"agent", a}); err != nil {
				return err
			}
		}
		tasks, err := tx.ListTasks(store.TaskFilter{Limit: snapshotRows})
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if err := enc.Encode(line{"task", t}); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	path := filepath.Join(dir, inc.ID+".ndjson")
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	return path, nil
}
