package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	runBudget     = 60 * time.Second
	workerCount   = 5
	askTitle      = "Define the money type"
	defectTitle   = "Expose the cart API"
	ownerAnswer   = "cents"
	leaderTokensI = 1200
	leaderTokensO = 640
)

type planTask struct {
	Ref                string       `json:"ref"`
	Title              string       `json:"title"`
	Spec               string       `json:"spec"`
	AcceptanceCriteria []string     `json:"acceptance_criteria"`
	Repo               string       `json:"repo"`
	DependsOn          []string     `json:"depends_on"`
	Role               string       `json:"role"`
	Budget             model.Budget `json:"budget"`
}

var samplePlan = []planTask{
	{Ref: "money", Title: askTitle, DependsOn: []string{}, Role: "worker",
		Spec:               "Add a Money type in internal/money with integer minor units and formatting helpers.",
		AcceptanceCriteria: []string{"Money round-trips through JSON", "go test ./internal/money passes"}},
	{Ref: "config", Title: "Load shop settings", DependsOn: []string{}, Role: "worker",
		Spec:               "Read shop settings from the environment in internal/config with validation errors.",
		AcceptanceCriteria: []string{"invalid settings are rejected with a clear error", "go test ./internal/config passes"}},
	{Ref: "cart", Title: defectTitle, DependsOn: []string{"money"}, Role: "worker",
		Spec:               "Serve GET /cart and POST /cart/items from internal/api using the Money type.",
		AcceptanceCriteria: []string{"GET /cart returns 200 with a JSON body", "an integration test covers adding an item"}},
	{Ref: "cli", Title: "Add the shopctl command", DependsOn: []string{"config", "cart"}, Role: "worker",
		Spec:               "Add cmd/shopctl that prints the cart through the API and honours the shop settings.",
		AcceptanceCriteria: []string{"shopctl cart prints every item", "go build ./cmd/shopctl succeeds"}},
	{Ref: "web", Title: "Render the cart page", DependsOn: []string{"cart"}, Role: "worker",
		Spec:               "Render the cart in web/app/cart/page.tsx as a table with name and price.",
		AcceptanceCriteria: []string{"the page lists every cart item", "pnpm build succeeds"}},
	{Ref: "e2e", Title: "End-to-end checkout test", DependsOn: []string{"cli", "web"}, Role: "tester",
		Spec:               "Write an end-to-end test that fills a cart through the API and reads it back in the CLI and the page.",
		AcceptanceCriteria: []string{"the test fails without the cart API", "the test passes in CI with make test"}},
}

func plannedDeps() map[string][]string {
	titles := map[string]string{}
	for _, p := range samplePlan {
		titles[p.Ref] = p.Title
	}
	out := map[string][]string{}
	for _, p := range samplePlan {
		for _, d := range p.DependsOn {
			out[p.Title] = append(out[p.Title], titles[d])
		}
	}
	return out
}

func writeJSONFile(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func leaderModelFile(t *testing.T) string {
	tasks := make([]planTask, len(samplePlan))
	for i, p := range samplePlan {
		p.Repo = repoName
		p.Budget = model.Budget{MaxMinutes: 60, MaxCostEUR: 5}
		tasks[i] = p
	}
	return writeJSONFile(t, "leader-model.json", map[string]any{"responses": []any{map[string]any{
		"usage": map[string]int{"input_tokens": leaderTokensI, "output_tokens": leaderTokensO},
		"json":  map[string]any{"note": "Money first, then the API, then the clients and the end-to-end test.", "tasks": tasks},
	}}})
}

type fleet struct {
	bins      binaries
	forge     *forge
	git       *gitRepo
	sink      *sink
	bus       *bus
	env       *servertest.Env
	owner     *client.Owner
	world     *world
	agents    *agentEnv
	tokens    map[string]string
	workers   []*scripted
	tester    *scripted
	leaderOut string
	brief     *model.Brief
	script    *ownerScript
}

func startServer(t *testing.T, f *forge, g *gitRepo, s *sink) *servertest.Env {
	t.Helper()
	dir := t.TempDir()
	mq := writeJSONFile(t, "mq.json", map[string]any{
		"workdir": filepath.Join(dir, "work"), "interval": "20ms",
		"repos": map[string]any{repoName: map[string]any{"remote": g.remote, "verify": []string{"true"}, "train": 8}},
	})
	ps := writeJSONFile(t, "prsync.json", map[string]any{
		"rate":  map[string]any{"per_minute": 0},
		"hosts": map[string]any{"forge": map[string]any{"kind": "forgejo", "url": f.url(), "rate": map[string]any{"per_minute": 0}}},
		"repos": map[string]string{repoName: "forge"},
	})
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "20ms")
	t.Setenv("SUBSPACE_MQ_CONFIG", mq)
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", ps)
	t.Setenv("SUBSPACE_PRSYNC_INTERVAL", "100ms")
	t.Setenv("SUBSPACE_NOTIFY_WEBHOOK_URL", s.srv.URL)
	t.Setenv("SUBSPACE_NOTIFY_ALLOW_PRIVATE", "true")
	return servertest.Start(t, server.Config{PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

func (fl *fleet) register(ctx context.Context, id, role, kind, mdl string) error {
	r, err := fl.owner.RegisterAgent(ctx, board.AgentInput{ID: id, Role: role, Kind: kind, Model: mdl})
	if err != nil {
		return fmt.Errorf("register %s: %w", id, err)
	}
	fl.tokens[id] = r.Connect.Token
	return nil
}

func (fl *fleet) setup(ctx context.Context, t *testing.T) error {
	fl.owner = client.NewOwner(fl.env.TS.URL, servertest.OwnerToken)
	fl.world = newWorld(fl.owner, fl.bus)
	fl.agents = &agentEnv{url: fl.env.TS.URL, world: fl.world, bus: fl.bus, forge: fl.forge, git: fl.git, ledger: &ledger{},
		sc: scenario{askTitle: askTitle, defectTitle: defectTitle, answer: ownerAnswer}}
	fl.tokens = map[string]string{}
	regs := []struct{ id, role, kind, mdl string }{
		{"leader-1", model.RoleLeader, model.KindOther, "claude-opus-5-5"},
		{"tester-1", model.RoleTester, model.KindClaudeCode, "claude-haiku-4-5"},
		{"reviewer-1", model.RoleReviewer, model.KindCodex, "gpt-5"},
		{"reviewer-2", model.RoleReviewer, model.KindOpenCode, "deepseek-chat"},
	}
	for i := 1; i <= workerCount; i++ {
		regs = append(regs, struct{ id, role, kind, mdl string }{fmt.Sprintf("worker-%d", i), model.RoleWorker, model.KindClaudeCode, "claude-sonnet-5-5"})
	}
	for _, r := range regs {
		if err := fl.register(ctx, r.id, r.role, r.kind, r.mdl); err != nil {
			return err
		}
	}
	b, err := fl.owner.CreateBrief(ctx, board.BriefInput{Title: "Shop checkout", Body: "Build the cart API, a CLI and a page for it, with an end-to-end test.",
		Repos: []string{repoName}, MaxCostEUR: 50, MaxParallel: 10})
	if err != nil {
		return err
	}
	fl.brief = b
	return fl.owner.Do(ctx, "autopilot", http.MethodPost, "/api/v1/autopilot/briefs/"+b.ID, map[string]bool{"enabled": true}, nil)
}

func (fl *fleet) startAgents(ctx context.Context, g *group) ([]*proc, error) {
	g.run(ctx, "world", fl.world.run)
	for i := 1; i <= workerCount; i++ {
		id := fmt.Sprintf("worker-%d", i)
		w, err := newScripted(ctx, fl.agents, id, fl.tokens[id])
		if err != nil {
			return nil, err
		}
		fl.workers = append(fl.workers, w)
		g.run(ctx, id, w.run)
	}
	tester, err := newScripted(ctx, fl.agents, "tester-1", fl.tokens["tester-1"])
	if err != nil {
		return nil, err
	}
	fl.tester = tester
	g.run(ctx, "tester-1", tester.run)
	var procs []*proc
	for _, id := range []string{"reviewer-1", "reviewer-2"} {
		p, err := startProc(ctx, fl.bins.reviewer, fl.tokens[id], "-url", fl.env.TS.URL, "-fake-model", "approve",
			"-githost", "forgejo", "-githost-url", fl.forge.url(), "-interval", "50ms")
		if err != nil {
			return nil, err
		}
		procs = append(procs, p)
	}
	fl.script = newOwnerScript(fl.owner, fl.agents, fl.sink)
	g.run(ctx, "owner", fl.script.run)
	return procs, nil
}

func (fl *fleet) runLeader(ctx context.Context, t *testing.T) error {
	p, err := startProc(ctx, fl.bins.leader, fl.tokens["leader-1"], "-url", fl.env.TS.URL, "-fake-model", leaderModelFile(t),
		"-brief", fl.brief.ID, "-once")
	if err != nil {
		return err
	}
	if err := p.wait(); err != nil {
		return fmt.Errorf("leader: %w\n%s", err, p.stderr.String())
	}
	fl.leaderOut = p.stdout.String()
	return nil
}

func (fl *fleet) settled() bool {
	v := fl.world.view()
	for id, task := range v.tasks {
		if task.PRURL == nil {
			return false
		}
		sha := fl.forge.pr(fl.forge.prNumber(*task.PRURL)).SHA
		if p, ok := v.prs[id]; !ok || p.HeadSHA != sha || p.Checks != "success" || fl.forge.state(sha) != "success" {
			return false
		}
	}
	return true
}

func settle(t *testing.T, base int) {
	t.Helper()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > base+2 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines leaked: %d, base %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func scanLines(s string) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestAcceptanceSwarm(t *testing.T) {
	bins := buildBinaries(t)
	prev := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(prev)
	base := runtime.NumGoroutine()
	started := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), runBudget-5*time.Second)
	defer cancel()
	b := newBus()
	fl := &fleet{bins: bins, bus: b, forge: newForge(b), git: newGitRepo(t), sink: newSink(b)}
	fl.env = startServer(t, fl.forge, fl.git, fl.sink)
	runCtx, stopAgents := context.WithCancel(ctx)
	g := newGroup(stopAgents)

	var procs []*proc
	err := fl.setup(runCtx, t)
	if err == nil {
		procs, err = fl.startAgents(runCtx, g)
	}
	if err == nil {
		err = fl.runLeader(runCtx, t)
	}
	if err == nil {
		err = b.until(runCtx, func() bool { return fl.world.view().allDone() })
	}
	if err == nil {
		err = b.until(runCtx, fl.settled)
	}
	stopAgents()
	errs := g.wait()
	for _, p := range procs {
		_ = p.wait()
	}
	if err != nil || len(errs) > 0 {
		t.Errorf("scenario failed: %v %v\n%s", err, errs, fl.dump(procs))
	} else {
		fl.verify(ctx, t)
	}
	fl.env.Stop()
	fl.forge.close()
	fl.sink.close()
	settle(t, base)
	if d := time.Since(started); d > runBudget {
		t.Errorf("took %s, budget %s", d, runBudget)
	}
}
