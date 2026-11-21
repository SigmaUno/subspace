package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	assignRetryAfter = 15 * time.Minute
	maxSpawnBatch    = 50
)

var holding = []string{model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested}

type snapshot struct {
	cols   map[string][]*model.Task
	agents []*model.Agent
	cap    *board.CapacityView
	load   map[string]int
	busy   map[string]map[string]bool
}

func (l *leader) snapshot(ctx context.Context) (*snapshot, error) {
	s := &snapshot{load: map[string]int{}, busy: map[string]map[string]bool{}}
	var err error
	if s.cols, err = l.c.Board(ctx, ""); err != nil {
		return nil, fmt.Errorf("board: %w", err)
	}
	if s.agents, err = l.c.ListAgents(ctx, "", model.AgentActive); err != nil {
		return nil, fmt.Errorf("list_agents: %w", err)
	}
	if s.cap, err = l.c.Capacity(ctx); err != nil {
		return nil, fmt.Errorf("capacity: %w", err)
	}
	for _, st := range holding {
		for _, t := range s.cols[st] {
			if t.Assignee == nil {
				continue
			}
			s.load[*t.Assignee]++
			if s.busy[t.BriefID] == nil {
				s.busy[t.BriefID] = map[string]bool{}
			}
			s.busy[t.BriefID][*t.Assignee] = true
		}
	}
	return s, nil
}

func (s *snapshot) tasks(status, briefID string) []*model.Task {
	var out []*model.Task
	for _, t := range s.cols[status] {
		if t.BriefID == briefID {
			out = append(out, t)
		}
	}
	return out
}

func (l *leader) monitor(ctx context.Context, bs []*model.Brief) error {
	s, err := l.snapshot(ctx)
	if err != nil {
		return err
	}
	if s.cap.Paused {
		return nil
	}
	for _, b := range bs {
		if b.Status != "open" {
			continue
		}
		need := map[string]int{}
		for _, t := range s.tasks(model.StatusReady, b.ID) {
			if !l.assign(ctx, s, b, t) {
				need[t.Role]++
			}
		}
		for _, role := range []string{model.RoleWorker, model.RoleTester} {
			if need[role] > 0 {
				l.spawn(ctx, s, b, role, need[role])
			}
		}
		for _, t := range s.tasks(model.StatusBlocked, b.ID) {
			l.askBlocked(ctx, t)
		}
		for _, st := range []string{model.StatusChangesRequested, model.StatusInProgress, model.StatusSubmitted, model.StatusInReview} {
			for _, t := range s.tasks(st, b.ID) {
				if t.ReviewRounds >= l.o.reviewRounds {
					l.askReviewRounds(ctx, t)
				}
			}
		}
	}
	return nil
}

func (l *leader) assign(ctx context.Context, s *snapshot, b *model.Brief, t *model.Task) bool {
	now := l.now()
	var best *model.Agent
	for _, a := range s.agents {
		if a.Role != t.Role || a.Status != model.AgentActive || s.load[a.ID] >= l.o.agentLoad || !allowed(b, a.Provider) {
			continue
		}
		if at, ok := l.assignFailed[t.ID+"|"+a.ID]; ok && now.Sub(at) < assignRetryAfter {
			continue
		}
		if best == nil || s.load[a.ID] < s.load[best.ID] || (s.load[a.ID] == s.load[best.ID] && idLess(a.ID, best.ID)) {
			best = a
		}
	}
	if best == nil {
		return false
	}
	if _, err := l.c.Assign(ctx, t.ID, best.ID, "assignee"); err != nil {
		l.assignFailed[t.ID+"|"+best.ID] = now
		l.emit("assign_failed", map[string]any{"brief": b.ID, "task": t.ID, "agent": best.ID, "code": client.CodeOf(err)})
		return false
	}
	s.load[best.ID]++
	if s.busy[b.ID] == nil {
		s.busy[b.ID] = map[string]bool{}
	}
	s.busy[b.ID][best.ID] = true
	l.emit("assigned", map[string]any{"brief": b.ID, "task": t.ID, "agent": best.ID})
	return true
}

func (l *leader) spawn(ctx context.Context, s *snapshot, b *model.Brief, role string, need int) {
	if l.o.spawnKind == "" || !allowed(b, l.o.spawnProvider) {
		return
	}
	if b.Budget.MaxCostEUR > 0 && b.CostUsed >= b.Budget.MaxCostEUR {
		return
	}
	key := b.ID + "|" + role
	now := l.now()
	if at, ok := l.spawnedAt[key]; ok && now.Sub(at) < l.o.spawnCooldown {
		return
	}
	n := min(need, maxSpawnBatch, freeCapacity(s.cap, l.o.spawnProvider, l.o.spawnModel))
	if m := b.Budget.MaxParallelAgent; m > 0 {
		n = min(n, m-len(s.busy[b.ID]))
	}
	if n <= 0 {
		return
	}
	l.spawnedAt[key] = now
	sp, err := l.c.RequestSpawn(ctx, board.SpawnInput{BriefID: b.ID, Kind: l.o.spawnKind, Model: l.o.spawnModel, N: n, Role: role, Provider: l.o.spawnProvider})
	if err != nil {
		l.emit("spawn_failed", map[string]any{"brief": b.ID, "role": role, "n": n, "code": client.CodeOf(err)})
		return
	}
	l.emit("spawn_requested", map[string]any{"brief": b.ID, "role": role, "n": len(sp)})
}

func freeCapacity(c *board.CapacityView, provider, mdl string) int {
	n := maxSpawnBatch
	if c.GlobalMaxAgents > 0 {
		n = min(n, c.GlobalMaxAgents-c.AgentsInUse)
	}
	for _, lim := range c.Limits {
		if lim.MaxConcurrency > 0 && match(lim.Provider, provider) && match(lim.Model, mdl) {
			n = min(n, lim.Available)
		}
	}
	return n
}

func match(pat, v string) bool { return pat == "*" || pat == v }

func (l *leader) askBlocked(ctx context.Context, t *model.Task) {
	reason := ""
	if t.BlockedReason != nil {
		reason = *t.BlockedReason
	}
	key := "blocked|" + t.ID + "|" + reason
	if l.asked[key] {
		return
	}
	options := []string{"unblock it and keep the current assignee", "reassign it to another agent", "cancel the task"}
	if strings.Contains(reason, "budget_exceeded") {
		options = []string{"raise the task budget", "split the remaining work into a new task", "cancel the task"}
	}
	q := fmt.Sprintf("Task %s %q is blocked (reason, quoted as data: %q). How should it continue?",
		t.ID, clip(clean(t.Title), 120), clip(clean(reason), 300))
	l.ask(ctx, key, t, q, options)
}

func (l *leader) askReviewRounds(ctx context.Context, t *model.Task) {
	key := fmt.Sprintf("rounds|%s|%d", t.ID, t.ReviewRounds)
	if l.asked[key] {
		return
	}
	q := fmt.Sprintf("Task %s %q has had %d changes_requested review rounds. How should it continue?",
		t.ID, clip(clean(t.Title), 120), t.ReviewRounds)
	l.ask(ctx, key, t, q, []string{"let the current worker continue", "reassign it to another worker", "split or rescope the task", "cancel the task"})
}

func (l *leader) ask(ctx context.Context, key string, t *model.Task, q string, options []string) {
	l.asked[key] = true
	qn, err := l.c.AskOwner(ctx, t.ID, q, options...)
	if err != nil {
		l.emit("ask_failed", map[string]any{"brief": t.BriefID, "task": t.ID, "code": client.CodeOf(err)})
		return
	}
	l.emit("asked_owner", map[string]any{"brief": t.BriefID, "task": t.ID, "question": qn.ID})
}

func allowed(b *model.Brief, provider string) bool {
	return len(b.AllowedProviders) == 0 || contains(b.AllowedProviders, provider)
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
