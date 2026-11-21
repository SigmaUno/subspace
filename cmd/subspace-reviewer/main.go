package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/llm"
)

const (
	exitOK      = 0
	exitUsage   = 1
	exitRefused = 2
	exitRuntime = 3
)

type options struct {
	url        string
	token      string
	provider   string
	model      string
	llmURL     string
	apiKey     string
	fakeModel  string
	gitHost    string
	gitHostURL string
	gitToken   string
	interval   time.Duration
	once       bool
	limits     limits
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	opts, err := parseOptions(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-reviewer:", err)
		return exitUsage
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	api := client.NewMCP(mcpURL(opts.url), opts.token, client.WithClientName("subspace-reviewer"))
	if err := api.Initialize(ctx); err != nil {
		log.Error("connect", "err", redact(err, opts))
		return exitRuntime
	}
	defer func() { _ = api.Close(context.WithoutCancel(ctx)) }()
	provider, err := newModel(ctx, opts, api)
	if err != nil {
		log.Error("model", "err", redact(err, opts))
		return exitUsage
	}
	src, err := newSource(opts.gitHost, opts.gitHostURL, opts.gitToken, hc)
	if err != nil {
		log.Error("git host", "err", err)
		return exitUsage
	}
	r := &reviewer{api: api, src: src, model: provider, limits: opts.limits, log: log, failed: map[string]time.Time{}}
	if err := r.checkProvider(ctx); err != nil {
		log.Error("refusing to review", "err", redact(err, opts))
		return exitRefused
	}
	for {
		n, err := r.pass(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("pass failed", "err", redact(err, opts))
		}
		if opts.once {
			if err != nil {
				return exitRuntime
			}
			log.Info("pass done", "reviewed", n)
			return exitOK
		}
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(opts.interval):
		}
	}
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("subspace-reviewer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := options{}
	fs.StringVar(&o.url, "url", envOr(getenv, "SUBSPACE_URL", "http://localhost:8080"), "Subspace base URL")
	fs.StringVar(&o.provider, "provider", getenv("SUBSPACE_REVIEWER_PROVIDER"), "model provider: anthropic, openai, deepseek")
	fs.StringVar(&o.model, "model", getenv("SUBSPACE_REVIEWER_MODEL"), "model id")
	fs.StringVar(&o.llmURL, "llm-url", getenv("SUBSPACE_REVIEWER_LLM_URL"), "model API base URL (default per provider)")
	fs.StringVar(&o.fakeModel, "fake-model", "", "deterministic canned model instead of a provider: approve or reject")
	fs.StringVar(&o.gitHost, "githost", envOr(getenv, "SUBSPACE_REVIEWER_GITHOST", "forgejo"), "git host: forgejo or github")
	fs.StringVar(&o.gitHostURL, "githost-url", getenv("SUBSPACE_REVIEWER_GITHOST_URL"), "git host base URL (forgejo) or API URL (github)")
	fs.DurationVar(&o.interval, "interval", 30*time.Second, "poll interval")
	fs.BoolVar(&o.once, "once", false, "run one pass and exit")
	fs.IntVar(&o.limits.DiffBudget, "diff-budget", 60000, "max diff bytes sent to the model")
	fs.IntVar(&o.limits.TextBudget, "text-budget", 8000, "max bytes of task spec and PR description each")
	fs.IntVar(&o.limits.MaxRounds, "max-rounds", 3, "do not review tasks that already had this many review rounds")
	fs.IntVar(&o.limits.Attempts, "attempts", 2, "model calls per task until the output is valid JSON")
	fs.IntVar(&o.limits.MaxTokens, "max-tokens", 4096, "max output tokens per model call")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	var err error
	if o.token, err = secret(getenv, "SUBSPACE_TOKEN"); err != nil {
		return o, err
	}
	if o.token == "" {
		return o, errors.New("SUBSPACE_TOKEN or SUBSPACE_TOKEN_FILE is required")
	}
	if o.gitToken, err = secret(getenv, "SUBSPACE_REVIEWER_GIT_TOKEN"); err != nil {
		return o, err
	}
	switch o.fakeModel {
	case "":
		if o.apiKey, err = secret(getenv, "SUBSPACE_REVIEWER_API_KEY"); err != nil {
			return o, err
		}
		if o.provider == "" || o.model == "" || o.apiKey == "" {
			return o, errors.New("-provider, -model and SUBSPACE_REVIEWER_API_KEY(_FILE) are required without -fake-model")
		}
	case "approve", "reject":
	default:
		return o, fmt.Errorf("-fake-model must be approve or reject, got %q", o.fakeModel)
	}
	if o.interval <= 0 || o.limits.DiffBudget <= 0 || o.limits.TextBudget <= 0 || o.limits.MaxRounds <= 0 || o.limits.Attempts <= 0 {
		return o, errors.New("-interval, -diff-budget, -text-budget, -max-rounds and -attempts must be positive")
	}
	return o, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func secret(getenv func(string) string, key string) (string, error) {
	if path := getenv(key + "_FILE"); path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(getenv(key)), nil
}

func mcpURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/mcp") {
		return base
	}
	return base + "/mcp"
}

func redact(err error, o options) string {
	s := err.Error()
	for _, v := range []string{o.token, o.apiKey, o.gitToken} {
		if v != "" {
			s = strings.ReplaceAll(s, v, "<redacted>")
		}
	}
	return s
}

func newModel(ctx context.Context, o options, api subspace) (llm.Provider, error) {
	if o.fakeModel == "" {
		return llm.New(llm.Config{Provider: o.provider, Model: o.model, BaseURL: o.llmURL, APIKey: o.apiKey, Retries: 3})
	}
	who, err := api.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	provider := o.provider
	if provider == "" && who.Agent != nil {
		provider = who.Agent.Provider
	}
	return fakeModel(provider, o.fakeModel == "approve"), nil
}

func fakeModel(provider string, approve bool) llm.Provider {
	n := 0
	return &llm.Func{ProviderName: provider, ModelName: "fake", Respond: func(_ context.Context, req llm.Request) (*llm.Response, error) {
		n++
		var in promptInput
		if len(req.Messages) == 0 {
			return nil, errors.New("fake model: no messages")
		}
		if err := json.Unmarshal([]byte(req.Messages[0].Content), &in); err != nil {
			return nil, fmt.Errorf("fake model: %w", err)
		}
		out := output{Verdict: verdictChanges, Comments: []string{"canned response from the fake model"}, PerCriterion: []criterionResult{}}
		if approve {
			out.Verdict = verdictApprove
		}
		for _, c := range in.AcceptanceCriteria {
			met := approve
			out.PerCriterion = append(out.PerCriterion, criterionResult{Criterion: c, Met: &met, Evidence: "fake model"})
		}
		b, _ := json.Marshal(out)
		return &llm.Response{ID: fmt.Sprintf("fake-%d", n), Model: "fake", Text: string(b)}, nil
	}}
}
