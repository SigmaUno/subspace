package prsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/scope"
)

const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"

	ChecksSuccess = "success"
	ChecksFailure = "failure"
	ChecksPending = "pending"
	ChecksNone    = "none"

	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	DecisionNone           = "none"

	EventPR        = "task.pr"
	KindMergeReady = "merge-ready"
)

type Review struct {
	User  string `json:"user"`
	State string `json:"state"`
}

type Status struct {
	TaskID         string        `json:"task_id"`
	BriefID        string        `json:"brief_id"`
	URL            string        `json:"url"`
	Host           string        `json:"host"`
	Repo           string        `json:"repo"`
	Number         int64         `json:"number"`
	State          string        `json:"state"`
	Draft          bool          `json:"draft"`
	Checks         string        `json:"checks"`
	Mergeable      *bool         `json:"mergeable"`
	HeadSHA        string        `json:"head_sha"`
	Reviews        []Review      `json:"reviews"`
	ReviewDecision string        `json:"review_decision"`
	MergeReady     bool          `json:"merge_ready"`
	Error          *string       `json:"error"`
	Diff           *scope.Diff   `json:"diff,omitempty"`
	Scope          *scope.Report `json:"scope,omitempty"`
	CheckedAt      *time.Time    `json:"checked_at"`
	ChangedAt      time.Time     `json:"changed_at"`
}

func (s Status) same(o Status) bool {
	s.CheckedAt, o.CheckedAt = nil, nil
	s.ChangedAt, o.ChangedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(s, o)
}

func (s Status) ready(taskStatus string) bool {
	return taskStatus == model.StatusApproved && s.State == StateOpen && !s.Draft && s.Checks == ChecksSuccess &&
		s.Mergeable != nil && *s.Mergeable && s.ReviewDecision != ReviewChangesRequested &&
		!s.Scope.Blocking()
}

type Store interface {
	PRStatus(ctx context.Context, taskID string) ([]byte, bool, error)
	PRStatuses(ctx context.Context) ([][]byte, error)
	SetPRStatus(ctx context.Context, taskID string, data []byte) error
}

type Service struct {
	b  *board.Board
	st Store
}

func NewService(b *board.Board, st Store) *Service { return &Service{b: b, st: st} }

func (s *Service) Get(ctx context.Context, taskID string) (*Status, error) {
	if _, err := s.b.GetTask(ctx, model.Owner, taskID); err != nil {
		return nil, err
	}
	raw, ok, err := s.st.PRStatus(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.NotFound("no pull request status for task %s", taskID)
	}
	var out Status
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Service) List(ctx context.Context) ([]Status, error) {
	raws, err := s.st.PRStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(raws))
	for _, raw := range raws {
		var st Status
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	all, err := s.List(ctx)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	tasks, err := s.b.ListTasks(ctx, board.TaskQuery{Status: model.StatusApproved, Limit: 100000})
	if err != nil {
		return nil, err
	}
	approved := make(map[string]*model.Task, len(tasks))
	for _, t := range tasks {
		approved[t.ID] = t
	}
	var out []*decisions.Decision
	for _, st := range all {
		t := approved[st.TaskID]
		if t == nil || !st.ready(t.Status) {
			continue
		}
		d := &decisions.Decision{
			ID: KindMergeReady + ":" + t.ID, Kind: KindMergeReady, TaskID: t.ID, BriefID: t.BriefID,
			Title:     fmt.Sprintf("PR ready to merge for %s: %s", t.ID, t.Title),
			Summary:   summary(st),
			Links:     decisions.Links{Thread: model.ThreadTask + ":" + t.ID, PR: st.URL},
			CreatedAt: st.ChangedAt,
		}
		if t.Assignee != nil {
			d.AgentID = *t.Assignee
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}/pr", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /prs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 100
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				writeError(w, model.BadRequest("limit must be a positive integer"))
				return
			}
			limit = min(n, 1000)
		}
		all, err := s.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		out := make([]Status, 0, min(len(all), limit))
		for _, st := range all {
			if len(out) == limit {
				break
			}
			if (q.Get("brief_id") == "" || st.BriefID == q.Get("brief_id")) && (q.Get("state") == "" || st.State == q.Get("state")) {
				st.Diff = nil
				out = append(out, st)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"prs": out})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
