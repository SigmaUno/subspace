package dst

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/announce"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/gates"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/knowledge"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/schedule"
	"github.com/SigmaUno/subspace/internal/search"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	scheduleTemplate = "fix-bug-with-regression-test"
	announcePrefix   = "Owner announcement "
	toolGetTask      = "get_task"
	toolArchived     = "archived"
)

type features struct {
	quota *quota.Service
	ann   *announce.Service
	help  *helpreq.Service
	know  *knowledge.Service
	inc   *incident.Service
	sched *schedule.Service
	exec  *schedule.BoardExecutor
	ret   *retention.Service
	gates *gates.Service
	est   *estimate.Service
	fc    *forecast.Service
}

type audience struct{ b *board.Board }

func (a audience) Brief(ctx context.Context, id string) error {
	_, err := a.b.GetBrief(ctx, id)
	return err
}

func (a audience) BriefAgents(ctx context.Context, id string) ([]string, error) {
	return a.b.ActiveAgentsOfBrief(ctx, id)
}

func (a audience) AllAgents(ctx context.Context) ([]string, error) {
	as, err := a.b.ListAgents(ctx, "", "")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, x := range as {
		if x.Status != model.AgentRevoked {
			ids = append(ids, x.ID)
		}
	}
	return ids, nil
}

type leaderInfo struct{ fc *forecast.Service }

var errNotSimulated = model.Errf("internal", "not simulated")

func (l leaderInfo) Forecast(ctx context.Context, briefID string) (any, error) {
	return l.fc.Forecast(ctx, briefID)
}

func (leaderInfo) Report(context.Context, string) (report.Report, error) {
	return report.Report{}, errNotSimulated
}

func (leaderInfo) Search(context.Context, string, string, int) ([]search.Result, error) {
	return nil, errNotSimulated
}

func (leaderInfo) Decisions(context.Context) ([]*decisions.Decision, error) {
	return nil, errNotSimulated
}

func (leaderInfo) Components(context.Context, string) ([]components.Component, error) {
	return nil, errNotSimulated
}

func (leaderInfo) AgentStats(context.Context, string) ([]any, error) { return nil, errNotSimulated }

type limiterClock struct{ c *Clock }

func (l limiterClock) Now() time.Time { return l.c.Now() }

func (l limiterClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time, 1)
	ch <- l.c.Now().Add(d)
	return ch, func() bool { return false }
}

func (s *ServerSystem) wireFeatures(ctx context.Context) error {
	sw, now := s.w.Server, s.clock.Now
	f := &s.f
	f.quota = quota.New(quota.Options{Store: s.st, Pub: s.BoardSystem.hub, Now: now})
	if !s.o.NoQuotaGate {
		s.b.SetGate(f.quota.Gate)
	}
	f.ann = announce.New(announce.Config{Store: s.st, Audience: audience{s.b}, Poster: s.b, Now: now})
	f.help = helpreq.New(s.st, s.b)
	f.know = knowledge.New(s.st, knowledge.Options{Now: now, Moderation: sw.Moderation,
		Limiter: ratelimit.New(ratelimit.Options{Default: ratelimit.Limit{PerMinute: 6, Burst: 10}, Clock: limiterClock{s.clock}})})
	f.inc = incident.New(incident.Options{Store: s.st, Control: s.b, Pub: s.BoardSystem.hub, Dir: s.dir, Now: now, Log: discard})
	f.exec = &schedule.BoardExecutor{Board: s.b, Tasks: prompts.New("")}
	f.sched = schedule.NewService(s.st, f.exec, now)
	pol := retention.DefaultPolicy()
	pol.AuditDays, pol.MessagesDays, pol.IdempotencyDays = sw.RetainDays, sw.RetainDays, sw.RetainDays
	pol.Batch, pol.SegmentRows, pol.VacuumPages, pol.Interval = 7, 40, 0, 0
	ret, err := retention.New(retention.Options{Store: s.st, Policy: pol, Dir: filepath.Join(s.dir, "retention"), Now: now, Log: discard})
	if err != nil {
		return err
	}
	f.ret = ret
	f.gates = gates.NewService(s.b, s.st, nil, now)
	if !s.o.NoQualityGate {
		s.b.SetQualityGate(f.gates.Gate)
	}
	f.est = estimate.NewService(estimate.StoreSource{Store: s.st}, estimate.Options{})
	f.fc = forecast.NewService(forecast.StoreSource{Store: s.st, Estimator: f.est}, forecast.Options{}, now)
	mcp.BindHelp(s.b, f.help)
	mcp.BindKnowledge(s.b, f.know)
	mcp.BindLeaderInfo(s.b, leaderInfo{f.fc})
	return s.runner().Recover(ctx)
}

func (s *ServerSystem) extendRegistry(r *mcp.Registry) {
	if s.f.ann != nil {
		mcp.AppendToInstructions(r, s.f.ann.Instructions)
	}
}

func (s *ServerSystem) runner() *schedule.Runner {
	return schedule.NewRunner(schedule.Options{Store: s.st, Exec: s.f.exec, Now: s.clock.Now, Log: discard})
}

func (s *ServerSystem) doFeature(ctx context.Context, op Op) (Result, bool) {
	var res Result
	var err error
	switch op.Kind {
	case OpSetQuota:
		err = s.f.quota.Set(ctx, op.Target, quota.Quota{MaxCostEURPerDay: op.Cost, MaxTasksPerDay: op.N, MaxConcurrentTasks: op.Limit})
	case OpAnnounce:
		res, err = s.announce(ctx, op)
	case OpUnannounce:
		err = s.f.ann.Delete(ctx, op.Target)
	case OpInstructions:
		res, err = s.instructions(ctx, op)
	case OpGetTask:
		var out struct {
			Task    *model.Task        `json:"task"`
			Context *board.TaskContext `json:"context"`
		}
		err = s.client(op).Call(ctx, toolGetTask, map[string]any{"task_id": op.Task}, &out)
		res.Context = out.Context
	case OpLeaderRead:
		res, err = s.leaderRead(ctx, op)
	case OpRequestHelp:
		var r *helpreq.Request
		if r, err = s.client(op).RequestHelp(ctx, op.Task, "unclear_spec", "which way?"); err == nil {
			res.ID = r.ID
		}
	case OpSuggestSplit:
		res, err = s.suggestSplit(ctx, op)
	case OpAcceptSplit:
		var r *helpreq.SplitResult
		if r, err = s.client(op).AcceptSplit(ctx, op.Target); err == nil {
			for _, t := range r.Tasks {
				res.Hits = append(res.Hits, t.ID)
			}
		}
	case OpAddKnowledge:
		var e *knowledge.Entry
		if e, err = s.client(op).AddKnowledge(ctx, knowledge.Input{BriefID: op.Target, Kind: "pitfall", Title: op.Text, Body: "note " + op.Text, Tags: []string{"sim"}}); err == nil {
			res.ID, res.Status, res.Hits = e.ID, e.Status, []string{e.BriefID}
		}
	case OpModerate:
		if op.Approve {
			_, err = s.f.know.Approve(ctx, op.Target)
		} else {
			err = s.f.know.Delete(ctx, op.Target)
		}
	case OpReadKnowledge:
		res, err = s.readKnowledge(ctx, op)
	case OpEstimate:
		res, err = s.estimate(ctx, op.Target)
	case OpIncidentStart:
		res, err = s.incidentStart(ctx, op)
	case OpIncidentEnd:
		res, err = s.incidentEnd(ctx, op)
	case OpResumeAgent:
		_, err = s.b.ResumeAgent(ctx, op.Target)
	case OpSchedule:
		res, err = s.createSchedule(ctx, op)
	case OpScheduleTick:
		err = s.scheduleTick(ctx, op.N)
		var lerr error
		res.Hits, lerr = s.scheduleRuns(ctx)
		err = errors.Join(err, lerr)
	case OpRetention:
		_, err = s.f.ret.RunOnce(ctx)
		s.verifyRetention(ctx)
	case OpRestore:
		res, err = s.restore(ctx, op.N)
		s.verifyRetention(ctx)
	case OpSetGates:
		g := gateConfig(op)
		_, err = s.b.UpdateBrief(ctx, s.brief, board.BriefPatch{QualityGates: &g})
	case OpOverride:
		var gs []string
		if op.Text != "" {
			gs = strings.Split(op.Text, ",")
		}
		_, err = s.f.gates.Override(ctx, op.Task, gates.OverrideInput{Reason: "dst override", Gates: gs})
	default:
		return res, false
	}
	res.Err = toolErr(err)
	return res, true
}

func gateConfig(op Op) model.QualityGates {
	var g model.QualityGates
	for _, name := range strings.Split(op.Text, ",") {
		switch name {
		case gates.Tests:
			g.Tests, g.MinTestsPassed = true, op.N
		case gates.Questions:
			g.Questions = true
		case gates.Review:
			g.Review = true
		case gates.Budget:
			g.Budget = true
		}
	}
	return g
}

func (s *ServerSystem) announce(ctx context.Context, op Op) (Result, error) {
	in := announce.Input{Text: op.Text, BriefID: op.Target}
	if op.Dur != 0 {
		e := s.clock.Now().Add(op.Dur)
		in.ExpiresAt = &e
	}
	a, n, err := s.f.ann.Publish(ctx, in)
	if err != nil {
		return Result{}, err
	}
	res := Result{ID: a.ID, Delivered: map[string]int{"": n}}
	as, err := s.b.ListAgents(ctx, "", "")
	if err != nil {
		return res, err
	}
	for _, ag := range as {
		ms, err := s.b.OwnerThread(ctx, model.ThreadAgent, ag.ID, 0, 100000)
		if err != nil {
			return res, err
		}
		for _, m := range ms {
			if strings.HasPrefix(m.Body, announcePrefix+a.ID+" ") {
				res.Delivered[ag.ID]++
			}
		}
	}
	return res, nil
}

func (s *ServerSystem) instructions(ctx context.Context, op Op) (Result, error) {
	in, err := s.client(op).GetInstructions(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Status: "none"}
	head, rest, ok := strings.Cut(in.Instructions, announce.Header)
	if !ok {
		return res, nil
	}
	res.Status = "listed"
	if strings.Contains(rest, announce.Header) || strings.Contains(head, announce.Header) {
		res.Status = "repeated"
	}
	for _, line := range strings.Split(rest, "\n") {
		if id, ok := strings.CutPrefix(line, "- ["); ok {
			if id, _, ok = strings.Cut(id, "]"); ok {
				res.Hits = append(res.Hits, id)
			}
		}
	}
	return res, nil
}

func (s *ServerSystem) leaderRead(ctx context.Context, op Op) (Result, error) {
	c := s.client(op)
	var res Result
	var err error
	switch op.Text {
	case "get_brief":
		var br *model.Brief
		if br, err = c.GetBrief(ctx, op.Target); err == nil {
			res.Hits = []string{br.ID}
		}
	case "board":
		var cols map[string][]*model.Task
		if cols, err = c.Board(ctx, op.Target); err == nil {
			for _, ts := range cols {
				for _, t := range ts {
					res.Hits = append(res.Hits, t.BriefID)
				}
			}
		}
	case "read_thread":
		_, err = c.ReadThread(ctx, "brief:"+op.Target, 0, 50)
		res.Hits = []string{op.Target}
	case "get_forecast":
		_, err = c.GetForecast(ctx, op.Target)
		res.Hits = []string{op.Target}
	case "get_task":
		var t *model.Task
		if t, err = c.GetTask(ctx, op.Task); err == nil {
			res.Hits = []string{t.BriefID}
		}
	case "list_briefs":
		var bs []*model.Brief
		if bs, err = c.ListBriefs(ctx, ""); err == nil {
			res.Hits = []string{}
			for _, b := range bs {
				res.Hits = append(res.Hits, b.ID)
			}
		}
	case "list_help_requests":
		var rs []*helpreq.Request
		if rs, err = c.ListHelpRequests(ctx, helpreq.Filter{Status: "all"}); err == nil {
			for _, r := range rs {
				res.Hits = append(res.Hits, r.BriefID)
			}
		}
	}
	return res, err
}

func (s *ServerSystem) suggestSplit(ctx context.Context, op Op) (Result, error) {
	ps := make([]helpreq.Proposed, op.N)
	for i := range ps {
		ps[i] = helpreq.Proposed{Ref: fmt.Sprintf("p%d", i), Title: fmt.Sprintf("part %d of %s", i, op.Task), Spec: "split spec"}
		if i > 0 && op.Text == "chain" {
			ps[i].DependsOn = []string{fmt.Sprintf("p%d", i-1)}
		}
	}
	r, err := s.client(op).SuggestSplit(ctx, op.Task, ps)
	if err != nil {
		return Result{}, err
	}
	return Result{ID: r.ID}, nil
}

func (s *ServerSystem) readKnowledge(ctx context.Context, op Op) (Result, error) {
	c := s.client(op)
	if op.Target != "" {
		e, err := c.GetKnowledge(ctx, op.Target)
		if err != nil {
			return Result{}, err
		}
		return Result{ID: e.ID, Status: e.Status}, nil
	}
	es, err := c.SearchKnowledge(ctx, op.Text, "", "", knowledge.MaxLimit)
	if err != nil {
		return Result{}, err
	}
	res := Result{Hits: []string{}}
	for _, e := range es {
		res.Hits = append(res.Hits, e.ID)
	}
	return res, nil
}

func (s *ServerSystem) estimate(ctx context.Context, briefID string) (Result, error) {
	var res Result
	var reps [2]estimate.Report
	var fcs [2]forecast.Forecast
	for i := range 2 {
		var err error
		if reps[i], err = s.f.est.Brief(ctx, briefID); err != nil {
			return res, err
		}
		if fcs[i], err = s.f.fc.Forecast(ctx, briefID); err != nil {
			return res, err
		}
	}
	res.Estimate, res.Forecast = &reps[0], &fcs[0]
	res.Stable = reflect.DeepEqual(reps[0], reps[1]) && reflect.DeepEqual(fcs[0], fcs[1])
	return res, nil
}

func (s *ServerSystem) activeIncident(ctx context.Context) (*incident.Incident, error) {
	all, err := s.f.inc.List(ctx, 1000)
	if err != nil {
		return nil, err
	}
	var active *incident.Incident
	for _, in := range all {
		if in.EndedAt == nil {
			if active != nil {
				s.fault(InvIncident, "incidents %s and %s are both active", active.ID, in.ID)
			}
			active = in
		}
	}
	return active, nil
}

func (s *ServerSystem) incidentStart(ctx context.Context, op Op) (Result, error) {
	req := incident.Request{Reason: "dst", Actions: strings.Split(op.Text, ",")}
	if op.Target != "" {
		role, provider, _ := strings.Cut(op.Target, "/")
		req.Revoke = incident.Scope{Role: role, Provider: provider}
	}
	out, err := s.f.inc.Start(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res := Result{ID: out.ID, Status: "joined"}
	if out.Created {
		res.Status = "created"
	}
	for _, c := range out.Tokens {
		res.Rotated = append(res.Rotated, c.AgentID)
		s.BoardSystem.mu.Lock()
		s.tokens[c.AgentID] = append(s.tokens[c.AgentID], c.Token)
		s.BoardSystem.mu.Unlock()
	}
	for _, r := range out.Results {
		if r.Status != incident.StatusOK {
			res.Hits = append(res.Hits, r.Action)
		}
	}
	if _, err := s.activeIncident(ctx); err != nil {
		return res, err
	}
	return res, nil
}

func (s *ServerSystem) incidentEnd(ctx context.Context, op Op) (Result, error) {
	active, err := s.activeIncident(ctx)
	if err != nil {
		return Result{}, err
	}
	id := "INC-0"
	if active != nil {
		id = active.ID
	}
	out, err := s.f.inc.End(ctx, id, incident.EndRequest{ResumeAgents: op.Approve})
	if err != nil {
		return Result{}, err
	}
	res := Result{ID: out.ID, Hits: out.ResumedAgents}
	if out.ResumedAll {
		res.Status = "resumed"
	}
	if active, err = s.activeIncident(ctx); err == nil && active != nil {
		s.fault(InvIncident, "incident %s still active after ending %s", active.ID, id)
	}
	return res, err
}

func (s *ServerSystem) createSchedule(ctx context.Context, op Op) (Result, error) {
	active := op.N
	sc, err := s.f.sched.Create(ctx, schedule.Input{
		Name: "sim " + op.Target, Cron: op.Target, Action: schedule.ActionAddTasks, BriefID: s.brief, CatchUp: op.Text, MaxActive: &active,
		Tasks: []schedule.TaskSpec{{TemplateID: scheduleTemplate, Repo: simRepo,
			Vars: map[string]string{"bug": "flake " + op.Target, "symptom": "fails", "area": "sim"}}},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ID: sc.ID}, nil
}

func (s *ServerSystem) scheduleTick(ctx context.Context, n int) error {
	errs := make([]error, max(n, 1))
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = s.runner().Tick(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (s *ServerSystem) scheduleRuns(ctx context.Context) ([]string, error) {
	scs, err := s.f.sched.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sc := range scs {
		runs, err := s.f.sched.Runs(ctx, sc.ID, 1000)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			if r.Status == schedule.RunCreated {
				out = append(out, runKey(sc.ID, r.ScheduledFor)+"|"+strings.Join(r.TaskIDs, ","))
			}
		}
	}
	return out, nil
}

func runKey(id string, at time.Time) string { return id + "|" + at.UTC().Format(time.RFC3339) }

func (s *ServerSystem) restore(ctx context.Context, n int) (Result, error) {
	st, err := s.f.ret.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if len(st.Segments) == 0 {
		return Result{}, model.NotFound("no retention segments")
	}
	seg := st.Segments[n%len(st.Segments)]
	out, err := s.f.ret.Restore(ctx, seg.ID)
	if err != nil {
		return Result{}, err
	}
	return Result{ID: fmt.Sprint(out.ID), Status: string(out.Kind)}, nil
}

func (s *ServerSystem) verifyRetention(ctx context.Context) {
	v, err := s.f.ret.Verify(ctx)
	switch {
	case err != nil:
		s.fault(InvRetention, "verify: %v", err)
	case !v.OK:
		s.fault(InvRetention, "archive does not verify: segment %d: %s", v.BrokenSegment, v.Error)
	}
	s.archived = nil
}

type archivedRow struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

func (s *ServerSystem) archivedAudit(ctx context.Context) ([]archivedRow, error) {
	st, err := s.f.ret.Status(ctx)
	if err != nil {
		return nil, err
	}
	var out []archivedRow
	for _, seg := range st.Segments {
		if seg.Kind != retention.Audit || seg.Status == retention.StatusRestored {
			continue
		}
		rows, err := readRows(filepath.Join(s.f.ret.Dir(), filepath.FromSlash(seg.File)))
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func readRows(path string) ([]archivedRow, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var out []archivedRow
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		var r archivedRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func (s *ServerSystem) Audit(ctx context.Context) ([]*model.AuditEvent, error) {
	live, err := s.b.ListAudit(ctx, store.AuditFilter{Limit: 1 << 30})
	if err != nil {
		return nil, err
	}
	rows, err := s.archivedAudit(ctx)
	if err != nil {
		return nil, err
	}
	have := map[int64]bool{}
	for _, e := range live {
		have[e.ID] = true
	}
	s.BoardSystem.mu.Lock()
	seen := map[int64]*model.AuditEvent{}
	for _, e := range s.audit {
		seen[e.ID] = e
	}
	s.BoardSystem.mu.Unlock()
	out := live
	for _, r := range rows {
		if have[r.ID] {
			continue
		}
		have[r.ID] = true
		e := seen[r.ID]
		if e == nil || e.Hash != r.Hash {
			e = &model.AuditEvent{ID: r.ID, Tool: toolArchived, Hash: r.Hash}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *ServerSystem) archivedSegments(ctx context.Context) (bool, error) {
	if s.archived != nil {
		return *s.archived, nil
	}
	st, err := s.f.ret.Status(ctx)
	if err != nil {
		return false, err
	}
	v := slices.ContainsFunc(st.Segments, func(g retention.Segment) bool {
		return g.Kind == retention.Audit && g.Status != retention.StatusRestored
	})
	s.archived = &v
	return v, nil
}

var featureIDs = map[Kind]bool{OpRestart: true, OpSuggestSplit: true, OpAcceptSplit: true, OpAnnounce: true, OpUnannounce: true,
	OpAddKnowledge: true, OpModerate: true, OpRetention: true, OpRestore: true}

func (s *ServerSystem) featureIDs(ctx context.Context) (*ServerState, error) {
	out := &ServerState{Splits: map[string]string{}}
	rs, err := s.f.help.List(ctx, helpreq.Filter{Status: helpreq.StatusOpen})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.Type == helpreq.TypeSplit {
			out.Splits[r.ID] = r.TaskID
		}
	}
	as, err := s.f.ann.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		out.Announcements = append(out.Announcements, a.ID)
	}
	es, err := s.f.know.Search(ctx, model.Owner, knowledge.Query{Limit: knowledge.MaxLimit})
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		out.Knowledge = append(out.Knowledge, e.ID)
	}
	slices.Sort(out.Announcements)
	slices.Sort(out.Knowledge)
	return out, nil
}
