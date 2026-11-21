package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	exitOK    = 0
	exitUsage = 1
	exitError = 2

	defaultModel   = "claude-opus-5-5"
	defaultBaseURL = "https://api.anthropic.com"
	mcpTimeout     = 30 * time.Second
)

type options struct {
	url            string
	token          string
	briefs         []string
	model          string
	anthropicURL   string
	apiKey         string
	fakeModel      string
	once           bool
	interval       time.Duration
	requestTimeout time.Duration
	retries        int
	backoff        time.Duration
	maxBackoff     time.Duration
	maxTokens      int64
	maxOutput      int
	planAttempts   int
	maxTasks       int
	agentLoad      int
	reviewRounds   int
	spawnKind      string
	spawnModel     string
	spawnProvider  string
	spawnCooldown  time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	o, err := parseOptions(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-leader:", err)
		return exitUsage
	}
	l, err := newLeader(o, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-leader:", err)
		return exitUsage
	}
	err = l.run(ctx)
	l.emit("usage", map[string]any{"usage": l.m.Ledger()})
	if err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, "subspace-leader:", l.redact(err.Error()))
		return exitError
	}
	return exitOK
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	var o options
	var briefs string
	fs := flag.NewFlagSet("subspace-leader", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.url, "url", getenv("SUBSPACE_URL"), "Subspace base URL (default $SUBSPACE_URL)")
	fs.StringVar(&o.token, "token", "", "leader agent token (default $SUBSPACE_TOKEN_FILE contents, then $SUBSPACE_TOKEN)")
	fs.StringVar(&briefs, "brief", "", "comma-separated brief ids to lead (default: every open brief)")
	fs.StringVar(&o.model, "model", defaultModel, "Anthropic model")
	fs.StringVar(&o.anthropicURL, "anthropic-url", defaultBaseURL, "Anthropic API base URL")
	fs.StringVar(&o.fakeModel, "fake-model", "", "read canned model responses from this JSON file instead of calling the API")
	fs.BoolVar(&o.once, "once", false, "plan and run one monitoring pass, then exit")
	fs.DurationVar(&o.interval, "interval", 30*time.Second, "monitoring interval")
	fs.DurationVar(&o.requestTimeout, "request-timeout", 3*time.Minute, "timeout per model request")
	fs.IntVar(&o.retries, "retries", 4, "model request attempts on retryable errors")
	fs.DurationVar(&o.backoff, "backoff", 2*time.Second, "initial retry backoff, doubled per attempt")
	fs.DurationVar(&o.maxBackoff, "max-backoff", time.Minute, "maximum wait between retries")
	fs.Int64Var(&o.maxTokens, "max-tokens", 400000, "total model token budget for this process (0 = unlimited)")
	fs.IntVar(&o.maxOutput, "max-output-tokens", 16000, "max output tokens per model request")
	fs.IntVar(&o.planAttempts, "plan-attempts", 3, "model attempts to produce a valid plan per brief")
	fs.IntVar(&o.maxTasks, "max-tasks", 40, "maximum tasks per plan")
	fs.IntVar(&o.agentLoad, "agent-load", 1, "open tasks an agent may hold before the leader stops assigning to it")
	fs.IntVar(&o.reviewRounds, "review-rounds", 2, "changes_requested rounds after which the owner is asked")
	fs.StringVar(&o.spawnKind, "spawn-kind", "", "worker kind to request when no agent is free (empty = never spawn)")
	fs.StringVar(&o.spawnModel, "spawn-model", "", "model for spawned workers")
	fs.StringVar(&o.spawnProvider, "spawn-provider", "", "provider for spawned workers (default from kind)")
	fs.DurationVar(&o.spawnCooldown, "spawn-cooldown", 10*time.Minute, "wait before requesting more agents for the same brief and role")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.url = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(o.url), "/"), "/mcp")
	if o.url == "" {
		return o, errors.New("-url or SUBSPACE_URL is required")
	}
	var err error
	if o.token == "" {
		if o.token, err = secret(getenv, "SUBSPACE_TOKEN"); err != nil {
			return o, err
		}
	}
	o.token = strings.TrimSpace(o.token)
	if o.token == "" {
		return o, errors.New("-token, SUBSPACE_TOKEN_FILE or SUBSPACE_TOKEN is required")
	}
	for _, b := range strings.Split(briefs, ",") {
		if b = strings.TrimSpace(b); b != "" {
			o.briefs = append(o.briefs, b)
		}
	}
	if o.fakeModel == "" {
		if o.apiKey, err = secret(getenv, "ANTHROPIC_API_KEY"); err != nil {
			return o, err
		}
		if o.apiKey = strings.TrimSpace(o.apiKey); o.apiKey == "" {
			return o, errors.New("ANTHROPIC_API_KEY_FILE or ANTHROPIC_API_KEY is required (or -fake-model)")
		}
	}
	switch {
	case o.model == "":
		return o, errors.New("-model is required")
	case o.interval <= 0, o.requestTimeout <= 0:
		return o, errors.New("-interval and -request-timeout must be positive")
	case o.retries < 1, o.planAttempts < 1, o.maxTasks < 1, o.maxOutput < 1, o.agentLoad < 1, o.reviewRounds < 1:
		return o, errors.New("-retries, -plan-attempts, -max-tasks, -max-output-tokens, -agent-load and -review-rounds must be at least 1")
	case o.maxTokens < 0:
		return o, errors.New("-max-tokens must not be negative")
	}
	switch o.spawnKind {
	case "":
	case model.KindClaudeCode, model.KindCodex, model.KindOpenCode:
		if o.spawnModel == "" {
			return o, errors.New("-spawn-kind requires -spawn-model")
		}
		if o.spawnProvider == "" {
			o.spawnProvider = model.ProviderForKind(o.spawnKind)
		}
	default:
		return o, fmt.Errorf("unknown -spawn-kind %q (want claude-code, codex or opencode)", o.spawnKind)
	}
	return o, nil
}

func secret(getenv func(string) string, name string) (string, error) {
	if f := getenv(name + "_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		return string(b), nil
	}
	return getenv(name), nil
}

type briefKey struct{}

type leader struct {
	o   options
	c   *client.MCP
	m   *Metered
	log *slog.Logger
	now func() time.Time

	outMu sync.Mutex
	out   io.Writer

	usageOff     bool
	planned      map[string]bool
	asked        map[string]bool
	assignFailed map[string]time.Time
	spawnedAt    map[string]time.Time
}

func newLeader(o options, stdout, stderr io.Writer) (*leader, error) {
	l := &leader{
		o: o, out: stdout, now: time.Now,
		log:     slog.New(slog.NewTextHandler(stderr, nil)),
		c:       client.NewMCP(o.url+"/mcp", o.token, client.WithHTTPClient(&http.Client{Timeout: mcpTimeout}), client.WithClientName("subspace-leader")),
		planned: map[string]bool{}, asked: map[string]bool{}, assignFailed: map[string]time.Time{}, spawnedAt: map[string]time.Time{},
	}
	var base Model
	if o.fakeModel != "" {
		f, err := loadFake(o.fakeModel, o.model)
		if err != nil {
			return nil, err
		}
		base = f
	} else {
		base = &Anthropic{
			HC: &http.Client{}, URL: o.anthropicURL, Key: o.apiKey, Model: o.model,
			Attempts: o.retries, Timeout: o.requestTimeout, Backoff: o.backoff, MaxBackoff: o.maxBackoff,
			OnRateLimit: l.rateLimited,
		}
	}
	l.m = &Metered{M: base, Budget: o.maxTokens, Report: l.reportUsage}
	return l, nil
}

func (l *leader) redact(s string) string {
	for _, sec := range []string{l.o.token, l.o.apiKey} {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "<redacted>")
		}
	}
	return s
}

func (l *leader) emit(event string, fields map[string]any) {
	fields["event"] = event
	fields["ts"] = l.now().UTC().Format(time.RFC3339)
	l.outMu.Lock()
	defer l.outMu.Unlock()
	_, _ = fmt.Fprintln(l.out, compact(fields))
}

func (l *leader) report(ctx context.Context, u client.UsageReport) {
	if l.usageOff {
		return
	}
	u.BriefID, _ = ctx.Value(briefKey{}).(string)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpTimeout)
	defer cancel()
	if _, err := l.c.ReportUsage(rctx, u); err != nil {
		if strings.HasPrefix(client.CodeOf(err), "rpc") {
			l.usageOff = true
			l.log.Warn("report_usage is not available on this server; usage is only counted locally", "err", err)
			return
		}
		l.log.Warn("report_usage failed", "err", err)
	}
}

func (l *leader) reportUsage(ctx context.Context, r *Reply) {
	mdl := r.Model
	if mdl == "" {
		mdl = l.o.model
	}
	l.report(ctx, client.UsageReport{Provider: "anthropic", Model: mdl, TokensIn: r.Usage.InputTokens, TokensOut: r.Usage.OutputTokens,
		CacheRead: r.Usage.CacheReadInputTokens, CacheWrite: r.Usage.CacheCreationInputTokens, RequestID: r.ID})
}

func (l *leader) rateLimited(ctx context.Context, requestID string) {
	l.m.rateLimited()
	l.report(ctx, client.UsageReport{Provider: "anthropic", Model: l.o.model, RateLimited: true, RequestID: requestID})
}

func (l *leader) join(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, mcpTimeout)
	defer cancel()
	if err := l.c.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	who, err := l.c.WhoAmI(ctx)
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	if who.Agent == nil || who.Agent.Role != model.RoleLeader {
		return errors.New("this token does not belong to a leader agent")
	}
	if _, err := l.c.GetInstructions(ctx); err != nil {
		return fmt.Errorf("get_instructions: %w", err)
	}
	l.emit("joined", map[string]any{"agent": who.Agent.ID})
	return nil
}

func (l *leader) run(ctx context.Context) error {
	if err := l.join(ctx); err != nil {
		return err
	}
	defer func() { _ = l.c.Close(context.WithoutCancel(ctx)) }()
	t := time.NewTicker(l.o.interval)
	defer t.Stop()
	for {
		err := l.round(ctx)
		if l.o.once {
			return err
		}
		if err != nil {
			l.log.Error("leader round", "err", l.redact(err.Error()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (l *leader) briefs(ctx context.Context) ([]*model.Brief, error) {
	if len(l.o.briefs) == 0 {
		return l.c.ListBriefs(ctx, "open")
	}
	out := make([]*model.Brief, 0, len(l.o.briefs))
	for _, id := range l.o.briefs {
		b, err := l.c.GetBrief(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get_brief %s: %w", id, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func (l *leader) round(ctx context.Context) error {
	bs, err := l.briefs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range bs {
		if b.Status != "open" || l.planned[b.ID] || (b.PlanStatus != "" && b.PlanStatus != "none") {
			continue
		}
		l.planned[b.ID] = true
		p, err := l.plan(context.WithValue(ctx, briefKey{}, b.ID), b)
		if err != nil {
			l.emit("plan_failed", map[string]any{"brief": b.ID, "error": l.redact(err.Error())})
			errs = append(errs, fmt.Errorf("plan %s: %w", b.ID, err))
			continue
		}
		l.emit("plan_proposed", map[string]any{"brief": b.ID, "plan": p.ID, "tasks": len(p.Tasks), "warnings": len(p.Warnings)})
	}
	if err := l.monitor(ctx, bs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (l *leader) plan(ctx context.Context, b *model.Brief) (*model.Plan, error) {
	if b.Budget.MaxCostEUR > 0 && b.CostUsed >= b.Budget.MaxCostEUR {
		return nil, fmt.Errorf("brief budget exhausted (%.2f of %.2f EUR)", b.CostUsed, b.Budget.MaxCostEUR)
	}
	msgs := []Message{{Role: "user", Content: briefPrompt(b)}}
	var problems []string
	for attempt := 1; attempt <= l.o.planAttempts; attempt++ {
		r, err := l.m.Complete(ctx, Request{System: systemFor(l.o.maxTasks), Messages: msgs, MaxTokens: l.o.maxOutput})
		if err != nil {
			return nil, err
		}
		var p *planOutput
		p, problems = l.check(b, r)
		if len(problems) == 0 {
			plan, err := l.c.ProposePlan(ctx, planInput(b, p))
			if err == nil {
				return plan, nil
			}
			if !client.IsCode(err, client.CodeBadRequest) {
				return nil, fmt.Errorf("propose_plan: %w", err)
			}
			var ce *client.Error
			errors.As(err, &ce)
			problems = []string{"server rejected the plan: " + ce.Message}
		}
		l.emit("plan_invalid", map[string]any{"brief": b.ID, "attempt": attempt, "problems": problems})
		text := r.Text
		if strings.TrimSpace(text) == "" {
			text = "(empty)"
		}
		msgs = append(msgs, Message{Role: "assistant", Content: text}, Message{Role: "user", Content: feedback(problems)})
	}
	return nil, fmt.Errorf("no valid plan after %d attempts: %s", l.o.planAttempts, strings.Join(problems, "; "))
}

func (l *leader) check(b *model.Brief, r *Reply) (*planOutput, []string) {
	if r.StopReason == "max_tokens" {
		return nil, []string{"output was cut off at the token limit; produce a smaller plan"}
	}
	p, err := parsePlan(r.Text)
	if err != nil {
		return nil, []string{err.Error()}
	}
	return p, validatePlan(b, p, l.o.maxTasks)
}
