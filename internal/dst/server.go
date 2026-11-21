package dst

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/search"
	"github.com/SigmaUno/subspace/internal/webhook"
)

//go:embed testdata/webhook/*.json
var fixtures embed.FS

var fixtureNames = []string{"github_pr_opened", "github_pr_synchronize", "github_pr_merged", "github_pr_closed", "forgejo_pr_merged", "forgejo_pr_opened"}

const (
	webhookSecret = "dst-webhook-secret" //nolint:gosec // simulation fixture key
	spawnModel    = "spawn"
	projectScope  = "project:sim"
)

var instrScopes = []string{instructions.ScopeGlobal, projectScope}

type ServerWorld struct {
	Faults      int
	Moderation  bool
	RetainDays  int
	Gates       string
	MinTests    int
	Autopilot   bool
	AutoMerge   bool
	Spawn       autopilot.SpawnConfig
	Capacity    capacity.Config
	VerifyFail  int
	NotifyFail  int
	BackupEvery time.Duration
	BackupKeep  int
	V4          *V4World
}

func NewServerWorld(seed int64) World {
	w := NewWorld(seed)
	r := rand.New(rand.NewSource(seed ^ 0x5e7e)) //nolint:gosec // deterministic simulation
	kinds := []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}
	if r.Intn(3) == 0 {
		skip := model.ProviderForKind(kinds[r.Intn(len(kinds))])
		for _, k := range kinds {
			if p := model.ProviderForKind(k); p != skip {
				w.AllowedProviders = append(w.AllowedProviders, p)
			}
		}
	}
	for _, role := range []string{model.RoleTester, model.RoleWorker} {
		if r.Intn(4) > 0 {
			continue
		}
		var keep []AgentSpec
		for _, a := range w.Agents {
			if a.Role != role {
				keep = append(keep, a)
			}
		}
		w.Agents = keep
	}
	kind := kinds[r.Intn(len(kinds))]
	for len(w.AllowedProviders) > 0 && !slices.Contains(w.AllowedProviders, model.ProviderForKind(kind)) {
		kind = kinds[r.Intn(len(kinds))]
	}
	sw := &ServerWorld{
		Autopilot:   r.Intn(4) > 0,
		AutoMerge:   r.Intn(2) == 0,
		Spawn:       autopilot.SpawnConfig{Kind: kind, Model: spawnModel},
		VerifyFail:  r.Intn(40),
		NotifyFail:  20 + r.Intn(50),
		BackupEvery: time.Duration(15+r.Intn(30)) * time.Minute,
		BackupKeep:  1 + r.Intn(3),
	}
	if r.Intn(2) == 0 {
		sw.Faults = 10
	}
	sw.Moderation = r.Intn(4) > 0
	sw.RetainDays = r.Intn(3)
	if r.Intn(3) == 0 {
		gs := []string{"questions", "review", "budget", "tests"}
		sw.Gates = strings.Join(gs[:1+r.Intn(len(gs))], ",")
		sw.MinTests = r.Intn(12)
	}
	if r.Intn(2) == 0 {
		w.Rival = "lead2"
	}
	sw.Capacity.Limits = []capacity.Limit{{Provider: model.ProviderForKind(kind), Model: spawnModel, MaxConcurrency: 1 + r.Intn(3)}}
	if r.Intn(2) == 0 {
		sw.Capacity.GlobalMaxAgents = len(w.Agents) + 1 + r.Intn(4)
	}
	w.Server = sw
	return w
}

type Push struct {
	Task             string
	Status           string
	Failed           int
	Reviewer         string
	ReviewerProvider string
	WorkerProvider   string
	Paused           bool
}

type InstrHead struct {
	Version int
	Text    string
}

type ServerState struct {
	AutoMerge     bool
	Autopilot     bool
	Moderation    bool
	BackupKeep    int
	Backups       int
	Spawns        []*model.Spawn
	Capacity      *board.CapacityView
	Candidates    []*mergequeue.Candidate
	Pushes        []Push
	Instr         map[string]InstrHead
	Sent          map[string]int
	Episodes      map[string]int
	OpenQuestions map[string]bool
	Archived      bool
	Splits        map[string]string
	Announcements []string
	Knowledge     []string
	V4            *V4State
}

type ServerOptions struct {
	AutoApprove   bool
	NoQuotaGate   bool
	NoQualityGate bool
	NoLeakScanner bool
}

type ServerSystem struct {
	*MCPSystem
	o        ServerOptions
	f        features
	archived *bool
	ids      *ServerState
	dir      string
	hub      events.Hub
	w        World
	ap       *autopilot.Service
	mq       *mergequeue.Queue
	cands    *mergequeue.MemStore
	hook     http.Handler
	bk       *backup.Scheduler
	next     time.Time
	expect   map[string]string
	nt       *notify.Service
	sink     *flakySink
	stop     context.CancelFunc
	watch    events.Subscription
	sigs     map[string]string
	eps      map[string]int
	instr    *instructions.Service
	search   *search.Service
	lastOp   Kind
	heads    map[string]InstrHead
	hooks    int
	v        *subsystems
	settled  bool

	mu     sync.Mutex
	head   string
	runs   map[string]int
	pushes []Push
}

func NewServerSystem(dir string, clock *Clock, lease time.Duration) (System, error) {
	return NewServerFactory(ServerOptions{})(dir, clock, lease)
}

func NewServerFactory(o ServerOptions) Factory {
	return func(dir string, clock *Clock, lease time.Duration) (System, error) {
		hub := events.NewHub()
		bs, err := newBoardSystem(dir, clock, lease, hub)
		if err != nil {
			return nil, err
		}
		s := &ServerSystem{o: o, dir: dir, hub: hub, sigs: map[string]string{}, eps: map[string]int{}, runs: map[string]int{}}
		s.MCPSystem = newMCP(bs, MCPOptions{}, s.extendRegistry)
		s.server = s.hookServer
		return s, nil
	}
}

func (s *ServerSystem) Setup(ctx context.Context, w World) (Setup, error) {
	if w.Server == nil {
		return Setup{}, errors.New("server system needs a server world")
	}
	out, err := s.BoardSystem.Setup(ctx, w)
	if err != nil {
		return out, err
	}
	s.w = w
	sw := w.Server
	s.faults.r = rand.New(rand.NewSource(w.Seed ^ 0xfa17)) //nolint:gosec // deterministic simulation
	s.faults.drop, s.faults.dup = sw.Faults, sw.Faults
	if _, err := s.b.SetCapacity(ctx, sw.Capacity); err != nil {
		return out, err
	}
	if sw.Gates != "" {
		g := gateConfig(Op{Text: sw.Gates, N: sw.MinTests})
		if _, err := s.b.UpdateBrief(ctx, s.brief, board.BriefPatch{QualityGates: &g}); err != nil {
			return out, err
		}
	}
	if sw.V4 != nil {
		s.v = newSubsystems(s, sw.V4)
	}
	if err := s.wire(ctx); err != nil {
		return out, err
	}
	s.cands = mergequeue.NewMemStore()
	s.mq, err = mergequeue.New(mergequeue.Config{
		Repos:    map[string]mergequeue.RepoConfig{simRepo: {Remote: "https://git.example/" + simRepo + ".git", Verify: []string{"make", "test"}, Auto: s.o.AutoApprove}},
		WorkDir:  filepath.Join(s.dir, "mq"),
		Store:    mqStore{s.cands, s},
		Runner:   s,
		Notifier: s,
		Gate:     s,
		Tasks:    s,
		Logger:   discard,
		Now:      s.clock.Now,
	})
	if err != nil {
		return out, err
	}
	if s.hook, err = webhook.NewHandler(webhookSecret, s.onWebhook); err != nil {
		return out, err
	}
	s.bk, err = backup.New(backup.Config{Dir: filepath.Join(s.dir, "backups"), Interval: sw.BackupEvery, Keep: sw.BackupKeep,
		Run: func(ctx context.Context, dst string) error { return s.st.Backup(ctx, dst) }, Verify: s.verifyBackup, Logger: discard, Now: s.clock.Now})
	if err != nil {
		return out, err
	}
	s.next = s.clock.Now().Add(sw.BackupEvery)
	s.sink = &flakySink{seed: w.Seed, pct: sw.NotifyFail, tries: map[string]int{}, sent: map[string]int{}}
	s.nt = notify.New(notify.Config{Sinks: []notify.Sink{s.sink}, Window: -1, Attempts: 4, BaseDelay: time.Millisecond,
		Clock: simClock{s.clock}, Jitter: func(int64) int64 { return 0 }, Log: discard})
	s.watch = s.hub.Subscribe(notify.Topics)
	nctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.nt.Start(nctx, s.hub)
	if s.v != nil {
		if err := s.v.setup(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *ServerSystem) hookServer(srv *mcp.Server) {
	if s.v != nil {
		s.v.hook(srv)
	}
}

func (s *ServerSystem) wire(ctx context.Context) error {
	s.ap = autopilot.New(s.b, autopilot.Options{DefaultSpawn: s.w.Server.Spawn, Now: s.clock.Now, Log: discard})
	s.ap.SetEnabled(s.brief, s.w.Server.Autopilot)
	s.instr = instructions.New(s.st, instructions.StoreAuditor{Store: s.st, Now: s.clock.Now}, s.clock.Now)
	s.search = search.New(s.st, discard)
	s.archived = nil
	if err := s.wireFeatures(ctx); err != nil {
		return err
	}
	if s.v != nil {
		if err := s.v.wire(ctx); err != nil {
			return err
		}
	}
	s.MCPSystem.wire()
	return nil
}

func (s *ServerSystem) Do(ctx context.Context, op Op) Result {
	s.lastOp = op.Kind
	s.touch(op)
	if s.v != nil {
		s.v.mark()
		if res, ok := s.v.do(ctx, op); ok {
			s.drain()
			return res
		}
	}
	if res, ok := s.doFeature(ctx, op); ok {
		s.drain()
		return res
	}
	var res Result
	switch op.Kind {
	case OpRestart:
		if s.v != nil {
			s.v.down(ctx, op.Crash)
		}
		res = s.BoardSystem.Do(ctx, op)
		if res.Err == nil {
			res.Err = s.wire(ctx)
		}
		if res.Err == nil && s.v != nil {
			res.Err = s.v.restart(ctx, op.Crash)
		}
	case OpEnqueue:
		res.Err = s.enqueue(ctx, op.Task)
	case OpMergeTick:
		s.mergeTick(ctx)
	case OpAutopilotTick:
		rs, err := s.ap.Tick(ctx)
		res.Err = err
		for _, r := range rs {
			if r.Error != "" {
				continue
			}
			res.Actions++
			if r.Kind == autopilot.ActSpawn {
				res.Spawned += r.Spawn.N
			}
		}
	case OpWebhook:
		res.Err = s.deliver(op)
	case OpWebhookRace:
		var wg sync.WaitGroup
		wg.Go(func() { res.Err = s.deliver(op) })
		wg.Go(func() { _, _ = s.b.MarkDone(ctx, op.Task, "merged") })
		wg.Wait()
	case OpInstrSave:
		scope, kind, _ := strings.Cut(op.Target, "/")
		_, res.Err = s.instr.Save(ctx, model.Owner, scope, kind, op.Text, "dst")
	case OpInstrRollback:
		scope, kind, _ := strings.Cut(op.Target, "/")
		_, res.Err = s.instr.Rollback(ctx, model.Owner, scope, kind, op.N, "")
	case OpSearch:
		rs, err := s.search.Search(ctx, op.Text, op.Target, search.MaxLimit)
		res.Err = err
		for _, r := range rs {
			res.Hits = append(res.Hits, r.ID)
		}
	default:
		res = s.MCPSystem.Do(ctx, op)
	}
	if op.Kind == OpAdvance {
		s.backupTick(ctx)
	}
	s.drain()
	return res
}

func (s *ServerSystem) check(ctx context.Context, taskID string) (*model.Task, error) {
	if paused, _ := s.b.Paused(); paused {
		return nil, model.Conflict("subspace is paused")
	}
	t, err := s.b.GetTask(ctx, model.Owner, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != model.StatusApproved {
		return nil, model.Conflict("task %s is %s, not approved", t.ID, t.Status)
	}
	if t.Tests != nil && t.Tests.Failed > 0 {
		return nil, model.Conflict("task %s has %d failing tests", t.ID, t.Tests.Failed)
	}
	if t.Reviewer == nil || t.Assignee == nil {
		return nil, model.Conflict("task %s was not approved by a reviewer", t.ID)
	}
	rv, err := s.b.GetAgent(ctx, *t.Reviewer)
	if err != nil {
		return nil, err
	}
	wk, err := s.b.GetAgent(ctx, *t.Assignee)
	if err != nil {
		return nil, err
	}
	if rv.Provider == wk.Provider {
		return nil, model.Conflict("task %s was reviewed on the worker's provider %s", t.ID, rv.Provider)
	}
	return t, nil
}

func (s *ServerSystem) Approved(ctx context.Context, c mergequeue.Candidate) (bool, error) {
	_, err := s.check(ctx, c.TaskID)
	if model.AsError(err).Code == "conflict" {
		return false, nil
	}
	return err == nil, err
}

func (s *ServerSystem) RequestChanges(ctx context.Context, taskID, reason string) error {
	t, err := s.b.GetTask(ctx, model.Owner, taskID)
	if err != nil || t.Assignee == nil {
		return err
	}
	_, err = s.b.AppendMessage(ctx, model.ThreadAgent, *t.Assignee, model.Message{
		AuthorID: "mergequeue", AuthorRole: "system", Kind: model.MsgSystem, Body: taskID + " was not merged: " + reason,
	})
	return err
}

func (s *ServerSystem) Notify(ctx context.Context, taskID, body string) error {
	_, err := s.b.AppendMessage(ctx, model.ThreadTask, taskID, model.Message{
		AuthorID: "mergequeue", AuthorRole: "system", Kind: model.MsgSystem, Body: body,
	})
	return err
}

func (s *ServerSystem) Run(ctx context.Context, _, name string, args ...string) (string, error) {
	s.mu.Lock()
	head := s.head
	if name != "git" {
		s.runs[head]++
		n := s.runs[head]
		s.mu.Unlock()
		if roll(s.w.Seed, "verify", head, n) < s.w.Server.VerifyFail {
			return "FAIL\n", errors.New("exit status 1")
		}
		return "ok\n", nil
	}
	s.mu.Unlock()
	switch args[0] {
	case "checkout":
		s.mu.Lock()
		s.head = strings.TrimPrefix(args[len(args)-1], "refs/remotes/origin/")
		s.mu.Unlock()
	case "rev-parse":
		return fmt.Sprintf("%040x\n", roll(s.w.Seed, "sha", head, args[len(args)-1])), nil
	case "push":
		s.recordPush(ctx, head)
	}
	return "", nil
}

func (s *ServerSystem) recordPush(ctx context.Context, branch string) {
	ts, err := s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000})
	if err != nil {
		s.fault(InvMerge, "list tasks at push: %v", err)
		return
	}
	p := Push{Task: "branch " + branch}
	for _, t := range ts {
		if t.Branch != branch {
			continue
		}
		p.Task, p.Status, p.Reviewer = t.ID, t.Status, deref(t.Reviewer)
		if t.Tests != nil {
			p.Failed = t.Tests.Failed
		}
		if a, err := s.b.GetAgent(ctx, p.Reviewer); err == nil {
			p.ReviewerProvider = a.Provider
		}
		if a, err := s.b.GetAgent(ctx, deref(t.Assignee)); err == nil {
			p.WorkerProvider = a.Provider
		}
	}
	p.Paused, _ = s.b.Paused()
	s.mu.Lock()
	s.pushes = append(s.pushes, p)
	s.mu.Unlock()
}

type mqStore struct {
	*mergequeue.MemStore
	s *ServerSystem
}

func (m mqStore) Update(ctx context.Context, c *mergequeue.Candidate) error {
	if err := m.MemStore.Update(ctx, c); err != nil || c.State != mergequeue.StateMerged {
		return err
	}
	_, err := m.s.b.MarkDone(ctx, c.TaskID, fmt.Sprintf("merged by merge queue: %s#%d", c.Repo, c.PR))
	return err
}

func prNumber(u *string) int {
	if u == nil {
		return 0
	}
	n, _ := strconv.Atoi((*u)[strings.LastIndex(*u, "/")+1:])
	return n
}

func (s *ServerSystem) enqueue(ctx context.Context, taskID string) error {
	t, err := s.check(ctx, taskID)
	if err != nil {
		return err
	}
	_, err = s.mq.Enqueue(ctx, mergequeue.Candidate{Repo: t.Repo, PR: prNumber(t.PRURL), Head: t.Branch, Base: "main", TaskID: t.ID})
	return err
}

func (s *ServerSystem) mergeTick(ctx context.Context) {
	if s.w.Server.AutoMerge {
		ts, err := s.b.ListTasks(ctx, board.TaskQuery{Status: model.StatusApproved, Limit: 100000})
		if err != nil {
			s.fault(InvMerge, "list approved: %v", err)
			return
		}
		cs, err := s.cands.List(ctx, "")
		if err != nil {
			s.fault(InvMerge, "list candidates: %v", err)
			return
		}
		queued := map[string]bool{}
		for _, c := range cs {
			queued[c.TaskID] = true
		}
		for _, t := range ts {
			if queued[t.ID] {
				continue
			}
			if err := s.enqueue(ctx, t.ID); err != nil && model.AsError(err).Code != "conflict" {
				s.fault(InvAutoEnqueue, "auto-enqueue %s: %v", t.ID, err)
			}
		}
	}
	for range 100 {
		c, err := s.mq.Process(ctx, simRepo)
		if err != nil {
			s.fault(InvMerge, "process: %v", err)
			return
		}
		if c == nil {
			return
		}
	}
	s.fault(InvMerge, "merge queue did not drain")
}

func (s *ServerSystem) payload(name, taskID string) ([]byte, error) {
	raw, err := fixtures.ReadFile("testdata/webhook/" + name + ".json")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	branch, url, n := "sub/unknown", prURL(taskID), 0
	if t, err := s.b.GetTask(context.Background(), model.Owner, taskID); err == nil {
		branch = t.Branch
		n = prNumber(&url)
	}
	m["number"] = n
	pr, _ := m["pull_request"].(map[string]any)
	pr["number"], pr["html_url"] = n, url
	pr["head"].(map[string]any)["ref"] = branch
	m["repository"].(map[string]any)["full_name"] = simRepo
	return json.Marshal(m)
}

func (s *ServerSystem) deliver(op Op) error {
	body, err := s.payload(op.Target, op.Task)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if op.Forged {
		body = bytes.Replace(body, []byte(simRepo), []byte("evil/repo"), 1)
	}
	s.hooks++
	id := fmt.Sprintf("dst-%d", s.hooks)
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(body))
	if strings.HasPrefix(op.Target, "forgejo") {
		req.Header.Set("X-Forgejo-Event", "pull_request")
		req.Header.Set("X-Forgejo-Signature", sig)
		req.Header.Set("X-Forgejo-Delivery", id)
	} else {
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
		req.Header.Set("X-GitHub-Delivery", id)
	}
	rec := httptest.NewRecorder()
	s.hook.ServeHTTP(rec, req)
	if rec.Code/100 != 2 {
		return fmt.Errorf("webhook: http %d", rec.Code)
	}
	return nil
}

func (s *ServerSystem) onWebhook(ctx context.Context, ev webhook.Event) error {
	if !ev.Merged {
		return nil
	}
	ts, err := s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000})
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Repo != ev.Repo || t.Branch != ev.HeadBranch || t.Status != model.StatusApproved {
			continue
		}
		_, err := s.b.MarkDone(ctx, t.ID, "merged: "+ev.URL)
		if model.AsError(err).Code == "conflict" {
			return nil
		}
		return err
	}
	return nil
}

func (s *ServerSystem) backupTick(ctx context.Context) {
	now := s.clock.Now()
	if now.Before(s.next) {
		return
	}
	for !s.next.After(now) {
		s.next = s.next.Add(s.w.Server.BackupEvery)
	}
	ts, err := s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000})
	if err != nil {
		s.fault(InvBackup, "list tasks: %v", err)
		return
	}
	s.expect = map[string]string{}
	for _, t := range ts {
		s.expect[t.ID] = t.Status
	}
	if _, err := s.bk.RunOnce(ctx); err != nil {
		s.fault(InvBackup, "%v", err)
	}
}

func (s *ServerSystem) verifyBackup(ctx context.Context, path string) (err error) {
	if s.pg != nil {
		return verifyPGBackup(ctx, path, s.expect)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	var ok string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&ok); err != nil {
		return err
	}
	if ok != "ok" {
		return fmt.Errorf("integrity_check: %s", ok)
	}
	rows, err := db.QueryContext(ctx, "SELECT id, status FROM tasks")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return err
		}
		got[id] = st
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !maps.Equal(got, s.expect) {
		return fmt.Errorf("backup tasks %v, live %v", got, s.expect)
	}
	return nil
}

func (s *ServerSystem) drain() {
	for {
		select {
		case ev := <-s.watch.C():
			id, sig, kind := notifyKey(ev)
			if id == "" || s.sigs[id] == sig {
				continue
			}
			s.sigs[id] = sig
			if kind != "" {
				s.eps[id+"/"+kind]++
			}
		default:
			if n := s.watch.TakeDropped(); n > 0 {
				s.fault(InvHarness, "event watcher dropped %d events", n)
			}
			return
		}
	}
}

func notifyKey(ev events.Event) (id, sig, kind string) {
	switch d := ev.Data.(type) {
	case *model.Question:
		if ev.Type == "question.created" {
			kind = "question"
		}
		return d.ID, ev.Type, kind
	case *model.Plan:
		if ev.Type == "plan.proposed" {
			kind = "plan"
		}
		return d.ID, ev.Type, kind
	case notify.Notification:
		if ev.Type == "alert.raised" {
			kind = d.Kind
		}
		return d.ID, ev.Type, kind
	case *model.Task:
		switch d.Status {
		case model.StatusNeedsOwner, model.StatusBlocked, model.StatusApproved:
			kind = d.Status
		}
		return d.ID, d.Status, kind
	}
	switch ev.Type {
	case "control.paused":
		return "control", "paused", "paused"
	case "control.resumed":
		return "control", "resumed", ""
	case "quota.warning":
		d, _ := ev.Data.(map[string]any)
		agent, _ := d["agent_id"].(string)
		level, _ := d["level"].(string)
		return "agent_quota:" + agent, fmt.Sprintf("%v|%s", d["starts_at"], level), "agent_quota"
	}
	return "", "", ""
}

func (s *ServerSystem) Snapshot(ctx context.Context) (*State, error) {
	prev := s.last
	out, err := s.BoardSystem.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	sv := &ServerState{AutoMerge: s.w.Server.AutoMerge, Autopilot: s.w.Server.Autopilot, Moderation: s.w.Server.Moderation,
		BackupKeep: s.w.Server.BackupKeep, OpenQuestions: map[string]bool{}}
	qs, err := s.b.ListQuestions(ctx, "open")
	if err != nil {
		return nil, err
	}
	for _, q := range qs {
		if q.TaskID != nil {
			sv.OpenQuestions[*q.TaskID] = true
		}
	}
	if sv.Archived, err = s.archivedSegments(ctx); err != nil {
		return nil, err
	}
	if s.ids == nil || featureIDs[s.lastOp] {
		if s.ids, err = s.featureIDs(ctx); err != nil {
			return nil, err
		}
	}
	sv.Splits, sv.Announcements, sv.Knowledge = s.ids.Splits, s.ids.Announcements, s.ids.Knowledge
	if s.lastOp == OpAutopilotTick {
		if sv.Spawns, err = s.b.ListSpawns(ctx, s.brief); err != nil {
			return nil, err
		}
		if sv.Capacity, err = s.b.Capacity(ctx); err != nil {
			return nil, err
		}
	}
	if sv.Candidates, err = s.cands.List(ctx, ""); err != nil {
		return nil, err
	}
	es, err := os.ReadDir(filepath.Join(s.dir, "backups"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "subspace-") && strings.HasSuffix(e.Name(), ".db") {
			sv.Backups++
		}
	}
	if s.heads == nil || s.lastOp == OpInstrSave || s.lastOp == OpInstrRollback {
		if s.heads, err = s.instrHeads(ctx); err != nil {
			return nil, err
		}
	}
	sv.Instr = s.heads
	s.mu.Lock()
	sv.Pushes = append([]Push(nil), s.pushes...)
	s.mu.Unlock()
	sv.Sent = s.sink.counts()
	sv.Episodes = maps.Clone(s.eps)
	if s.v != nil {
		quotas := s.lastOp == OpClaim || s.lastOp == OpUpdate || s.lastOp == OpBatch || s.lastOp == OpLeak || s.lastOp == OpRestart
		if sv.V4, err = s.v.snapshot(ctx, prev, out, quotas); err != nil {
			return nil, err
		}
	}
	out.Server = sv
	return out, nil
}

func (s *ServerSystem) instrHeads(ctx context.Context) (map[string]InstrHead, error) {
	out := map[string]InstrHead{}
	for _, scope := range instrScopes {
		for _, kind := range instructions.Kinds {
			v, err := s.instr.Get(ctx, scope, kind)
			switch {
			case err == nil:
				out[scope+"/"+kind] = InstrHead{Version: v.Version, Text: v.Text}
			case model.AsError(err).Code != "not_found":
				return nil, err
			}
		}
	}
	return out, nil
}

func (s *ServerSystem) Settle() {
	if s.v != nil && !s.settled {
		s.settled = true
		s.v.settlePush(context.Background())
	}
	if s.stop != nil {
		s.stop()
		s.nt.Wait()
	}
}

func (s *ServerSystem) Close() error {
	s.Settle()
	if s.watch != nil {
		s.watch.Close()
	}
	if s.v != nil {
		s.v.close()
	}
	return s.BoardSystem.Close()
}

type flakySink struct {
	seed  int64
	pct   int
	mu    sync.Mutex
	tries map[string]int
	sent  map[string]int
}

func (f *flakySink) Name() string { return "dst" }

func (f *flakySink) Send(_ context.Context, n notify.Notification) error {
	key := n.ID + "/" + n.Kind
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries[key]++
	if roll(f.seed, "notify", key, f.tries[key]) < f.pct {
		return errors.New("sink unavailable")
	}
	f.sent[key]++
	return nil
}

func (f *flakySink) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.sent)
}

type simClock struct{ c *Clock }

func (c simClock) Now() time.Time { return c.c.Now() }

func (c simClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.c.Now()
	return ch
}

func roll(seed int64, parts ...any) int {
	h := fnv.New64a()
	_, _ = fmt.Fprint(h, seed)
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "|%v", p)
	}
	return int(h.Sum64() % 100)
}
