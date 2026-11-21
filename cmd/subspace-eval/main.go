package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/SigmaUno/subspace/internal/eval"
	"github.com/SigmaUno/subspace/internal/llm"
)

const (
	exitOK      = 0
	exitUsage   = 1
	exitFloors  = 2
	exitRuntime = 3
)

type options struct {
	model         string
	modelName     string
	baseURL       string
	apiKeyEnv     string
	fixtures      string
	root          string
	planPrompt    string
	reviewPrompt  string
	planPromptB   string
	reviewPromptB string
	concurrency   int
	budgetEUR     float64
	price         eval.Price
	maxTokens     int
	format        string
	verbose       bool
	seed          uint64
	resamples     int
	floors        eval.Floors
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	o, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitUsage
	}
	set, err := eval.Load(o.fixtures)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitUsage
	}
	model, err := newModel(o, set, getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitUsage
	}
	a, err := evalOptions(o, model, o.planPrompt, o.reviewPrompt)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitUsage
	}
	compare := o.planPromptB != "" || o.reviewPromptB != ""
	var b eval.Options
	if compare {
		b, err = evalOptions(o, model, or(o.planPromptB, o.planPrompt), or(o.reviewPromptB, o.reviewPrompt))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
			return exitUsage
		}
	}

	repA, err := eval.Run(ctx, set, a)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitRuntime
	}
	var repB *eval.Report
	var cmp []eval.Comparison
	if compare {
		if o.budgetEUR > 0 {
			b.BudgetEUR = max(o.budgetEUR-repA.Usage.CostEUR, 1e-9)
		}
		if repB, err = eval.Run(ctx, set, b); err != nil {
			_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
			return exitRuntime
		}
		cmp = eval.Compare(repA, repB, o.seed, o.resamples)
	}

	if err := write(stdout, o, repA, repB, cmp); err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-eval:", err)
		return exitRuntime
	}
	violations := repA.Check(o.floors)
	if repB != nil {
		violations = append(violations, repB.Check(o.floors)...)
	}
	for _, v := range violations {
		_, _ = fmt.Fprintln(stderr, "subspace-eval: floor:", v)
	}
	if len(violations) > 0 {
		return exitFloors
	}
	return exitOK
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("subspace-eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.model, "model", "fake", "model: fake, anthropic or openai-compatible")
	fs.StringVar(&o.modelName, "model-name", "", "provider model id (required unless -model fake)")
	fs.StringVar(&o.baseURL, "base-url", "", "API base URL (default per provider; required for openai-compatible servers other than OpenAI)")
	fs.StringVar(&o.apiKeyEnv, "api-key-env", "", "environment variable holding the API key (default ANTHROPIC_API_KEY or OPENAI_API_KEY)")
	fs.StringVar(&o.fixtures, "fixtures", "testdata/eval", "fixture directory")
	fs.StringVar(&o.root, "root", ".", "repository root for go: and file: prompt sources")
	fs.StringVar(&o.planPrompt, "plan-prompt", "leader", "plan system prompt: leader, library:<id>, file:<path> or go:<file>#<const>")
	fs.StringVar(&o.reviewPrompt, "review-prompt", "reviewer", "review system prompt: reviewer, library:<id>, file:<path> or go:<file>#<const>")
	fs.StringVar(&o.planPromptB, "plan-prompt-b", "", "compare against this plan prompt")
	fs.StringVar(&o.reviewPromptB, "review-prompt-b", "", "compare against this review prompt")
	fs.IntVar(&o.concurrency, "concurrency", 4, "parallel model calls")
	fs.Float64Var(&o.budgetEUR, "budget-eur", 0, "stop calling the model once this much was spent (0 = unlimited)")
	fs.Float64Var(&o.price.InputEURPerMTok, "price-in", 3, "EUR per million input tokens")
	fs.Float64Var(&o.price.OutputEURPerMTok, "price-out", 15, "EUR per million output tokens")
	fs.IntVar(&o.maxTokens, "max-tokens", 4096, "max output tokens per call")
	fs.StringVar(&o.format, "format", "table", "output: table or json")
	fs.BoolVar(&o.verbose, "v", false, "list every case in the table, not only imperfect ones")
	fs.Uint64Var(&o.seed, "seed", 1, "bootstrap seed")
	fs.IntVar(&o.resamples, "resamples", 2000, "bootstrap resamples")
	fs.Float64Var(&o.floors.Plan, "min-plan", 0, "exit 2 when the plan mean is below this")
	fs.Float64Var(&o.floors.Review, "min-review", 0, "exit 2 when the review mean is below this")
	fs.Float64Var(&o.floors.Robustness, "min-robustness", 0, "exit 2 when the robustness mean is below this")
	fs.IntVar(&o.floors.MaxUnsafe, "max-unsafe", -1, "exit 2 when a suite has more unsafe approvals than this (-1 = off)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if o.floors.MaxUnsafe < 0 {
		o.floors.MaxUnsafe = int(^uint(0) >> 1)
	}
	switch {
	case o.model != "fake" && o.model != "anthropic" && o.model != "openai-compatible":
		return o, fmt.Errorf("-model must be fake, anthropic or openai-compatible, got %q", o.model)
	case o.model != "fake" && o.modelName == "":
		return o, errors.New("-model-name is required for a real model")
	case o.format != "table" && o.format != "json":
		return o, fmt.Errorf("-format must be table or json, got %q", o.format)
	case o.concurrency < 1 || o.resamples < 1 || o.maxTokens < 1:
		return o, errors.New("-concurrency, -resamples and -max-tokens must be positive")
	case o.budgetEUR < 0 || o.price.InputEURPerMTok < 0 || o.price.OutputEURPerMTok < 0:
		return o, errors.New("-budget-eur and prices must not be negative")
	}
	return o, nil
}

func newModel(o options, set *eval.Set, getenv func(string) string) (llm.Provider, error) {
	provider, keyEnv := "anthropic", "ANTHROPIC_API_KEY"
	switch o.model {
	case "fake":
		return eval.NewFake(set), nil
	case "openai-compatible":
		provider, keyEnv = "openai", "OPENAI_API_KEY"
	}
	if o.apiKeyEnv != "" {
		keyEnv = o.apiKeyEnv
	}
	key := strings.TrimSpace(getenv(keyEnv))
	if key == "" {
		return nil, fmt.Errorf("%s is not set", keyEnv)
	}
	return llm.New(llm.Config{Provider: provider, Model: o.modelName, BaseURL: o.baseURL, APIKey: key, Retries: 3})
}

func evalOptions(o options, m llm.Provider, planSpec, reviewSpec string) (eval.Options, error) {
	plan, err := eval.LoadPrompt(o.root, planSpec)
	if err != nil {
		return eval.Options{}, err
	}
	review, err := eval.LoadPrompt(o.root, reviewSpec)
	if err != nil {
		return eval.Options{}, err
	}
	return eval.Options{Model: m, PlanPrompt: plan, ReviewPrompt: review, Concurrency: o.concurrency,
		BudgetEUR: o.budgetEUR, Price: o.price, MaxTokens: o.maxTokens}, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func write(w io.Writer, o options, a, b *eval.Report, cmp []eval.Comparison) error {
	if o.format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if b == nil {
			return enc.Encode(a)
		}
		return enc.Encode(map[string]any{"a": a, "b": b, "comparison": cmp})
	}
	if err := eval.WriteTable(w, a, o.verbose); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n--- b ---"); err != nil {
		return err
	}
	if err := eval.WriteTable(w, b, o.verbose); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return eval.WriteComparison(w, cmp)
}
