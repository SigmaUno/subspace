package board

import (
	"context"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	QualityApprove = "approve"
	QualitySubmit  = "submit"
	QualityPromote = "promote"
	QualityMerge   = "merge"
	QualityStatus  = "status"

	CodeGatesFailed = "gates_failed"
	ToolGatesPassed = "gates_passed"
)

var gatesActor = model.Actor{ID: "system", Role: "system"}

type QualityCheck struct {
	Ctx      context.Context
	Op       string
	Actor    model.Actor
	Task     *model.Task
	Brief    *model.Brief
	Comments string
	Now      time.Time
}

type QualityGate func(tx store.Tx, c QualityCheck) error

func (b *Board) SetQualityGate(g QualityGate) {
	b.mu.Lock()
	b.quality = g
	b.mu.Unlock()
}

func (b *Board) checkQuality(tx store.Tx, c QualityCheck) error {
	b.mu.Lock()
	g := b.quality
	b.mu.Unlock()
	if g == nil || !c.Brief.QualityGates.Enabled() {
		return nil
	}
	c.Now = b.now()
	return g(tx, c)
}

func (b *Board) CheckQuality(ctx context.Context, taskID string) error {
	return b.read(ctx, func(tx store.Tx) error {
		t, err := tx.GetTask(taskID)
		if err != nil {
			return err
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		return b.checkQuality(tx, QualityCheck{Ctx: ctx, Op: QualityMerge, Actor: model.Owner, Task: t, Brief: br})
	})
}

func (b *Board) PromoteGated(ctx context.Context, taskID string) (bool, error) {
	promoted := false
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		promoted = false
		t, err := tx.GetTask(taskID)
		if err != nil {
			return err
		}
		if t.Status != model.StatusSubmitted {
			return nil
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		if needsReview(br, t) || !br.QualityGates.Enabled() {
			return nil
		}
		if err := b.checkQuality(tx, QualityCheck{Ctx: ctx, Op: QualityPromote, Actor: gatesActor, Task: t, Brief: br}); err != nil {
			if _, ok := gatesFailed(err); ok {
				return nil
			}
			return err
		}
		promoted = true
		return b.statusChange(tx, w, gatesActor, ToolGatesPassed, t, model.StatusApproved, "quality gates passed")
	})
	return promoted, err
}

func gatesFailed(err error) (*model.Error, bool) {
	var e *model.Error
	if errors.As(err, &e) && e.Code == CodeGatesFailed {
		return e, true
	}
	return nil, false
}

func (b *Board) postHeld(tx store.Tx, w *wctx, t *model.Task, ge *model.Error) error {
	return w.post(tx, model.ThreadTask, t.ID, gatesActor, model.MsgSystem, "Approval of "+t.ID+" held: "+ge.Message)
}

func (b *Board) holdForGates(tx store.Tx, w *wctx, a model.Actor, t *model.Task, ge *model.Error) error {
	note := "approval held: " + ge.Message
	if t.Status == model.StatusSubmitted {
		if err := b.statusChange(tx, w, a, "review", t, model.StatusInReview, note); err != nil {
			return err
		}
	} else {
		if err := b.saveTask(tx, w, t); err != nil {
			return err
		}
		if err := w.audit(tx, a, "review", t.ID, nil, map[string]any{"verdict": "approve", "gates_failed": ge.Details, "state": replayState(t)}, note); err != nil {
			return err
		}
	}
	return b.postHeld(tx, w, t, ge)
}

func checkQualityGates(q model.QualityGates) error {
	if q.MinTestsPassed < 0 || q.MinTestsPassed > 1000000 {
		return model.BadRequest("quality_gates.min_tests_passed must be between 0 and 1000000")
	}
	if q.MinTestsPassed > 0 && !q.Tests {
		return model.BadRequest("quality_gates.min_tests_passed needs quality_gates.tests")
	}
	if q.MaxPRAgeHours < 0 || q.MaxPRAgeHours > 8760 {
		return model.BadRequest("quality_gates.max_pr_age_hours must be between 0 and 8760")
	}
	return nil
}
