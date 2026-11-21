package dst

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/drill"
	"github.com/SigmaUno/subspace/internal/gateway"
	"github.com/SigmaUno/subspace/internal/leakguard"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
	"github.com/SigmaUno/subspace/internal/transcript"
)

const (
	OpGatewayCall    Kind = "gateway_call"
	OpGatewayList    Kind = "gateway_tools"
	OpLogOutput      Kind = "log_output"
	OpTranscriptRead Kind = "transcript_read"
	OpLeak           Kind = "leak_token"
	OpReissue        Kind = "reissue_leaked"
	OpDrill          Kind = "restore_drill"
	OpPushCut        Kind = "push_cut"
	OpPushSettle     Kind = "push_settle"
)

const (
	gwName       = "sim"
	gwEnv        = "SUBSPACE_GATEWAY_SIM_TOKEN"
	gwCredential = "dst-gateway-credential-4b1d9e" //nolint:gosec // simulation fixture, not a credential
	leakMask     = "[redacted:token]"
	pushWait     = 5 * time.Second
)

var (
	upstreamTools = []string{"search", "search_code", "fetch", "delete_repo"}
	gwDenySets    = [][]string{nil, {"delete_*"}, {"fetch", "delete_*"}}
	leakChannels  = []string{board.ChannelMessage, board.ChannelNote, board.ChannelQuestion, board.ChannelSummary, board.ChannelStatus,
		board.ChannelTests, board.ChannelReview, board.ChannelOutput, board.ChannelHelp}
	flushStatuses = []string{model.StatusSubmitted, model.StatusBlocked, model.StatusCancelled, model.StatusDone}
)

type V4World struct {
	Roles []string
	Deny  []string
	Rate  int
	Calls int
	Fail  int
	Push  []string
}

func newV4World(seed int64, w World) *V4World {
	r := rand.New(rand.NewSource(seed ^ 0x4a4a)) //nolint:gosec // deterministic simulation
	v := &V4World{Deny: gwDenySets[r.Intn(len(gwDenySets))], Rate: 1 + r.Intn(4), Calls: 3 + r.Intn(15), Fail: r.Intn(25)}
	for _, role := range []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer} {
		if r.Intn(2) == 0 {
			v.Roles = append(v.Roles, role)
		}
	}
	if len(v.Roles) == 0 {
		v.Roles = []string{model.RoleWorker}
	}
	for _, a := range w.Agents {
		if (a.Role == model.RoleWorker || a.Role == model.RoleTester) && len(v.Push) < 2 {
			v.Push = append(v.Push, a.ID)
		}
	}
	return v
}

func (v *V4World) allowed(role, tool string) bool {
	if !slices.Contains(v.Roles, role) || !slices.Contains(upstreamTools, tool) {
		return false
	}
	g := gateway.Gateway{AllowTools: []string{"*"}, DenyTools: v.Deny}
	return g.Allows(tool)
}

func (v *V4World) rateLimited(tool string) bool {
	return strings.HasPrefix(tool, "search")
}

type fakeUpstream struct {
	seed int64
	fail int

	mu       sync.Mutex
	calls    int
	outcomes []string
}

func (f *fakeUpstream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply := func(key string, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, key: v})
	}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "upstream-1")
		reply("result", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "dst-upstream", "version": "1"}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		tools := make([]map[string]any, len(upstreamTools))
		for i, t := range upstreamTools {
			tools[i] = map[string]any{"name": t, "description": "simulated " + t, "inputSchema": map[string]any{"type": "object"}}
		}
		reply("result", map[string]any{"tools": tools})
	case "tools/call":
		f.mu.Lock()
		f.calls++
		n := f.calls
		p := roll(f.seed, "upstream", n)
		switch {
		case p < f.fail:
			f.outcomes = append(f.outcomes, "upstream_error")
			f.mu.Unlock()
			reply("error", map[string]any{"code": -32000, "message": "backend down, credential " + gwCredential})
		case p < f.fail+10:
			f.outcomes = append(f.outcomes, "tool_error")
			f.mu.Unlock()
			reply("result", map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "no such thing"}}})
		default:
			f.outcomes = append(f.outcomes, "ok")
			f.mu.Unlock()
			reply("result", map[string]any{"content": []any{map[string]any{"type": "text",
				"text": fmt.Sprintf("hit %d of %s via %s", n, req.Params.Name, gwCredential)}}})
		}
	default:
		reply("error", map[string]any{"code": -32601, "message": "unknown method"})
	}
}

type transcriptTasks struct{ b *board.Board }

func (t transcriptTasks) Task(ctx context.Context, id string) (transcript.Info, error) {
	task, err := t.b.GetTask(ctx, model.Owner, id)
	if err != nil {
		return transcript.Info{}, err
	}
	return transcript.Info{BriefID: task.BriefID, Status: task.Status}, nil
}

type outputDir struct{ b *board.Board }

func (o outputDir) Agent(ctx context.Context, id string) error {
	_, err := o.b.GetAgent(ctx, id)
	return err
}

func (o outputDir) Active(ctx context.Context) ([]string, error) {
	as, err := o.b.ListAgents(ctx, "", model.AgentActive)
	ids := make([]string, 0, len(as))
	for _, a := range as {
		ids = append(ids, a.ID)
	}
	return ids, err
}

func (o outputDir) CanSeeTask(ctx context.Context, a model.Actor, id string) error {
	_, err := o.b.GetTask(ctx, a, id)
	return err
}

type taskOwners struct{ b *board.Board }

func (o taskOwners) Owners(ctx context.Context, id string) (string, string, bool) {
	t, err := o.b.GetTask(ctx, model.Owner, id)
	if err != nil {
		return "", "", false
	}
	return deref(t.Assignee), deref(t.Reviewer), true
}

type backupList struct{ s *ServerSystem }

func (l backupList) List(ctx context.Context) ([]backup.Info, error) {
	if l.s.bk == nil {
		return nil, nil
	}
	return l.s.bk.List(ctx)
}

type tline struct {
	off  int64
	text string
}

type taskLines struct {
	lines   []tline
	durable int
	next    int64
}

type outModel struct {
	last   int64
	client int64
	texts  map[int64]string
}

type wake struct {
	task string
	mark int
}

type pusher struct {
	agent    string
	c        *client.MCP
	cancel   context.CancelFunc
	gate     chan struct{}
	asleep   chan struct{}
	attached chan struct{}
	done     chan struct{}
	live     atomic.Bool

	mu     sync.Mutex
	got    []client.Notification
	expect []wake
	mark   int
}

func (p *pusher) closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *pusher) received() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.got)
}

func (p *pusher) woken(w wake) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.got[min(w.mark, len(p.got)):] {
		if n.Resync() {
			return true
		}
		if e, ok := n.Event(); ok && e.Type == "task_assigned" && e.TaskID == w.task {
			return true
		}
	}
	return false
}

type subsystems struct {
	s      *ServerSystem
	w      *V4World
	up     *fakeUpstream
	upWeb  *httptest.Server
	web    *httptest.Server
	srv    atomic.Pointer[mcp.Server]
	gw     *gateway.Service
	out    *agentout.Service
	tr     *transcript.Service
	guard  *leakguard.Guard
	drill  *drill.Service
	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	push   map[string]*pusher
	tasks  map[string]*taskLines
	outs   map[string]*outModel
	schema map[string][]string
}

func newSubsystems(s *ServerSystem, w *V4World) *subsystems {
	v := &subsystems{s: s, w: w, up: &fakeUpstream{seed: s.w.Seed, fail: w.Fail}, push: map[string]*pusher{},
		tasks: map[string]*taskLines{}, outs: map[string]*outModel{}}
	v.upWeb = httptest.NewServer(v.up)
	v.web = httptest.NewServer(http.HandlerFunc(v.serve))
	return v
}

type attachWriter struct {
	http.ResponseWriter
	on func()
}

func (a *attachWriter) WriteHeader(code int) {
	a.ResponseWriter.WriteHeader(code)
	if code == http.StatusOK {
		a.on()
	}
}

func (a *attachWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

func (v *subsystems) serve(w http.ResponseWriter, r *http.Request) {
	srv := v.srv.Load()
	if srv == nil {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		v.s.BoardSystem.mu.Lock()
		p := v.push[strings.TrimPrefix(r.URL.Path, "/")]
		v.s.BoardSystem.mu.Unlock()
		if p != nil {
			w = &attachWriter{ResponseWriter: w, on: func() {
				p.live.Store(true)
				select {
				case p.attached <- struct{}{}:
				default:
				}
			}}
		}
	}
	srv.ServeHTTP(w, r)
}

func (v *subsystems) setup(ctx context.Context) error {
	s := v.s
	_, err := v.gw.Gateways.Create(ctx, gateway.Gateway{Name: gwName, Transport: gateway.TransportHTTP, URL: v.upWeb.URL,
		CredentialEnv: gwEnv, AllowedRoles: slices.Clone(v.w.Roles), AllowTools: []string{"*"}, DenyTools: slices.Clone(v.w.Deny),
		RateLimits: []gateway.RateLimit{{Tool: "search*", PerMinute: v.w.Rate}}, Budget: gateway.Budget{MaxCallsPerDay: v.w.Calls},
		AllowPrivateHosts: true, Enabled: true})
	if err != nil {
		return err
	}
	if s.pg == nil {
		if v.schema, err = v.readSchema(ctx); err != nil {
			return err
		}
	}
	for _, id := range v.w.Push {
		v.startPusher(id)
	}
	return nil
}

func (v *subsystems) wire(ctx context.Context) error {
	s := v.s
	v.ctx, v.stop = context.WithCancel(context.Background())
	v.gw = gateway.New(gateway.Options{Store: s.st, Now: s.clock.Now, Log: discard,
		Getenv: func(k string) string {
			if k == gwEnv {
				return gwCredential
			}
			return ""
		}})
	oc := agentout.DefaultConfig()
	oc.Rate = ratelimit.Limit{PerMinute: 1 << 20, Burst: 1 << 20}
	oc.FlushEvery = 5 * time.Millisecond
	oc.Now, oc.Log = s.clock.Now, discard
	oc.Scan = func(ctx context.Context, a model.Actor, text string) string {
		return s.b.Scan(ctx, a, board.ChannelOutput, text)
	}
	v.out = agentout.New(s.st, outputDir{s.b}, oc)
	if err := v.out.Restore(ctx); err != nil {
		return err
	}
	tc := transcript.DefaultConfig()
	tc.Dir, tc.FlushEvery, tc.CheckEvery, tc.Now, tc.Log = filepath.Join(s.dir, "transcripts"), 24*time.Hour, time.Millisecond, s.clock.Now, discard
	tr, err := transcript.New(s.st, transcriptTasks{s.b}, tc)
	if err != nil {
		return err
	}
	v.tr = tr
	v.out.SetSink(tr.Add)
	v.guard = leakguard.New(leakguard.Options{Board: s.b, Store: s.st, Agents: s.cache, Pub: s.BoardSystem.hub, Log: discard, Now: s.clock.Now})
	s.b.SetLeakScanner(v.guard.Scan)
	do := drill.Options{Store: s.st, Backups: backupList{s}, Dir: filepath.Join(s.dir, "drill"), Pub: s.BoardSystem.hub, Now: s.clock.Now, Log: discard}
	if s.pg != nil {
		do.Unsupported = "postgres backups are not restorable by drills"
	}
	if v.drill, err = drill.New(do); err != nil {
		return err
	}
	lctx := v.ctx
	v.wg.Go(func() { v.out.Run(lctx) })
	v.wg.Go(func() { v.tr.Run(lctx) })
	v.restoreOutputs(ctx)
	return nil
}

func (v *subsystems) extend(r *mcp.Registry) {
	if v.gw == nil {
		return
	}
	r.AddSource(v.gw)
	mcp.RegisterOutput(r, v.out)
}

func (v *subsystems) hook(srv *mcp.Server) {
	if v.ctx == nil {
		return
	}
	p := mcp.NewPush()
	p.Tasks = taskOwners{v.s.b}
	w := &mcp.PushWatcher{Push: p}
	ctx, hub := v.ctx, v.s.hub
	p.Start = func() { w.Activate(ctx, hub) }
	srv.Push = p
	v.srv.Store(srv)
}

func (v *subsystems) down(ctx context.Context, crash bool) {
	v.stop()
	v.wg.Wait()
	if !crash {
		if err := v.tr.Flush(ctx, ""); err != nil {
			v.s.fault(InvTranscript, "flush on shutdown: %v", err)
		}
		for _, tl := range v.tasks {
			tl.durable = len(tl.lines)
		}
	}
	v.gw.Close()
	v.srv.Store(nil)
	v.web.CloseClientConnections()
	v.waitAsleep()
	for _, tl := range v.tasks {
		tl.lines = tl.lines[:tl.durable]
		tl.next = 1
		if n := len(tl.lines); n > 0 {
			tl.next = tl.lines[n-1].off + 1
		}
	}
}

func (v *subsystems) restart(ctx context.Context, crash bool) error {
	if v.schema == nil || v.s.pg != nil {
		return nil
	}
	got, err := v.readSchema(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, v.schema) {
		v.s.fault(InvSchema, "schema changed across %s restart: %s", map[bool]string{true: "crash", false: "graceful"}[crash], schemaDiff(v.schema, got))
	}
	v.schema = got
	want, err := expectedSchema(ctx)
	if err != nil {
		return err
	}
	for table, cols := range want {
		for _, c := range cols {
			if !slices.Contains(got[table], c) {
				v.s.fault(InvSchema, "column %s.%s missing after restart", table, c)
				return nil
			}
		}
	}
	return nil
}

var (
	schemaOnce sync.Once
	schemaWant map[string][]string
	schemaErr  error
)

func expectedSchema(ctx context.Context) (map[string][]string, error) {
	schemaOnce.Do(func() {
		schemaWant, schemaErr = sqlite.ExpectedSchema(ctx)
		for _, cols := range schemaWant {
			slices.Sort(cols)
		}
	})
	return schemaWant, schemaErr
}

func (v *subsystems) readSchema(ctx context.Context) (out map[string][]string, err error) {
	db, err := sql.Open("sqlite", "file:"+v.s.path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	out, err = sqlite.ReadSchema(ctx, db)
	for _, cols := range out {
		slices.Sort(cols)
	}
	return out, err
}

func schemaDiff(a, b map[string][]string) string {
	for t, cols := range a {
		if !slices.Equal(cols, b[t]) {
			return fmt.Sprintf("%s %v → %v", t, cols, b[t])
		}
	}
	for t, cols := range b {
		if _, ok := a[t]; !ok {
			return fmt.Sprintf("new table %s %v", t, cols)
		}
	}
	return "?"
}

func (v *subsystems) close() {
	for _, p := range v.push {
		p.cancel()
	}
	if v.stop != nil {
		v.stop()
		v.wg.Wait()
	}
	if v.gw != nil {
		v.gw.Close()
	}
	v.web.CloseClientConnections()
	v.web.Close()
	v.upWeb.Close()
}

func (v *subsystems) token(id string, n int) string {
	v.s.BoardSystem.mu.Lock()
	defer v.s.BoardSystem.mu.Unlock()
	toks := v.s.tokens[id]
	if n < 0 {
		n = len(toks) - 1
	}
	if n >= 0 && n < len(toks) {
		return toks[n]
	}
	return ""
}

func (v *subsystems) startPusher(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pusher{agent: id, cancel: cancel, gate: make(chan struct{}, 1), asleep: make(chan struct{}, 1),
		attached: make(chan struct{}, 1), done: make(chan struct{})}
	p.c = client.NewMCP(v.web.URL+"/"+id, v.token(id, -1), client.WithSleep(func(ctx context.Context, _ time.Duration) error {
		p.live.Store(false)
		select {
		case p.asleep <- struct{}{}:
		default:
		}
		select {
		case <-p.gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	v.s.BoardSystem.mu.Lock()
	if old := v.push[id]; old != nil {
		old.cancel()
	}
	v.push[id] = p
	v.s.BoardSystem.mu.Unlock()
	ch := p.c.Notifications(ctx)
	go func() {
		for n := range ch {
			p.mu.Lock()
			p.got = append(p.got, n)
			p.mu.Unlock()
		}
		p.live.Store(false)
		close(p.done)
	}()
	v.connect(p)
}

func (v *subsystems) connect(p *pusher) {
	deadline := time.After(pushWait)
	for !p.live.Load() {
		select {
		case <-p.asleep:
			p.gate <- struct{}{}
		case <-p.attached:
		case <-p.done:
			return
		case <-deadline:
			v.s.fault(InvHarness, "push client of %s did not reconnect within %s", p.agent, pushWait)
			return
		}
	}
	select {
	case <-p.attached:
	default:
	}
}

func (v *subsystems) waitAsleep() {
	deadline := time.Now().Add(pushWait)
	for _, p := range v.push {
		for p.live.Load() && !p.closed() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
}

func (v *subsystems) mark() {
	for _, p := range v.push {
		p.mark = p.received()
	}
}

func (v *subsystems) observe(prev, cur *State) {
	if prev == nil {
		return
	}
	for _, t := range cur.Tasks {
		p := v.push[deref(t.Assignee)]
		if p == nil || p.closed() || t.Status != model.StatusAssigned {
			continue
		}
		if o := prev.Task(t.ID); o != nil && o.Status == model.StatusAssigned && deref(o.Assignee) == p.agent {
			continue
		}
		p.mu.Lock()
		p.expect = append(p.expect, wake{task: t.ID, mark: p.mark})
		p.mu.Unlock()
	}
}

func (v *subsystems) settlePush(ctx context.Context) {
	for _, id := range v.w.Push {
		p := v.push[id]
		if p.closed() {
			a, err := v.s.b.GetAgent(ctx, id)
			if err == nil && a.Status == model.AgentActive {
				v.startPusher(id)
			}
			continue
		}
		v.connect(p)
		if p.closed() {
			continue
		}
		p.mu.Lock()
		pending := slices.Clone(p.expect)
		p.expect = nil
		p.mu.Unlock()
		deadline := time.Now().Add(pushWait)
		for _, w := range pending {
			for !p.woken(w) && !p.closed() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !p.woken(w) && !p.closed() {
				v.s.fault(InvPush, "%s was assigned %s but its push stream delivered neither task_assigned nor resync", id, w.task)
				return
			}
		}
	}
}

func (v *subsystems) do(ctx context.Context, op Op) (Result, bool) {
	var res Result
	var err error
	switch op.Kind {
	case OpGatewayCall:
		res, err = v.gatewayCall(ctx, op)
	case OpGatewayList:
		var tools []client.ToolInfo
		if tools, err = v.s.client(op).ListTools(ctx); err == nil {
			res.Hits = []string{}
			for _, t := range tools {
				if strings.HasPrefix(t.Name, gateway.Prefix) {
					res.Hits = append(res.Hits, t.Name)
				}
			}
			slices.Sort(res.Hits)
		}
	case OpLogOutput:
		res, err = v.logOutput(ctx, op, op.Text, op.Text)
	case OpTranscriptRead:
		err = v.readTranscript(ctx, op.Task)
	case OpLeak:
		res, err = v.leak(ctx, op)
	case OpReissue:
		res, err = v.reissue(ctx, op.Target)
	case OpDrill:
		var r *drill.Result
		if r, err = v.drill.RunOnce(ctx, "manual"); err == nil {
			res.Status, res.Hits = r.Status, []string{r.Error}
			v.checkDrillDir()
		}
	case OpPushCut:
		v.web.CloseClientConnections()
		v.waitAsleep()
	case OpPushSettle:
		v.settlePush(ctx)
	default:
		return res, false
	}
	res.Err = toolErr(err)
	return res, true
}

func (v *subsystems) gatewayCall(ctx context.Context, op Op) (Result, error) {
	before := v.up.count()
	var out struct {
		IsError bool `json:"is_error"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	err := v.s.client(op).Call(ctx, gateway.ToolName(gwName, op.Target), map[string]any{"q": "dst"}, &out)
	res := Result{Upstream: v.up.count() - before}
	if err != nil {
		return res, err
	}
	res.Status = "ok"
	if out.IsError {
		res.Status = "tool_error"
	}
	for _, c := range out.Content {
		res.Hits = append(res.Hits, c.Text)
	}
	return res, nil
}

func outputLines(text string) []string { return strings.Split(text, "\n") }

func (v *subsystems) logOutput(ctx context.Context, op Op, text, expect string) (Result, error) {
	in := agentout.Input{TaskID: op.Task, Stream: "stdout", Text: text}
	if op.N > 0 {
		seq := int64(op.N)
		in.Seq = &seq
	}
	r, err := v.s.client(op).LogOutput(ctx, in)
	if err != nil {
		return Result{}, err
	}
	m := v.outs[op.Actor]
	if m == nil {
		m = &outModel{texts: map[int64]string{}}
		v.outs[op.Actor] = m
	}
	res := Result{Status: "appended"}
	if r.Duplicate {
		res.Status = "duplicate"
	}
	dup := in.Seq != nil && *in.Seq <= m.client
	switch {
	case dup != r.Duplicate:
		v.s.fault(InvOutput, "log_output by %s with seq %d: duplicate %t, last accepted seq %d", op.Actor, op.N, r.Duplicate, m.client)
	case r.Duplicate:
		if r.Lines != 0 || r.Seq != m.last {
			v.s.fault(InvOutput, "duplicate log_output by %s appended %d lines (seq %d, was %d)", op.Actor, r.Lines, r.Seq, m.last)
		}
		return res, nil
	}
	if in.Seq != nil {
		m.client = *in.Seq
	}
	want := outputLines(expect)
	if r.Lines != len(want) || r.Seq != m.last+int64(len(want)) {
		v.s.fault(InvOutput, "log_output by %s of %d lines returned seq %d with %d lines, last seq was %d", op.Actor, len(want), r.Seq, r.Lines, m.last)
		return res, nil
	}
	o, err := v.out.Since(ctx, op.Actor, m.last, len(want), "")
	if err != nil {
		return res, err
	}
	for i, l := range o.Lines {
		if l.Seq != m.last+int64(i)+1 || l.Text != want[i] || deref(l.TaskID) != op.Task {
			v.s.fault(InvOutput, "output line %d of %s is #%d %q task %q, want #%d %q task %q", i, op.Actor, l.Seq, l.Text, deref(l.TaskID), m.last+int64(i)+1, want[i], op.Task)
			return res, nil
		}
	}
	if len(o.Lines) != len(want) {
		v.s.fault(InvOutput, "output of %s holds %d of %d new lines", op.Actor, len(o.Lines), len(want))
	}
	for i, t := range want {
		m.texts[m.last+int64(i)+1] = t
	}
	m.last = r.Seq
	if op.Task != "" {
		tl := v.tasks[op.Task]
		if tl == nil {
			tl = &taskLines{next: 1}
			v.tasks[op.Task] = tl
		}
		for _, t := range want {
			tl.lines = append(tl.lines, tline{off: tl.next, text: t})
			tl.next++
		}
	}
	res.Seq = r.Seq
	return res, nil
}

func (v *subsystems) restoreOutputs(ctx context.Context) {
	for id, m := range v.outs {
		o, err := v.out.Since(ctx, id, 0, agentout.MaxLimit, "")
		if err != nil {
			v.s.fault(InvOutput, "read restored output of %s: %v", id, err)
			continue
		}
		for _, l := range o.Lines {
			if t, ok := m.texts[l.Seq]; !ok || t != l.Text {
				v.s.fault(InvOutput, "restored output line #%d of %s is %q, was %q (known %t)", l.Seq, id, l.Text, t, ok)
				break
			}
		}
		if o.LastSeq > m.last {
			v.s.fault(InvOutput, "restored output of %s ends at #%d beyond the last line #%d", id, o.LastSeq, m.last)
		}
		m.last, m.client = o.LastSeq, 0
	}
}

func (v *subsystems) readTranscript(ctx context.Context, task string) error {
	p, err := v.tr.Read(ctx, task, transcript.ReadOptions{Limit: transcript.MaxLimit})
	if err != nil {
		return err
	}
	tl := v.tasks[task]
	if tl == nil {
		tl = &taskLines{next: 1}
	}
	tl.durable = len(tl.lines)
	v.compareTranscript(task, tl, p.Lines)
	return nil
}

func (v *subsystems) compareTranscript(task string, tl *taskLines, got []transcript.Entry) {
	i := 0
	for _, e := range got {
		for i < len(tl.lines) && tl.lines[i].off < e.Offset {
			i++
		}
		if i == len(tl.lines) || tl.lines[i].off != e.Offset || tl.lines[i].text != e.Text {
			v.s.fault(InvTranscript, "transcript of %s has line %d %q that was never logged there", task, e.Offset, e.Text)
			return
		}
		i++
	}
	if i < tl.durable && len(got) > 0 {
		v.s.fault(InvTranscript, "transcript of %s ends at offset %d, %d lines logged", task, got[len(got)-1].Offset, tl.durable)
	}
	if len(got) == 0 && tl.durable > 0 && v.s.w.Server.RetainDays == 0 {
		v.s.fault(InvTranscript, "transcript of %s is empty, %d lines logged", task, tl.durable)
	}
}

func (v *subsystems) settleTranscripts(ctx context.Context, s *State) {
	for id, tl := range v.tasks {
		if tl.durable == len(tl.lines) {
			continue
		}
		t := s.Task(id)
		if t == nil || !slices.Contains(flushStatuses, t.Status) {
			continue
		}
		want := tl.lines[len(tl.lines)-1].off
		deadline := time.Now().Add(pushWait)
		for {
			st, err := v.tr.Stats(ctx, []string{id})
			if err != nil {
				v.s.fault(InvTranscript, "stats of %s: %v", id, err)
				return
			}
			if st[id].LastOffset >= want {
				break
			}
			if time.Now().After(deadline) {
				v.s.fault(InvTranscript, "%s is %s but its transcript ends at offset %d, %d lines logged", id, t.Status, st[id].LastOffset, want)
				return
			}
			time.Sleep(time.Millisecond)
		}
		tl.durable = len(tl.lines)
	}
}

func (v *subsystems) checkDrillDir() {
	es, err := os.ReadDir(filepath.Join(v.s.dir, "drill"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		v.s.fault(InvDrill, "read drill dir: %v", err)
		return
	}
	for _, e := range es {
		v.s.fault(InvDrill, "drill left %s behind", e.Name())
		return
	}
}

func (v *subsystems) leakText(op Op) string {
	return "context " + v.token(op.Target, op.N) + " end"
}

func (v *subsystems) leak(ctx context.Context, op Op) (Result, error) {
	tok := v.token(op.Target, op.N)
	text := v.leakText(op)
	c := v.s.client(op)
	var err error
	switch op.Text {
	case board.ChannelMessage:
		_, err = c.PostMessage(ctx, "agent:"+op.Actor, text)
	case board.ChannelNote:
		_, err = c.AddNote(ctx, op.Task, text)
	case board.ChannelQuestion:
		_, err = c.AskOwner(ctx, op.Task, text, "a", "b")
	case board.ChannelSummary:
		_, err = c.Submit(ctx, board.SubmitInput{TaskID: op.Task, PRURL: prURL(op.Task), Summary: text})
	case board.ChannelStatus:
		_, err = c.UpdateStatus(ctx, board.StatusInput{TaskID: op.Task, Status: model.StatusInProgress, Note: text})
	case board.ChannelTests:
		_, err = c.ReportTests(ctx, op.Task, 3, 0, text)
	case board.ChannelReview:
		_, err = c.Review(ctx, op.Task, "changes_requested", text)
	case board.ChannelOutput:
		want := redact.String(text)
		if a, err := v.s.b.GetAgent(ctx, op.Target); err == nil && a.Status != model.AgentRevoked && tok == v.token(op.Target, -1) {
			want = redact.String(strings.ReplaceAll(text, tok, leakMask))
		}
		_, err = v.logOutput(ctx, Op{Actor: op.Actor, Token: op.Token, Task: op.Task}, text, want)
	case board.ChannelHelp:
		_, err = c.RequestHelp(ctx, op.Task, "unclear_spec", text)
	}
	v.findToken(ctx, op, tok)
	return Result{}, err
}

func (v *subsystems) findToken(ctx context.Context, op Op, tok string) {
	if tok == "" {
		return
	}
	s := v.s
	threads := [][2]string{{model.ThreadAgent, op.Actor}, {model.ThreadAgent, op.Target}}
	if op.Task != "" {
		threads = append(threads, [2]string{model.ThreadTask, op.Task})
	}
	for _, th := range threads {
		ms, err := s.b.OwnerThread(ctx, th[0], th[1], 0, 100000)
		if err != nil {
			continue
		}
		for _, m := range ms {
			if strings.Contains(m.Body, tok) {
				s.fault(InvLeak, "token of %s stored in %s:%s message %d via %s", op.Target, th[0], th[1], m.ID, op.Text)
				return
			}
		}
	}
	o, err := v.out.Since(ctx, op.Actor, 0, agentout.MaxLimit, "")
	if err == nil {
		for _, l := range o.Lines {
			if strings.Contains(l.Text, tok) {
				s.fault(InvLeak, "token of %s kept in the output of %s", op.Target, op.Actor)
				return
			}
		}
	}
	added, err := s.b.ListAudit(ctx, auditSince(s.audit))
	if err != nil {
		return
	}
	for _, e := range added {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), tok) {
			s.fault(InvLeak, "token of %s written to audit event %s", op.Target, e.Tool)
			return
		}
	}
}

func (v *subsystems) reissue(ctx context.Context, agent string) (Result, error) {
	id := "L-0"
	for _, e := range v.s.audit {
		if e.Tool == "token_leak_detected" && str(e.Before["agent"]) == agent {
			id = str(e.After["leak_id"])
		}
	}
	r, err := v.guard.Reissue(ctx, id)
	if err != nil {
		return Result{}, err
	}
	v.s.BoardSystem.mu.Lock()
	v.s.tokens[agent] = append(v.s.tokens[agent], r.Connect.Token)
	v.s.BoardSystem.mu.Unlock()
	return Result{ID: id, Rotated: []string{agent}}, nil
}

type V4State struct {
	World    *V4World
	Quota    map[string]quota.Today
	Upstream []string
}

func (v *subsystems) snapshot(ctx context.Context, prev, cur *State, quotas bool) (*V4State, error) {
	v.observe(prev, cur)
	v.settleTranscripts(ctx, cur)
	out := &V4State{World: v.w}
	v.up.mu.Lock()
	out.Upstream = slices.Clone(v.up.outcomes)
	v.up.mu.Unlock()
	if !quotas {
		return out, nil
	}
	sn, err := v.s.f.quota.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out.Quota = map[string]quota.Today{}
	for _, a := range cur.Agents {
		_, out.Quota[a.ID] = sn.Of(a.ID)
	}
	return out, nil
}

func auditSince(es []*model.AuditEvent) store.AuditFilter {
	f := store.AuditFilter{Limit: 1 << 30}
	if n := len(es); n > 0 {
		f.SinceID = es[n-1].ID
	}
	return f
}
