package helpreq

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	TypeHelp    = "help"
	TypeBlocker = "blocker"
	TypeSplit   = "split"

	StatusOpen       = "open"
	StatusAccepted   = "accepted"
	StatusSuperseded = "superseded"
	StatusClosed     = "closed"

	StatusAll = "all"

	NeedsLeader = "leader"
	NeedsOwner  = "owner"

	maxDetails = 8000
	maxSummary = 300
)

var helpKinds = []string{"unclear_spec", "missing_access", "conflicting_requirements", "too_big", "dependency_issue"}

type Proposed struct {
	Ref                string       `json:"ref,omitempty"`
	Title              string       `json:"title"`
	Spec               string       `json:"spec"`
	AcceptanceCriteria []string     `json:"acceptance_criteria"`
	Repo               string       `json:"repo"`
	DependsOn          []string     `json:"depends_on"`
	Role               string       `json:"role"`
	Budget             model.Budget `json:"budget"`
	Priority           int          `json:"priority"`
	ComponentID        string       `json:"component_id,omitempty"`
}

type Request struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Kind          string     `json:"kind"`
	TaskID        string     `json:"task_id"`
	BriefID       string     `json:"brief_id"`
	AgentID       string     `json:"agent_id"`
	Role          string     `json:"role"`
	Needs         string     `json:"needs"`
	Details       string     `json:"details"`
	ProposedTasks []Proposed `json:"proposed_tasks"`
	Status        string     `json:"status"`
	CreatedTasks  []string   `json:"created_tasks"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Filter struct {
	Status  string
	BriefID string
	TaskID  string
	Limit   int
}

type Service struct {
	st  store.Store
	b   *board.Board
	now func() time.Time
}

func New(st store.Store, b *board.Board) *Service {
	return &Service{st: st, b: b, now: time.Now}
}

func terminal(t *model.Task) bool {
	return t.Status == model.StatusDone || t.Status == model.StatusCancelled
}

func (s *Service) RequestHelp(ctx context.Context, a model.Actor, taskID, kind, details string) (*Request, error) {
	if !slices.Contains(helpKinds, kind) {
		return nil, model.BadRequest("kind must be one of %s", strings.Join(helpKinds, ", "))
	}
	details, err := s.checkText(ctx, a, board.ChannelHelp, "details", details)
	if err != nil {
		return nil, err
	}
	t, err := s.liveTask(ctx, a, taskID)
	if err != nil {
		return nil, err
	}
	r := &Request{Type: TypeHelp, Kind: kind, TaskID: t.ID, BriefID: t.BriefID, AgentID: a.ID, Role: a.Role, Details: details}
	if err := s.record(ctx, a, "request_help", r, map[string]any{"request_id": "", "kind": kind}); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("[%s] help requested (%s): %s", r.ID, kind, details)
	return r, s.announce(ctx, a, t, body)
}

func (s *Service) ReportBlocker(ctx context.Context, a model.Actor, taskID, reason, needs string) (*Request, error) {
	if needs != NeedsLeader && needs != NeedsOwner {
		return nil, model.BadRequest("needs must be leader or owner")
	}
	reason, err := s.checkText(ctx, a, board.ChannelStatus, "reason", reason)
	if err != nil {
		return nil, err
	}
	t, err := s.b.UpdateStatus(ctx, a, board.StatusInput{TaskID: taskID, Status: model.StatusBlocked, Note: reason})
	if err != nil {
		return nil, err
	}
	r := &Request{Type: TypeBlocker, Kind: "blocker", TaskID: t.ID, BriefID: t.BriefID, AgentID: a.ID, Role: a.Role, Needs: needs, Details: reason}
	if err := s.record(ctx, a, "report_blocker", r, map[string]any{"needs": needs}); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("[%s] blocker (needs %s): %s", r.ID, needs, reason)
	if err := s.announce(ctx, a, t, body); err != nil {
		return nil, err
	}
	if needs == NeedsLeader {
		return r, s.notifyLeaders(ctx, a, t.BriefID, body)
	}
	return r, nil
}

func (s *Service) SuggestSplit(ctx context.Context, a model.Actor, taskID string, proposed []Proposed) (*Request, error) {
	t, err := s.liveTask(ctx, a, taskID)
	if err != nil {
		return nil, err
	}
	br, err := s.b.GetBrief(ctx, t.BriefID)
	if err != nil {
		return nil, err
	}
	proposed, err = normalize(t, br, proposed, func(v string) string {
		return board.Redact(s.b.Scan(ctx, a, board.ChannelHelp, v))
	})
	if err != nil {
		return nil, err
	}
	if err := s.checkDeps(ctx, t, proposed); err != nil {
		return nil, err
	}
	r := &Request{Type: TypeSplit, Kind: "too_big", TaskID: t.ID, BriefID: t.BriefID, AgentID: a.ID, Role: a.Role, ProposedTasks: proposed}
	r.Details = splitSummary(proposed)
	err = s.atomic(ctx, func(tx store.Tx, q sqlTx) error {
		old, err := query(q, `WHERE task_id=? AND type=? AND status=?`, t.ID, TypeSplit, StatusOpen)
		if err != nil {
			return err
		}
		for _, o := range old {
			if err := setStatus(q, o.ID, StatusOpen, StatusSuperseded); err != nil {
				return err
			}
		}
		r.Status, r.CreatedAt = StatusOpen, s.now().UTC()
		if err := insert(tx, q, r); err != nil {
			return err
		}
		return s.audit(tx, a, "suggest_split", r.TaskID, nil, map[string]any{"request_id": r.ID, "tasks": len(proposed)}, "")
	})
	if err != nil {
		return nil, err
	}
	return r, s.announce(ctx, a, t, fmt.Sprintf("[%s] split proposed: %s", r.ID, r.Details))
}

func (s *Service) List(ctx context.Context, f Filter) ([]*Request, error) {
	return s.ListFor(ctx, model.Owner, f)
}

func (s *Service) ListFor(ctx context.Context, a model.Actor, f Filter) ([]*Request, error) {
	briefs, err := s.b.BriefsFor(ctx, a, "")
	if err != nil {
		return nil, err
	}
	visible := make(map[string]bool, len(briefs))
	for _, br := range briefs {
		visible[br.ID] = true
	}
	status := f.Status
	if status == "" {
		status = StatusOpen
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var out []*Request
	err = s.view(ctx, func(tx store.Tx, q sqlTx) error {
		where, args := `WHERE 1=1`, []any{}
		if f.BriefID != "" {
			where += ` AND brief_id=?`
			args = append(args, f.BriefID)
		}
		if f.TaskID != "" {
			where += ` AND task_id=?`
			args = append(args, f.TaskID)
		}
		rs, err := query(q, where+` ORDER BY rowid DESC`, args...)
		if err != nil {
			return err
		}
		for _, r := range rs {
			if !visible[r.BriefID] {
				continue
			}
			t, err := tx.GetTask(r.TaskID)
			if err != nil {
				return err
			}
			r.Status = effective(r, t)
			if status == StatusAll || r.Status == status {
				out = append(out, r)
			}
			if len(out) >= limit {
				break
			}
		}
		return nil
	})
	if out == nil {
		out = []*Request{}
	}
	return out, err
}

func effective(r *Request, t *model.Task) string {
	if r.Status != StatusOpen {
		return r.Status
	}
	live := !terminal(t)
	if r.Type == TypeBlocker {
		live = t.Status == model.StatusBlocked || t.Status == model.StatusNeedsOwner
	}
	if live {
		return StatusOpen
	}
	return StatusClosed
}

func (s *Service) liveTask(ctx context.Context, a model.Actor, id string) (*model.Task, error) {
	if id == "" {
		return nil, model.BadRequest("task_id is required")
	}
	t, err := s.b.GetTask(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if terminal(t) {
		return nil, model.Conflict("task %s is %s", t.ID, t.Status)
	}
	return t, nil
}

func (s *Service) record(ctx context.Context, a model.Actor, tool string, r *Request, after map[string]any) error {
	return s.atomic(ctx, func(tx store.Tx, q sqlTx) error {
		r.Status, r.CreatedAt = StatusOpen, s.now().UTC()
		if err := insert(tx, q, r); err != nil {
			return err
		}
		after["request_id"] = r.ID
		return s.audit(tx, a, tool, r.TaskID, nil, after, "")
	})
}

func (s *Service) audit(tx store.Tx, a model.Actor, tool, taskID string, before, after map[string]any, note string) error {
	e := &model.AuditEvent{TS: s.now().UTC(), AgentID: a.ID, Role: a.Role, Tool: tool, Before: before, After: after}
	if taskID != "" {
		e.TaskID = &taskID
	}
	if note != "" {
		e.Note = &note
	}
	return tx.InsertAudit(e)
}

func (s *Service) announce(ctx context.Context, a model.Actor, t *model.Task, body string) error {
	m := model.Message{AuthorID: a.ID, AuthorRole: a.Role, Kind: model.MsgMessage, Body: body}
	if _, err := s.b.AppendMessage(ctx, model.ThreadTask, t.ID, m); err != nil {
		return err
	}
	_, err := s.b.AppendMessage(ctx, model.ThreadBrief, t.BriefID, m)
	return err
}

func (s *Service) notifyLeaders(ctx context.Context, a model.Actor, briefID, body string) error {
	leaders, err := s.b.ListAgents(ctx, model.RoleLeader, model.AgentActive)
	if err != nil {
		return err
	}
	for _, l := range leaders {
		ok, err := s.b.BriefVisible(ctx, model.Actor{ID: l.ID, Role: l.Role}, briefID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		m := model.Message{AuthorID: a.ID, AuthorRole: a.Role, Kind: model.MsgMessage, Body: body}
		if _, err := s.b.AppendMessage(ctx, model.ThreadAgent, l.ID, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkText(ctx context.Context, a model.Actor, channel, field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", model.BadRequest("%s is required", field)
	}
	if len(v) > maxDetails {
		return "", model.BadRequest("%s too long (max %d bytes)", field, maxDetails)
	}
	return board.Redact(s.b.Scan(ctx, a, channel, v)), nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func splitSummary(p []Proposed) string {
	titles := make([]string, len(p))
	for i, x := range p {
		titles[i] = x.Title
	}
	return fmt.Sprintf("%d tasks: %s", len(p), strings.Join(titles, "; "))
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
