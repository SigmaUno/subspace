package board

import (
	"context"
	"errors"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type UsageInput struct {
	TaskID       string
	Provider     string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostEUR      float64
}

type UsageResult struct {
	Usage *store.Usage `json:"usage"`
	Task  *model.Task  `json:"task"`
}

var errNoUsageStore = errors.New("store does not support usage tracking")

func (b *Board) ReportUsage(ctx context.Context, a model.Actor, in UsageInput) (*UsageResult, error) {
	if in.InputTokens < 0 || in.OutputTokens < 0 || in.CostEUR < 0 {
		return nil, model.BadRequest("token counts and cost must be >= 0")
	}
	if in.Provider == "" || in.Model == "" {
		return nil, model.BadRequest("provider and model are required")
	}
	res := &UsageResult{}
	exceeded := false
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		ut, ok := tx.(store.UsageTx)
		if !ok {
			return errNoUsageStore
		}
		res.Task, exceeded = nil, false
		var t *model.Task
		if in.TaskID != "" {
			var err error
			t, err = tx.GetTask(in.TaskID)
			if err != nil || deref(t.Assignee) != a.ID {
				return model.Errf("not_assigned", "task %s is not assigned to you", in.TaskID)
			}
		}
		u := &store.Usage{AgentID: a.ID, TaskID: in.TaskID, Provider: in.Provider, Model: in.Model,
			InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CostEUR: in.CostEUR, At: b.now()}
		if err := ut.InsertUsage(u); err != nil {
			return err
		}
		res.Usage = u
		after := map[string]any{"provider": in.Provider, "model": in.Model, "input_tokens": in.InputTokens,
			"output_tokens": in.OutputTokens, "cost_eur": in.CostEUR}
		if t == nil {
			return w.audit(tx, a, "report_usage", "", nil, after, "")
		}
		t.CostUsed += in.CostEUR
		t.UpdatedAt = u.At
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		res.Task = t
		after["cost_used"] = t.CostUsed
		if err := w.audit(tx, a, "report_usage", t.ID, nil, after, ""); err != nil {
			return err
		}
		if t.Status == model.StatusInProgress {
			br, err := tx.GetBrief(t.BriefID)
			if err != nil {
				return err
			}
			if berr := b.checkBudget(tx, t, br); berr != nil {
				exceeded = true
				return b.block(tx, w, a, t, "budget_exceeded: "+berr.Error())
			}
		}
		w.task(t, false)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if exceeded {
		return res, model.Errf("budget_exceeded", "budget exceeded: task blocked")
	}
	return res, nil
}

type ProviderErrorInput struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Code     string `json:"code"`
}

const maxProviderErrorField = 128

func (b *Board) ReportProviderError(ctx context.Context, a model.Actor, in ProviderErrorInput) (*store.ProviderError, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.Model, in.Code = strings.TrimSpace(in.Model), strings.TrimSpace(in.Code)
	if in.Provider == "" || in.Model == "" || in.Code == "" {
		return nil, model.BadRequest("provider, model and code are required")
	}
	if len(in.Provider) > maxProviderErrorField || len(in.Model) > maxProviderErrorField || len(in.Code) > maxProviderErrorField {
		return nil, model.BadRequest("provider, model and code must be at most %d characters", maxProviderErrorField)
	}
	e := &store.ProviderError{AgentID: a.ID, Provider: in.Provider, Model: in.Model, Code: in.Code}
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		ut, ok := tx.(store.UsageTx)
		if !ok {
			return errNoUsageStore
		}
		e.At = b.now()
		if err := ut.InsertProviderError(e); err != nil {
			return err
		}
		return w.audit(tx, a, "report_provider_error", "", nil, map[string]any{"provider": in.Provider, "model": in.Model, "code": in.Code}, "")
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}
