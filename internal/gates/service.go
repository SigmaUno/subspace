package gates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	ToolOverride = "override_gates"

	overrideKey = "gates.override."
	headKey     = "gates.head."
	reviewNote  = "Review (approve): "
	maxReason   = 2000
)

type PRSource interface {
	PRStatus(ctx context.Context, taskID string) ([]byte, bool, error)
}

type Service struct {
	b   *board.Board
	st  store.Store
	pr  PRSource
	now func() time.Time
}

func NewService(b *board.Board, st store.Store, pr PRSource, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{b: b, st: st, pr: pr, now: now}
}

func (s *Service) Gate(tx store.Tx, c board.QualityCheck) error {
	r, err := s.evaluate(tx, c)
	if err != nil {
		return err
	}
	if e := r.Error(); e != nil {
		return e
	}
	return nil
}

func (s *Service) Report(ctx context.Context, taskID string) (Report, error) {
	var out Report
	err := s.st.View(ctx, func(tx store.Tx) error {
		t, err := tx.GetTask(taskID)
		if errors.Is(err, store.ErrNotFound) {
			return model.NotFound("task %s not found", taskID)
		}
		if err != nil {
			return err
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		out, err = s.evaluate(tx, board.QualityCheck{Ctx: ctx, Op: board.QualityStatus, Actor: model.Owner, Task: t, Brief: br, Now: s.now()})
		return err
	})
	return out, err
}

func (s *Service) evaluate(tx store.Tx, c board.QualityCheck) (Report, error) {
	t, br, cfg := c.Task, c.Brief, c.Brief.QualityGates
	in := Input{Config: cfg, Now: c.Now, Task: t, Brief: br}
	var err error
	if cfg.Tests {
		if in.Tests, err = testReports(tx, t); err != nil {
			return Report{}, err
		}
	}
	if cfg.Checks || cfg.Scope || cfg.MaxPRAgeHours > 0 {
		if in.PR, in.PRError, err = s.prInfo(c.Ctx, tx, t); err != nil {
			return Report{}, err
		}
	}
	if cfg.Questions {
		if in.OpenQuestions, err = openQuestions(tx, t.ID); err != nil {
			return Report{}, err
		}
	}
	if cfg.Review {
		if in.Review, err = reviewInfo(tx, c); err != nil {
			return Report{}, err
		}
	}
	if cfg.Budget {
		if in.BriefCost, err = tx.BriefCost(br.ID); err != nil {
			return Report{}, err
		}
	}
	if in.Override, err = loadOverride(tx, t.ID); err != nil {
		return Report{}, err
	}
	return Evaluate(in), nil
}

func testReports(tx store.Tx, t *model.Task) ([]TestReport, error) {
	var out []TestReport
	if t.Tests != nil {
		out = append(out, TestReport{TaskID: t.ID, Result: *t.Tests})
	}
	testers, err := tx.ListTasks(store.TaskFilter{BriefID: t.BriefID, Role: model.RoleTester, Limit: 100000})
	if err != nil {
		return nil, err
	}
	for _, o := range testers {
		if o.ID == t.ID || o.Tests == nil || o.Status == model.StatusCancelled {
			continue
		}
		sameBranch := t.Branch != "" && o.Branch == t.Branch
		samePR := t.PRURL != nil && *t.PRURL != "" && o.PRURL != nil && *o.PRURL == *t.PRURL
		if sameBranch || samePR {
			out = append(out, TestReport{TaskID: o.ID, Result: *o.Tests})
		}
	}
	return out, nil
}

type head struct {
	SHA   string    `json:"sha"`
	Since time.Time `json:"since"`
}

func (s *Service) prInfo(ctx context.Context, tx store.Tx, t *model.Task) (*PR, string, error) {
	if s.pr == nil {
		return nil, "pull request status is not available", nil
	}
	if t.PRURL == nil || *t.PRURL == "" {
		return nil, "task has no pull request", nil
	}
	raw, ok, err := s.pr.PRStatus(ctx, t.ID)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "pull request was never synced", nil
	}
	var st prsync.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, "", err
	}
	if st.URL != *t.PRURL {
		return nil, "pull request status is for another pull request", nil
	}
	if st.Error != nil && st.State == "" {
		return nil, "pull request sync failed", nil
	}
	pr := &PR{Checks: st.Checks, HeadSHA: st.HeadSHA, ScopeSynced: st.Scope != nil}
	if st.Scope != nil {
		for _, f := range st.Scope.Findings {
			if f.Severity == "error" && !slices.Contains(pr.ScopeErrors, f.Rule) {
				pr.ScopeErrors = append(pr.ScopeErrors, f.Rule)
			}
		}
	}
	var h head
	if v, ok, err := tx.GetKV(headKey + t.ID); err != nil {
		return nil, "", err
	} else if ok {
		_ = json.Unmarshal([]byte(v), &h)
	}
	switch {
	case h.SHA == st.HeadSHA && !h.Since.IsZero():
		pr.HeadSince = h.Since
	case st.CheckedAt != nil:
		pr.HeadSince = st.ChangedAt
	}
	return pr, "", nil
}

func openQuestions(tx store.Tx, taskID string) ([]string, error) {
	qs, err := tx.ListQuestions(store.QuestionFilter{Status: "open", Limit: 100000})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, q := range qs {
		if q.TaskID != nil && *q.TaskID == taskID {
			out = append(out, q.ID)
		}
	}
	return out, nil
}

func reviewInfo(tx store.Tx, c board.QualityCheck) (*ReviewInfo, error) {
	t := c.Task
	rv := &ReviewInfo{}
	if c.Op == board.QualityApprove {
		rv.Reviewer, rv.Comments = c.Actor.ID, c.Comments
	} else if t.Reviewer != nil {
		rv.Reviewer = *t.Reviewer
		msgs, err := tx.ListMessages(model.ThreadTask, t.ID, 0, 100000)
		if err != nil {
			return nil, err
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if m := msgs[i]; m.AuthorID == rv.Reviewer && strings.HasPrefix(m.Body, reviewNote) {
				rv.Comments = strings.TrimPrefix(m.Body, reviewNote)
				break
			}
		}
	}
	if rv.Reviewer == "" {
		return rv, nil
	}
	provider := func(id string) (string, error) {
		a, err := tx.GetAgent(id)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return a.Provider, nil
	}
	var err error
	if rv.ReviewerProvider, err = provider(rv.Reviewer); err != nil {
		return nil, err
	}
	if t.Assignee != nil {
		if rv.WorkerProvider, err = provider(*t.Assignee); err != nil {
			return nil, err
		}
	}
	return rv, nil
}

func loadOverride(tx store.Tx, taskID string) (*Override, error) {
	v, ok, err := tx.GetKV(overrideKey + taskID)
	if err != nil || !ok || v == "" {
		return nil, err
	}
	var o Override
	if err := json.Unmarshal([]byte(v), &o); err != nil {
		return nil, err
	}
	return &o, nil
}

type OverrideInput struct {
	Reason string   `json:"reason"`
	Gates  []string `json:"gates"`
}

func (s *Service) Override(ctx context.Context, taskID string, in OverrideInput) (Report, error) {
	in.Reason = board.Redact(strings.TrimSpace(in.Reason))
	if in.Reason == "" {
		return Report{}, model.BadRequest("reason is required")
	}
	if len(in.Reason) > maxReason {
		return Report{}, model.BadRequest("reason must be at most %d bytes", maxReason)
	}
	gates := []string{}
	for _, g := range in.Gates {
		if !slices.Contains(All, g) {
			return Report{}, model.BadRequest("unknown gate %q (known: %s)", g, strings.Join(All, ", "))
		}
		if !slices.Contains(gates, g) {
			gates = append(gates, g)
		}
	}
	o := Override{Gates: gates, Reason: in.Reason, By: model.Owner.ID, At: s.now()}
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		t, err := tx.GetTask(taskID)
		if errors.Is(err, store.ErrNotFound) {
			return model.NotFound("task %s not found", taskID)
		}
		if err != nil {
			return err
		}
		prev, err := loadOverride(tx, t.ID)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if err := tx.SetKV(overrideKey+t.ID, string(raw)); err != nil {
			return err
		}
		note := auth.AuditNote(ctx, o.Reason)
		id := t.ID
		before := map[string]any{"override": prev}
		return tx.InsertAudit(&model.AuditEvent{TS: o.At, AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: ToolOverride,
			TaskID: &id, Note: &note, Before: before, After: map[string]any{"gates": gates, "brief_id": t.BriefID}})
	})
	if err != nil {
		return Report{}, err
	}
	scope := "all gates"
	if len(gates) > 0 {
		scope = strings.Join(gates, ", ")
	}
	if _, err := s.b.AppendMessage(ctx, model.ThreadTask, taskID, model.Message{AuthorID: model.Owner.ID, AuthorRole: model.Owner.Role,
		Kind: model.MsgSystem, Body: "Owner overrode quality gates (" + scope + "): " + o.Reason}); err != nil {
		return Report{}, err
	}
	if _, err := s.b.PromoteGated(ctx, taskID); err != nil {
		return Report{}, err
	}
	return s.Report(ctx, taskID)
}

func (s *Service) Run(ctx context.Context, hub events.Hub) {
	sub := hub.Subscribe([]string{"board"})
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.C():
			st, ok := ev.Data.(prsync.Status)
			if !ok || ev.Type != prsync.EventPR {
				continue
			}
			if st.HeadSHA != "" {
				_ = s.recordHead(ctx, st.TaskID, st.HeadSHA, ev.TS)
			}
			_, _ = s.b.PromoteGated(ctx, st.TaskID)
		}
	}
}

func (s *Service) recordHead(ctx context.Context, taskID, sha string, at time.Time) error {
	return s.st.Atomic(ctx, func(tx store.Tx) error {
		v, ok, err := tx.GetKV(headKey + taskID)
		if err != nil {
			return err
		}
		var h head
		if ok {
			_ = json.Unmarshal([]byte(v), &h)
		}
		if h.SHA == sha {
			return nil
		}
		raw, err := json.Marshal(head{SHA: sha, Since: at.UTC()})
		if err != nil {
			return err
		}
		return tx.SetKV(headKey+taskID, string(raw))
	})
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}/gates", func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.Report(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	})
	mux.HandleFunc("POST /tasks/{id}/gates/override", func(w http.ResponseWriter, r *http.Request) {
		var in OverrideInput
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, model.BadRequest("invalid JSON body"))
			return
		}
		rep, err := s.Override(r.Context(), r.PathValue("id"), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
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
