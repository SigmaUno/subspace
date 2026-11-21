package incident_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type flakyControl struct {
	agents  []*model.Agent
	failing map[string]int
	rotated map[string]int
}

func (c *flakyControl) PauseAll(context.Context, string) error { return nil }
func (c *flakyControl) ResumeAll(context.Context) error        { return nil }
func (c *flakyControl) Paused() (bool, string)                 { return false, "" }

func (c *flakyControl) ListAgents(_ context.Context, role, _ string) ([]*model.Agent, error) {
	var out []*model.Agent
	for _, a := range c.agents {
		if role == "" || a.Role == role {
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *flakyControl) RevokeAgent(_ context.Context, id, _ string) (*model.Agent, error) {
	return &model.Agent{ID: id}, nil
}

func (c *flakyControl) RotateToken(_ context.Context, id string) (*board.Registered, error) {
	if c.failing[id] > 0 {
		c.failing[id]--
		return nil, errors.New("sqlite: disk I/O error writing /srv/subspace/data/subspace.db-wal")
	}
	c.rotated[id]++
	return &board.Registered{Agent: &model.Agent{ID: id}, Connect: board.Connect{AgentID: id, Token: "fresh-" + id}}, nil
}

func (c *flakyControl) ResumeAgent(_ context.Context, id string) (*model.Agent, error) {
	return &model.Agent{ID: id}, nil
}

func TestIncidentRotationRetryRotatesOnlyFailedLeaders(t *testing.T) {
	st := testdb.Open(t, testdb.Path(t, "t.db"))
	ctl := &flakyControl{
		agents: []*model.Agent{
			{ID: "lead-a", Role: model.RoleLeader, Status: model.AgentActive},
			{ID: "lead-b", Role: model.RoleLeader, Status: model.AgentActive},
		},
		failing: map[string]int{"lead-b": 1},
		rotated: map[string]int{},
	}
	now := func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	svc := incident.New(incident.Options{Store: st, Control: ctl, Now: now})
	req := incident.Request{Reason: "leaked leader token", Actions: []string{incident.RotateLeaderTokens}}

	first, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed != 1 || len(first.Tokens) != 1 || first.Tokens[0].AgentID != "lead-a" {
		t.Fatalf("first: failed=%d tokens=%v", first.Failed, first.Tokens)
	}
	for _, r := range first.Results {
		for _, f := range r.Failures {
			if strings.Contains(f.Error, "/srv/") || strings.Contains(f.Error, "sqlite") {
				t.Fatalf("failure exposes internals: %q", f.Error)
			}
		}
	}

	again, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if ctl.rotated["lead-a"] != 1 {
		t.Fatalf("lead-a rotated %d times; its token from the first response must stay valid", ctl.rotated["lead-a"])
	}
	if ctl.rotated["lead-b"] != 1 || len(again.Tokens) != 1 || again.Tokens[0].AgentID != "lead-b" {
		t.Fatalf("retry: rotated=%v tokens=%v", ctl.rotated, again.Tokens)
	}
	if again.Failed != 0 {
		t.Fatalf("retry still failed: %+v", again.Results)
	}
	for _, r := range again.Results {
		if r.Action == incident.RotateLeaderTokens && !slices.Equal(r.Agents, []string{"lead-a", "lead-b"}) {
			t.Fatalf("rotated agents: %v", r.Agents)
		}
	}
}
