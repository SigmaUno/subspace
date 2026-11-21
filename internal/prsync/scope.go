package prsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/scope"
)

type ScopeStatus struct {
	TaskID        string     `json:"task_id"`
	MergeEligible bool       `json:"merge_eligible"`
	CheckedAt     *time.Time `json:"checked_at"`
	scope.Report
}

func scoreTask(t *model.Task, h *host, d *scope.Diff) *scope.Report {
	if d == nil {
		return nil
	}
	in := scope.Input{Diff: *d, Paths: t.Paths, Branch: t.Branch, MaxMinutes: t.Budget.MaxMinutes}
	if len(h.authors) > 0 {
		in.AllowedAuthors = slices.Clone(h.authors)
		if t.Assignee != nil {
			in.AllowedAuthors = append(in.AllowedAuthors, *t.Assignee)
		}
	}
	r := scope.Score(in)
	return &r
}

func summary(st Status) string {
	out := fmt.Sprintf("%s#%d approved by reviewer, checks green, mergeable", st.Repo, st.Number)
	if st.Scope == nil || len(st.Scope.Findings) == 0 {
		return out
	}
	msgs := make([]string, len(st.Scope.Findings))
	for i, f := range st.Scope.Findings {
		msgs[i] = f.Message
	}
	return out + "; scope: " + strings.Join(msgs, "; ")
}

func (s *Service) status(ctx context.Context, taskID string) (*Status, bool, error) {
	raw, ok, err := s.st.PRStatus(ctx, taskID)
	if err != nil || !ok {
		return nil, false, err
	}
	var out Status
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

func (s *Service) Scope(ctx context.Context, taskID string) (*ScopeStatus, error) {
	if _, err := s.b.GetTask(ctx, model.Owner, taskID); err != nil {
		return nil, err
	}
	st, ok, err := s.status(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !ok || st.Scope == nil {
		return nil, model.NotFound("no scope report for task %s", taskID)
	}
	return &ScopeStatus{TaskID: taskID, MergeEligible: !st.Scope.Blocking(), CheckedAt: st.CheckedAt, Report: *st.Scope}, nil
}

func (s *Service) Findings(ctx context.Context, taskIDs []string) (map[string][]scope.Finding, error) {
	out := map[string][]scope.Finding{}
	for _, id := range taskIDs {
		st, ok, err := s.status(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok && st.Scope != nil && len(st.Scope.Findings) > 0 {
			out[id] = st.Scope.Findings
		}
	}
	return out, nil
}

func (s *Service) ScopeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}/scope", func(w http.ResponseWriter, r *http.Request) {
		sc, err := s.Scope(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sc)
	})
	return mux
}
