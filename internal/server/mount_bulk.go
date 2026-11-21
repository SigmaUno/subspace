package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	bulkMaxIDs  = 500
	bulkMaxBody = 1 << 20
)

type bulkHandler struct {
	b   *board.Board
	log *slog.Logger
}

type bulkRequest struct {
	IDs    []string        `json:"ids"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

type bulkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bulkResult struct {
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Error   *bulkError     `json:"error,omitempty"`
	Connect *board.Connect `json:"connect,omitempty"`
}

type bulkParams struct {
	Reason     string   `json:"reason"`
	Note       string   `json:"note"`
	Status     string   `json:"status"`
	AgentID    string   `json:"agent_id"`
	As         string   `json:"as"`
	MaxMinutes *int     `json:"max_minutes"`
	MaxCostEUR *float64 `json:"max_cost_eur"`
}

func init() {
	registerMount(func(d Deps) {
		h := &bulkHandler{b: d.Board, log: d.Log}
		d.API.Mount(d.Mux, "POST /api/v1/tasks/bulk", http.HandlerFunc(h.tasks))
		d.API.Mount(d.Mux, "POST /api/v1/agents/bulk-action", http.HandlerFunc(h.agents))
	})
}

func (h *bulkHandler) tasks(w http.ResponseWriter, r *http.Request) {
	req, p, ok := h.parse(w, r)
	if !ok {
		return
	}
	var run func(ctx context.Context, id string) bulkResult
	switch req.Action {
	case "cancel":
		run = func(ctx context.Context, id string) bulkResult {
			_, err := h.b.Cancel(ctx, id, p.Reason)
			return h.result(id, err)
		}
	case "reopen":
		run = func(ctx context.Context, id string) bulkResult {
			_, err := h.b.Reopen(ctx, id, p.Status, p.Note)
			return h.result(id, err)
		}
	case "unblock":
		run = func(ctx context.Context, id string) bulkResult {
			t, err := h.b.GetTask(ctx, model.Owner, id)
			if err == nil && t.Status != model.StatusBlocked && t.Status != model.StatusNeedsOwner {
				err = model.Conflict("task %s is %s, not blocked", id, t.Status)
			}
			if err == nil {
				_, err = h.b.Reopen(ctx, id, p.Status, p.Note)
			}
			return h.result(id, err)
		}
	case "set_budget":
		if p.MaxMinutes == nil && p.MaxCostEUR == nil || p.MaxMinutes != nil && *p.MaxMinutes < 0 || p.MaxCostEUR != nil && *p.MaxCostEUR < 0 {
			httpx.WriteError(w, model.BadRequest("set_budget needs non-negative max_minutes and/or max_cost_eur"))
			return
		}
		run = func(ctx context.Context, id string) bulkResult {
			t, err := h.b.GetTask(ctx, model.Owner, id)
			if err == nil {
				bud := t.Budget
				if p.MaxMinutes != nil {
					bud.MaxMinutes = *p.MaxMinutes
				}
				if p.MaxCostEUR != nil {
					bud.MaxCostEUR = *p.MaxCostEUR
				}
				_, err = h.b.SetTaskBudget(ctx, id, bud)
			}
			return h.result(id, err)
		}
	case "assign", "reassign":
		if p.AgentID == "" {
			httpx.WriteError(w, model.BadRequest("%s needs agent_id", req.Action))
			return
		}
		run = func(ctx context.Context, id string) bulkResult {
			var err error
			if req.Action == "assign" {
				_, err = h.b.Assign(ctx, model.Owner, id, p.AgentID, p.As)
			} else {
				_, err = h.b.Reassign(ctx, model.Owner, id, p.AgentID, p.Reason)
			}
			return h.result(id, err)
		}
	default:
		httpx.WriteError(w, model.BadRequest("action must be cancel, reopen, set_budget, assign, reassign or unblock"))
		return
	}
	h.respond(w, r.Context(), req, run)
}

func (h *bulkHandler) agents(w http.ResponseWriter, r *http.Request) {
	req, p, ok := h.parse(w, r)
	if !ok {
		return
	}
	var run func(ctx context.Context, id string) bulkResult
	switch req.Action {
	case "pause":
		run = func(ctx context.Context, id string) bulkResult {
			a, err := h.b.GetAgent(ctx, id)
			if err == nil && a.Status == model.AgentRevoked {
				err = model.Conflict("agent %s is revoked", id)
			}
			if err == nil {
				reason := p.Reason
				if reason == "" {
					reason = "paused by owner"
				}
				err = h.b.PauseAgent(ctx, id, reason)
			}
			return h.result(id, err)
		}
	case "resume":
		run = func(ctx context.Context, id string) bulkResult {
			_, err := h.b.ResumeAgent(ctx, id)
			return h.result(id, err)
		}
	case "revoke":
		run = func(ctx context.Context, id string) bulkResult {
			a, err := h.b.GetAgent(ctx, id)
			if err == nil && a.Status == model.AgentRevoked {
				err = model.Conflict("agent %s is already revoked", id)
			}
			if err == nil {
				_, err = h.b.RevokeAgent(ctx, id, p.Reason)
			}
			return h.result(id, err)
		}
	case "rotate-token":
		run = func(ctx context.Context, id string) bulkResult {
			reg, err := h.b.RotateToken(ctx, id)
			res := h.result(id, err)
			if err == nil {
				res.Connect = &reg.Connect
			}
			return res
		}
	default:
		httpx.WriteError(w, model.BadRequest("action must be pause, resume, revoke or rotate-token"))
		return
	}
	h.respond(w, r.Context(), req, run)
}

func (h *bulkHandler) respond(w http.ResponseWriter, ctx context.Context, req bulkRequest, run func(context.Context, string) bulkResult) {
	results := make([]bulkResult, 0, len(req.IDs))
	failed := 0
	for _, id := range req.IDs {
		res := run(ctx, id)
		if !res.OK {
			failed++
		}
		results = append(results, res)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"action": req.Action, "results": results,
		"succeeded": len(results) - failed, "failed": failed,
	})
}

func (h *bulkHandler) parse(w http.ResponseWriter, r *http.Request) (bulkRequest, bulkParams, bool) {
	var req bulkRequest
	var p bulkParams
	raw, err := httpx.Body(w, r, bulkMaxBody)
	if err == nil {
		err = httpx.Unmarshal(raw, &req)
	}
	if err != nil {
		httpx.WriteError(w, err)
		return req, p, false
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			httpx.WriteError(w, model.BadRequest("invalid params"))
			return req, p, false
		}
	}
	switch n := len(req.IDs); {
	case n == 0:
		httpx.WriteError(w, model.BadRequest("ids must not be empty"))
		return req, p, false
	case n > bulkMaxIDs:
		httpx.WriteError(w, model.BadRequest("at most %d ids per request", bulkMaxIDs))
		return req, p, false
	}
	seen := make(map[string]bool, len(req.IDs))
	ids := req.IDs[:0]
	for _, id := range req.IDs {
		if strings.TrimSpace(id) == "" {
			httpx.WriteError(w, model.BadRequest("ids must not contain empty values"))
			return req, p, false
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	req.IDs = ids
	return req, p, true
}

func (h *bulkHandler) result(id string, err error) bulkResult {
	if err == nil {
		return bulkResult{ID: id, OK: true}
	}
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("bulk", "id", id, "err", err)
	}
	return bulkResult{ID: id, Error: &bulkError{Code: e.Code, Message: e.Message}}
}
