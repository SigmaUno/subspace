package incident_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testdb"
)

var clock = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type fakeControl struct {
	agents    []*model.Agent
	paused    bool
	why       string
	listErr   error
	revokeErr map[string]error
	rotateErr map[string]error
	revoked   []string
	reasons   []string
	resumed   []string
}

func (c *fakeControl) PauseAll(_ context.Context, reason string) error {
	c.paused, c.why = true, reason
	return nil
}

func (c *fakeControl) ResumeAll(context.Context) error {
	c.paused, c.why = false, ""
	return nil
}

func (c *fakeControl) Paused() (bool, string) { return c.paused, c.why }

func (c *fakeControl) ListAgents(_ context.Context, role, status string) ([]*model.Agent, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	var out []*model.Agent
	for _, a := range c.agents {
		if (role == "" || a.Role == role) && (status == "" || a.Status == status) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *fakeControl) agent(id string) (*model.Agent, error) {
	for _, a := range c.agents {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, model.NotFound("agent %s not found", id)
}

func (c *fakeControl) RevokeAgent(_ context.Context, id, reason string) (*model.Agent, error) {
	if err := c.revokeErr[id]; err != nil {
		return nil, err
	}
	a, err := c.agent(id)
	if err != nil {
		return nil, err
	}
	a.Status = model.AgentRevoked
	c.revoked = append(c.revoked, id)
	c.reasons = append(c.reasons, reason)
	return a, nil
}

func (c *fakeControl) RotateToken(_ context.Context, id string) (*board.Registered, error) {
	if err := c.rotateErr[id]; err != nil {
		return nil, err
	}
	a, err := c.agent(id)
	if err != nil {
		return nil, err
	}
	return &board.Registered{Agent: a, Connect: board.Connect{AgentID: id, Token: "fresh-" + id}}, nil
}

func (c *fakeControl) ResumeAgent(_ context.Context, id string) (*model.Agent, error) {
	a, err := c.agent(id)
	if err != nil {
		return nil, err
	}
	a.Status = model.AgentActive
	c.resumed = append(c.resumed, id)
	return a, nil
}

type published struct {
	topics []string
	typ    string
	data   any
}

type recorder struct{ events []published }

func (r *recorder) Publish(topics []string, typ string, data any) {
	r.events = append(r.events, published{topics, typ, data})
}

func newService(t *testing.T, st store.Store, ctl incident.Control, pub *recorder, dir string) *incident.Service {
	t.Helper()
	o := incident.Options{Store: st, Control: ctl, Dir: dir, Now: func() time.Time { return clock }}
	if pub != nil {
		o.Pub = pub
	}
	return incident.New(o)
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	return testdb.Open(t, testdb.Path(t, "t.db"))
}

func audits(t *testing.T, st store.Store, tool string) []*model.AuditEvent {
	t.Helper()
	var out []*model.AuditEvent
	if err := st.View(context.Background(), func(tx store.Tx) (err error) {
		out, err = tx.ListAudit(store.AuditFilter{Tool: tool, Limit: 10})
		return
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func result(t *testing.T, in incident.Incident, action string) incident.Result {
	t.Helper()
	for _, r := range in.Results {
		if r.Action == action {
			return r
		}
	}
	t.Fatalf("no %s result in %+v", action, in.Results)
	return incident.Result{}
}

func TestStartValidatesRequest(t *testing.T) {
	svc := newService(t, openStore(t), &fakeControl{}, nil, "")
	for _, tc := range []struct {
		name string
		req  incident.Request
		msg  string
	}{
		{"blank reason", incident.Request{Reason: "   ", Actions: []string{incident.Notify}}, "reason is required"},
		{"long reason", incident.Request{Reason: strings.Repeat("é", 501), Actions: []string{incident.Notify}}, "at most 500 characters"},
		{"no actions", incident.Request{Reason: "x"}, "actions must list at least one"},
		{"unknown action", incident.Request{Reason: "x", Actions: []string{incident.Notify, "nuke"}}, `unknown action "nuke"`},
		{"scope without revoke", incident.Request{Reason: "x", Actions: []string{incident.Notify}, Revoke: incident.Scope{Provider: "acme"}}, "revoke applies to the revoke_agents action only"},
		{"bad role", incident.Request{Reason: "x", Actions: []string{incident.RevokeAgents}, Revoke: incident.Scope{Role: "owner"}}, "revoke.role must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Start(context.Background(), tc.req)
			if e := model.AsError(err); err == nil || e.Code != "bad_request" || !strings.Contains(e.Message, tc.msg) {
				t.Fatalf("err = %v, want bad_request containing %q", err, tc.msg)
			}
		})
	}
	list, err := svc.List(context.Background(), 10)
	if err != nil || len(list) != 0 {
		t.Fatalf("rejected requests created incidents: %v %v", list, err)
	}
}

func TestStartNormalizesRequestAndRecordsAudit(t *testing.T) {
	st := openStore(t)
	ctl := &fakeControl{}
	pub := &recorder{}
	svc := newService(t, st, ctl, pub, "")
	reason := strings.Repeat("é", 500)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TokenID: "tok-7"})

	out, err := svc.Start(ctx, incident.Request{
		Reason:  "  " + reason + "  ",
		Actions: []string{incident.Notify, incident.PauseAll, incident.Notify},
		Revoke:  incident.Scope{Role: " ", Provider: " "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.ID != "INC-1" || out.Reason != reason || out.Actor != "tok-7" || !out.StartedAt.Equal(clock) || out.EndedAt != nil {
		t.Fatalf("outcome %+v", out.Incident)
	}
	want := []string{incident.PauseAll, incident.Notify}
	if !slices.Equal(out.Actions, want) || out.Succeeded != 2 || out.Failed != 0 {
		t.Fatalf("actions %v succeeded %d failed %d", out.Actions, out.Succeeded, out.Failed)
	}
	for i, r := range out.Results {
		if r.Action != want[i] || r.Status != incident.StatusOK {
			t.Fatalf("results %+v", out.Results)
		}
	}
	if !ctl.paused || ctl.why != "incident INC-1: "+reason {
		t.Fatalf("paused=%v why=%q", ctl.paused, ctl.why)
	}

	got, err := svc.Get(context.Background(), "INC-1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Actions, want) || len(got.Results) != 2 || got.Actor != "tok-7" || !got.StartedAt.Equal(clock) || got.EndedAt != nil {
		t.Fatalf("stored %+v", got)
	}

	ev := audits(t, st, "incident_start")
	if len(ev) != 1 || ev[0].Note == nil || *ev[0].Note != "token=tok-7; incident INC-1" || ev[0].After["id"] != "INC-1" {
		t.Fatalf("audit %+v", ev)
	}
}

func TestStartActorFallsBackToOwner(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no principal":    context.Background(),
		"anonymous token": auth.WithPrincipal(context.Background(), auth.Principal{Scope: auth.ScopeOwner}),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newService(t, openStore(t), &fakeControl{}, nil, "")
			out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}})
			if err != nil {
				t.Fatal(err)
			}
			if out.Actor != model.Owner.ID {
				t.Fatalf("actor %q", out.Actor)
			}
		})
	}
}

func TestStartUsesWallClockWithoutNow(t *testing.T) {
	svc := incident.New(incident.Options{Store: openStore(t), Control: &fakeControl{}})
	before := time.Now().Add(-time.Second)
	out, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.Notify}})
	if err != nil {
		t.Fatal(err)
	}
	if out.StartedAt.Before(before) || out.StartedAt.After(time.Now()) || out.StartedAt.Location() != time.UTC {
		t.Fatalf("started at %v", out.StartedAt)
	}
}

func TestStartMergesActionsIntoActiveIncident(t *testing.T) {
	ctx := context.Background()
	ctl := &fakeControl{agents: []*model.Agent{{ID: "w1", Role: model.RoleWorker, Status: model.AgentActive}}}
	pub := &recorder{}
	svc := newService(t, openStore(t), ctl, pub, "")

	first, err := svc.Start(ctx, incident.Request{Reason: "first", Actions: []string{incident.Notify, incident.RevokeAgents}})
	if err != nil {
		t.Fatal(err)
	}
	ctl.agents = append(ctl.agents, &model.Agent{ID: "w2", Role: model.RoleWorker, Status: model.AgentActive})

	second, err := svc.Start(ctx, incident.Request{Reason: "second", Actions: []string{incident.Notify, incident.RevokeAgents, incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.ID != first.ID || second.Reason != "first" {
		t.Fatalf("second start did not join the active incident: %+v", second.Incident)
	}
	want := []string{incident.PauseAll, incident.RevokeAgents, incident.Notify}
	if !slices.Equal(second.Actions, want) || !slices.Equal(second.Skipped, []string{incident.Notify}) {
		t.Fatalf("actions %v skipped %v", second.Actions, second.Skipped)
	}
	if len(pub.events) != 1 || !ctl.paused {
		t.Fatalf("published %d times, paused=%v", len(pub.events), ctl.paused)
	}
	if r := result(t, second.Incident, incident.RevokeAgents); !slices.Equal(r.Agents, []string{"w1", "w2"}) {
		t.Fatalf("revoke rerun agents %v", r.Agents)
	}
	got, err := svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Actions, want) {
		t.Fatalf("stored actions %v", got.Actions)
	}

	third, err := svc.Start(ctx, incident.Request{Reason: "third", Actions: []string{incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(third.Actions, want) || !slices.Equal(third.Skipped, []string{incident.PauseAll}) || third.Succeeded != 3 {
		t.Fatalf("third %+v", third)
	}
}

func TestRevokeHonoursScope(t *testing.T) {
	for _, tc := range []struct {
		scope  incident.Scope
		agents []string
		detail string
	}{
		{incident.Scope{}, []string{"w1", "w2", "l1"}, "all agents"},
		{incident.Scope{Role: model.RoleWorker}, []string{"w1", "w2"}, "role=worker"},
		{incident.Scope{Provider: "acme"}, []string{"w1", "l1"}, "provider=acme"},
		{incident.Scope{Role: model.RoleWorker, Provider: "acme"}, []string{"w1"}, "role=worker provider=acme"},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			ctl := &fakeControl{agents: []*model.Agent{
				{ID: "w1", Role: model.RoleWorker, Provider: "acme", Status: model.AgentActive},
				{ID: "w2", Role: model.RoleWorker, Provider: "other", Status: model.AgentPaused},
				{ID: "l1", Role: model.RoleLeader, Provider: "acme", Status: model.AgentActive},
				{ID: "w3", Role: model.RoleWorker, Provider: "acme", Status: model.AgentRevoked},
			}}
			svc := newService(t, openStore(t), ctl, nil, "")
			out, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.RevokeAgents}, Revoke: tc.scope})
			if err != nil {
				t.Fatal(err)
			}
			r := result(t, out.Incident, incident.RevokeAgents)
			if r.Status != incident.StatusOK || r.Detail != tc.detail || !slices.Equal(r.Agents, tc.agents) || !slices.Equal(ctl.revoked, tc.agents) {
				t.Fatalf("result %+v revoked %v", r, ctl.revoked)
			}
			for _, why := range ctl.reasons {
				if why != "incident INC-1" {
					t.Fatalf("revoke reason %q", why)
				}
			}
		})
	}
}

func TestRevokeRetryKeepsEarlierAgents(t *testing.T) {
	ctx := context.Background()
	ctl := &fakeControl{
		agents: []*model.Agent{
			{ID: "w1", Role: model.RoleWorker, Status: model.AgentActive},
			{ID: "w2", Role: model.RoleWorker, Status: model.AgentActive},
		},
		revokeErr: map[string]error{"w2": errors.New("sqlite: disk I/O error at /srv/subspace/data")},
	}
	svc := newService(t, openStore(t), ctl, nil, "")
	req := incident.Request{Reason: "x", Actions: []string{incident.RevokeAgents}}

	first, err := svc.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r := result(t, first.Incident, incident.RevokeAgents)
	if r.Status != incident.StatusFailed || !slices.Equal(r.Agents, []string{"w1"}) || first.Failed != 1 || first.Succeeded != 0 {
		t.Fatalf("first %+v", r)
	}
	if len(r.Failures) != 1 || r.Failures[0] != (incident.Failure{AgentID: "w2", Error: "internal error"}) {
		t.Fatalf("failures %+v", r.Failures)
	}

	ctl.revokeErr = nil
	again, err := svc.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r = result(t, again.Incident, incident.RevokeAgents)
	if r.Status != incident.StatusOK || !slices.Equal(r.Agents, []string{"w1", "w2"}) || !slices.Equal(ctl.revoked, []string{"w1", "w2"}) {
		t.Fatalf("retry %+v revoked %v", r, ctl.revoked)
	}
}

func TestRotateOnlyActiveLeaders(t *testing.T) {
	ctl := &fakeControl{agents: []*model.Agent{
		{ID: "w1", Role: model.RoleWorker, Status: model.AgentActive},
		{ID: "l1", Role: model.RoleLeader, Status: model.AgentActive},
		{ID: "l2", Role: model.RoleLeader, Status: model.AgentRevoked},
	}}
	svc := newService(t, openStore(t), ctl, nil, "")
	out, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.RotateLeaderTokens}})
	if err != nil {
		t.Fatal(err)
	}
	r := result(t, out.Incident, incident.RotateLeaderTokens)
	if r.Status != incident.StatusOK || !slices.Equal(r.Agents, []string{"l1"}) || len(out.Tokens) != 1 || out.Tokens[0].Token != "fresh-l1" {
		t.Fatalf("result %+v tokens %+v", r, out.Tokens)
	}
}

func TestAgentListingFailureFailsAgentActions(t *testing.T) {
	ctx := context.Background()
	ctl := &fakeControl{listErr: model.Conflict("board unavailable")}
	svc := newService(t, openStore(t), ctl, nil, "")
	out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.RevokeAgents, incident.RotateLeaderTokens, incident.Notify}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{incident.RevokeAgents, incident.RotateLeaderTokens} {
		if r := result(t, out.Incident, a); r.Status != incident.StatusFailed || r.Error != "board unavailable" {
			t.Fatalf("%s: %+v", a, r)
		}
	}
	if out.Failed != 2 || out.Succeeded != 1 {
		t.Fatalf("failed %d succeeded %d", out.Failed, out.Succeeded)
	}

	end, err := svc.End(ctx, out.ID, incident.EndRequest{ResumeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(end.Failures) != 1 || end.Failures[0] != (incident.Failure{Error: "board unavailable"}) || end.EndedAt == nil {
		t.Fatalf("end %+v", end)
	}
}

func TestNotifyPublishesSummary(t *testing.T) {
	ctl := &fakeControl{
		agents: []*model.Agent{
			{ID: "w1", Role: model.RoleWorker, Status: model.AgentActive},
			{ID: "w2", Role: model.RoleWorker, Status: model.AgentActive},
			{ID: "l1", Role: model.RoleLeader, Status: model.AgentActive},
		},
		revokeErr: map[string]error{"w2": model.Conflict("busy")},
	}
	pub := &recorder{}
	svc := newService(t, openStore(t), ctl, pub, "")
	out, err := svc.Start(context.Background(), incident.Request{
		Reason:  "token leak",
		Actions: []string{incident.Notify, incident.RevokeAgents, incident.RotateLeaderTokens, incident.PauseAll},
		Revoke:  incident.Scope{Role: model.RoleWorker},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := result(t, out.Incident, incident.Notify); r.Status != incident.StatusOK || r.Detail != "published" {
		t.Fatalf("notify %+v", r)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published %d events", len(pub.events))
	}
	ev := pub.events[0]
	n, ok := ev.data.(notify.Notification)
	if !ok || !slices.Equal(ev.topics, []string{"alerts"}) || ev.typ != "alert.raised" {
		t.Fatalf("event %+v", ev)
	}
	want := notify.Notification{
		ID: "incident:INC-1", Kind: "incident", Priority: 5, TS: clock,
		Title: "Incident INC-1: token leak",
		Text:  "pause_all ok; revoke_agents failed (1 agents) (1 failed); rotate_leader_tokens ok (1 agents)",
	}
	if n.ID != want.ID || n.Kind != want.Kind || n.Priority != want.Priority || !n.TS.Equal(want.TS) || n.Title != want.Title || n.Text != want.Text {
		t.Fatalf("notification %+v", n)
	}
}

func TestNotifyWithoutPublisher(t *testing.T) {
	svc := newService(t, openStore(t), &fakeControl{}, nil, "")
	out, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.Notify}})
	if err != nil {
		t.Fatal(err)
	}
	if r := result(t, out.Incident, incident.Notify); r.Status != incident.StatusOK {
		t.Fatalf("notify %+v", r)
	}
}

func TestEndLeavesForeignPauseInPlace(t *testing.T) {
	ctx := context.Background()
	ctl := &fakeControl{paused: true, why: "maintenance"}
	svc := newService(t, openStore(t), ctl, nil, "")
	out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}
	if r := result(t, out.Incident, incident.PauseAll); r.Status != incident.StatusOK || r.Detail != "already paused: maintenance" {
		t.Fatalf("pause %+v", r)
	}
	end, err := svc.End(ctx, out.ID, incident.EndRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if end.ResumedAll || !ctl.paused || ctl.why != "maintenance" {
		t.Fatalf("resumed_all=%v paused=%v why=%q", end.ResumedAll, ctl.paused, ctl.why)
	}
}

func TestEndResumesOnlyPausedAgentsAndAudits(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	ctl := &fakeControl{agents: []*model.Agent{
		{ID: "w1", Role: model.RoleWorker, Status: model.AgentPaused},
		{ID: "w2", Role: model.RoleWorker, Status: model.AgentActive},
		{ID: "w3", Role: model.RoleWorker, Status: model.AgentRevoked},
	}}
	svc := newService(t, st, ctl, nil, "")
	out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.PauseAll}})
	if err != nil {
		t.Fatal(err)
	}
	end, err := svc.End(ctx, out.ID, incident.EndRequest{ResumeAgents: true, Note: "contained"})
	if err != nil {
		t.Fatal(err)
	}
	if !end.ResumedAll || ctl.paused || !slices.Equal(end.ResumedAgents, []string{"w1"}) || !slices.Equal(ctl.resumed, []string{"w1"}) {
		t.Fatalf("end %+v resumed %v", end, ctl.resumed)
	}
	if end.EndedAt == nil || !end.EndedAt.Equal(clock) {
		t.Fatalf("ended at %v", end.EndedAt)
	}
	ev := audits(t, st, "incident_end")
	if len(ev) != 1 || ev[0].After["note"] != "contained" || ev[0].After["resumed_all"] != true || ev[0].Before["active"] != true {
		t.Fatalf("audit %+v", ev)
	}

	next, err := svc.Start(ctx, incident.Request{Reason: "y", Actions: []string{incident.Notify}})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Created || next.ID == out.ID {
		t.Fatalf("start after end joined %s", next.ID)
	}
}

func TestEndUnknownIncident(t *testing.T) {
	svc := newService(t, openStore(t), &fakeControl{}, nil, "")
	_, err := svc.End(context.Background(), "INC-404", incident.EndRequest{})
	if e := model.AsError(err); err == nil || e.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}
