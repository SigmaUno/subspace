package board

import (
	"fmt"
	"strings"
	"sync"

	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type conflictConfig struct {
	mu    sync.RWMutex
	mode  conflict.Mode
	paths func(briefID, componentID string) []string
}

func (b *Board) SetConflictMode(m conflict.Mode) {
	b.cc.mu.Lock()
	b.cc.mode = m
	b.cc.mu.Unlock()
}

func (b *Board) ConflictMode() conflict.Mode {
	b.cc.mu.RLock()
	defer b.cc.mu.RUnlock()
	if b.cc.mode == "" {
		return conflict.Warn
	}
	return b.cc.mode
}

func (b *Board) SetComponentPaths(f func(briefID, componentID string) []string) {
	b.cc.mu.Lock()
	b.cc.paths = f
	b.cc.mu.Unlock()
}

func (b *Board) taskPaths(briefID string, in TaskInput) (model.Paths, error) {
	if len(in.Paths) > 0 {
		ps, err := conflict.NormalizeAll(in.Paths)
		if err != nil {
			return nil, model.BadRequest("paths: %v", err)
		}
		return ps, nil
	}
	b.cc.mu.RLock()
	f := b.cc.paths
	b.cc.mu.RUnlock()
	if in.ComponentID == "" || f == nil {
		return nil, nil
	}
	var out model.Paths
	for _, p := range f(briefID, in.ComponentID) {
		if n, err := conflict.Normalize(p); err == nil && !contains(out, n) && len(out) < conflict.MaxPaths {
			out = append(out, n)
		}
	}
	return out, nil
}

func needsReview(br *model.Brief, t *model.Task) bool {
	if !br.CrossReview {
		return false
	}
	if len(br.RiskyPaths) == 0 || len(t.Paths) == 0 {
		return true
	}
	hit, err := conflict.Overlap(t.Paths, br.RiskyPaths)
	return hit || err != nil
}

func (b *Board) pathConflicts(tx store.Tx, t *model.Task, agentID string) ([]*model.Task, error) {
	if len(t.Paths) == 0 || b.ConflictMode() == conflict.Off {
		return nil, nil
	}
	held, err := tx.ListTasks(store.TaskFilter{Status: conflict.Held, Limit: 100000})
	if err != nil {
		return nil, err
	}
	return conflict.Conflicts(t, agentID, held), nil
}

func conflictIDs(ts []*model.Task) string {
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	return strings.Join(ids, ", ")
}

func conflictNote(t *model.Task, hits []*model.Task) string {
	parts := make([]string, len(hits))
	for i, h := range hits {
		parts[i] = fmt.Sprintf("%s (%s: %s)", h.ID, deref(h.Assignee), strings.Join(h.Paths, ", "))
	}
	s := fmt.Sprintf("Path overlap: %s [%s] overlaps %s", t.ID, strings.Join(t.Paths, ", "), strings.Join(parts, "; "))
	if len(s) > maxConflictNote {
		s = strings.ToValidUTF8(s[:maxConflictNote], "") + "…"
	}
	return s
}

const maxConflictNote = 4 << 10
