package client

import (
	"context"

	"github.com/SigmaUno/subspace/internal/helpreq"
)

func (m *MCP) helpRequest(ctx context.Context, tool string, args any) (*helpreq.Request, error) {
	var out struct {
		Request *helpreq.Request `json:"request"`
	}
	if err := m.Call(ctx, tool, args, &out); err != nil {
		return nil, err
	}
	return out.Request, nil
}

func (m *MCP) RequestHelp(ctx context.Context, taskID, kind, details string) (*helpreq.Request, error) {
	return m.helpRequest(ctx, "request_help", map[string]any{"task_id": taskID, "kind": kind, "details": details})
}

func (m *MCP) ReportBlocker(ctx context.Context, taskID, reason, needs string) (*helpreq.Request, error) {
	return m.helpRequest(ctx, "report_blocker", map[string]any{"task_id": taskID, "reason": reason, "needs": needs})
}

func (m *MCP) SuggestSplit(ctx context.Context, taskID string, proposed []helpreq.Proposed) (*helpreq.Request, error) {
	return m.helpRequest(ctx, "suggest_split", map[string]any{"task_id": taskID, "proposed_tasks": proposed})
}

func (m *MCP) ListHelpRequests(ctx context.Context, f helpreq.Filter) ([]*helpreq.Request, error) {
	var out struct {
		Requests []*helpreq.Request `json:"requests"`
	}
	args := map[string]any{"status": f.Status, "brief_id": f.BriefID, "task_id": f.TaskID, "limit": f.Limit}
	if err := m.Call(ctx, "list_help_requests", args, &out); err != nil {
		return nil, err
	}
	return out.Requests, nil
}

func (m *MCP) AcceptSplit(ctx context.Context, requestID string) (*helpreq.SplitResult, error) {
	var out helpreq.SplitResult
	if err := m.Call(ctx, "accept_split", map[string]any{"request_id": requestID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
