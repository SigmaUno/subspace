package incident_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type pausedControl struct {
	flakyControl
	paused    bool
	why       string
	resumeErr error
	resumeBad map[string]bool
	resumed   []string
}

func (c *pausedControl) PauseAll(_ context.Context, reason string) error {
	c.paused, c.why = true, reason
	return nil
}

func (c *pausedControl) ResumeAll(context.Context) error {
	if c.resumeErr != nil {
		return c.resumeErr
	}
	c.paused = false
	return nil
}

func (c *pausedControl) Paused() (bool, string) { return c.paused, c.why }

func (c *pausedControl) ListAgents(_ context.Context, _, status string) ([]*model.Agent, error) {
	var out []*model.Agent
	for _, a := range c.agents {
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *pausedControl) ResumeAgent(_ context.Context, id string) (*model.Agent, error) {
	if c.resumeBad[id] {
		return nil, errors.New("sqlite: database is locked at /srv/subspace/data/subspace.db")
	}
	c.resumed = append(c.resumed, id)
	return &model.Agent{ID: id}, nil
}

func newEndService(t *testing.T, ctl *pausedControl) *incident.Service {
	t.Helper()
	st := testdb.Open(t, testdb.Path(t, "t.db"))
	now := func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	return incident.New(incident.Options{Store: st, Control: ctl, Now: now})
}

func TestEndKeepsIncidentActiveWhenResumeAllFails(t *testing.T) {
	ctx := context.Background()
	ctl := &pausedControl{resumeErr: errors.New("control plane down")}
	svc := newEndService(t, ctl)
	started, err := svc.Start(ctx, incident.Request{Reason: "runaway agents", Actions: []string{incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}
	if !ctl.paused {
		t.Fatal("pause_all did not pause")
	}

	if _, err := svc.End(ctx, started.ID, incident.EndRequest{}); err == nil {
		t.Fatal("End succeeded although resume_all failed")
	}
	got, err := svc.Get(ctx, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndedAt != nil {
		t.Fatalf("incident ended at %v after a failed resume", got.EndedAt)
	}

	ctl.resumeErr = nil
	out, err := svc.End(ctx, started.ID, incident.EndRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.ResumedAll || out.EndedAt == nil || ctl.paused {
		t.Fatalf("retry: resumed_all=%v ended=%v paused=%v", out.ResumedAll, out.EndedAt, ctl.paused)
	}
}

func TestEndResumeAgentsReportsPartialFailure(t *testing.T) {
	ctx := context.Background()
	ctl := &pausedControl{
		flakyControl: flakyControl{agents: []*model.Agent{
			{ID: "w1", Role: model.RoleWorker, Status: model.AgentPaused},
			{ID: "w2", Role: model.RoleWorker, Status: model.AgentPaused},
			{ID: "w3", Role: model.RoleWorker, Status: model.AgentActive},
			{ID: "w4", Role: model.RoleWorker, Status: model.AgentPaused},
		}},
		resumeBad: map[string]bool{"w2": true},
	}
	svc := newEndService(t, ctl)
	started, err := svc.Start(ctx, incident.Request{Reason: "loop storm", Actions: []string{incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}

	out, err := svc.End(ctx, started.ID, incident.EndRequest{ResumeAgents: true, Note: "contained"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.ResumedAgents, []string{"w1", "w4"}) || !slices.Equal(ctl.resumed, []string{"w1", "w4"}) {
		t.Fatalf("resumed %v / %v, want w1 and w4 only", out.ResumedAgents, ctl.resumed)
	}
	if len(out.Failures) != 1 || out.Failures[0].AgentID != "w2" {
		t.Fatalf("failures %+v", out.Failures)
	}
	if strings.Contains(out.Failures[0].Error, "/srv/") || strings.Contains(out.Failures[0].Error, "sqlite") {
		t.Fatalf("failure exposes internals: %q", out.Failures[0].Error)
	}
	if out.EndedAt == nil {
		t.Fatal("partial resume failure kept the incident open")
	}

	again, err := svc.End(ctx, started.ID, incident.EndRequest{ResumeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.ResumedAgents) != 0 || len(again.Failures) != 0 || len(ctl.resumed) != 2 {
		t.Fatalf("ending an ended incident acted again: %+v resumed %v", again, ctl.resumed)
	}
}
