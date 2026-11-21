package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const (
	exitOK          = 0
	exitUsage       = 1
	exitAuth        = 2
	exitUnreachable = 3
	exitPaused      = 4

	requestTimeout = 15 * time.Second
	redacted       = "<redacted>"
)

type options struct {
	url         string
	token       string
	kind        string
	printConfig bool
	showToken   bool
}

type report struct {
	who          *client.WhoAmI
	instructions *client.Instructions
	tools        []client.ToolInfo
	stream       bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-join:", err)
		return exitUsage
	}

	rep, err := verify(ctx, opts)
	if rep.who != nil {
		_, _ = io.WriteString(stdout, renderReport(rep))
	}
	code := exitOK
	switch {
	case err != nil:
		_, _ = fmt.Fprintln(stderr, "subspace-join:", strings.ReplaceAll(err.Error(), opts.token, redacted))
		code = classify(err)
	case isPaused(rep.who):
		code = exitPaused
	}
	if opts.printConfig && code != exitAuth && code != exitUnreachable {
		_, _ = io.WriteString(stdout, renderConfig(opts))
	}
	return code
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("subspace-join", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.url, "url", getenv("SUBSPACE_URL"), "Subspace base URL (default $SUBSPACE_URL)")
	fs.StringVar(&o.token, "token", "", "agent token (default $SUBSPACE_TOKEN_FILE contents, then $SUBSPACE_TOKEN)")
	fs.StringVar(&o.kind, "kind", "", "harness kind: claude-code, codex or opencode")
	fs.BoolVar(&o.printConfig, "print-config", false, "print the MCP configuration for -kind")
	fs.BoolVar(&o.showToken, "show-token", false, "include the real token in -print-config output")
	if err := fs.Parse(args); err != nil {
		return o, err
	}

	o.url = strings.TrimRight(strings.TrimSpace(o.url), "/")
	o.url = strings.TrimSuffix(o.url, "/mcp")
	if o.url == "" {
		return o, errors.New("-url or SUBSPACE_URL is required")
	}
	if o.token == "" {
		tok, err := tokenFromEnv(getenv)
		if err != nil {
			return o, err
		}
		o.token = tok
	}
	o.token = strings.TrimSpace(o.token)
	if o.token == "" {
		return o, errors.New("-token, SUBSPACE_TOKEN_FILE or SUBSPACE_TOKEN is required")
	}
	switch o.kind {
	case model.KindClaudeCode, model.KindCodex, model.KindOpenCode:
	case "":
		if o.printConfig {
			return o, errors.New("-print-config requires -kind")
		}
	default:
		return o, fmt.Errorf("unknown -kind %q (want claude-code, codex or opencode)", o.kind)
	}
	return o, nil
}

func tokenFromEnv(getenv func(string) string) (string, error) {
	if f := getenv("SUBSPACE_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", fmt.Errorf("read SUBSPACE_TOKEN_FILE: %w", err)
		}
		return string(b), nil
	}
	return getenv("SUBSPACE_TOKEN"), nil
}

func verify(ctx context.Context, o options) (*report, error) {
	rep := &report{}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	c := client.NewMCP(o.url+"/mcp", o.token,
		client.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		client.WithClientName("subspace-join"))
	if err := c.Initialize(ctx); err != nil {
		return rep, fmt.Errorf("initialize: %w", err)
	}
	defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()

	var err error
	if rep.who, err = c.WhoAmI(ctx); err != nil {
		return rep, fmt.Errorf("whoami: %w", err)
	}
	if rep.instructions, err = c.GetInstructions(ctx); err != nil {
		return rep, fmt.Errorf("get_instructions: %w", err)
	}
	if rep.tools, err = c.ListTools(ctx); err != nil {
		return rep, fmt.Errorf("tools/list: %w", err)
	}
	rep.stream, _ = c.StreamAvailable(ctx)
	return rep, nil
}

func classify(err error) int {
	switch status := client.StatusOf(err); {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return exitAuth
	case status == http.StatusLocked,
		client.IsCode(err, client.CodePaused),
		client.IsCode(err, client.CodeAgentPaused):
		return exitPaused
	}
	return exitUnreachable
}

func isPaused(w *client.WhoAmI) bool {
	if w == nil {
		return false
	}
	return w.Paused || (w.Agent != nil && w.Agent.Status != model.AgentActive)
}

func renderReport(r *report) string {
	a := r.who.Agent
	names := make([]string, len(r.tools))
	for i, t := range r.tools {
		names[i] = t.Name
	}
	verdict := "ready"
	if isPaused(r.who) {
		verdict = "not ready: paused"
	}
	return fmt.Sprintf("agent:        %s\nrole:         %s\nkind:         %s (%s, %s)\nstatus:       %s%s\nworkspace:    %s\ntools (%d):   %s\ninstructions: %d bytes\nnotify:       %s\n%s\n",
		a.ID, a.Role, a.Kind, a.Provider, a.Model, a.Status, pauseSuffix(a), pausedLabel(r.who.Paused),
		len(names), strings.Join(names, ", "), len(r.instructions.Instructions), streamLabel(r.stream), verdict)
}

func pauseSuffix(a *model.Agent) string {
	if a.PauseReason == nil || *a.PauseReason == "" {
		return ""
	}
	return " (" + *a.PauseReason + ")"
}

func streamLabel(ok bool) string {
	if ok {
		return "push stream available"
	}
	return "push stream unavailable (poll list_my_tasks)"
}

func pausedLabel(paused bool) string {
	if paused {
		return "paused"
	}
	return "running"
}

func renderConfig(o options) string {
	token := redacted
	if o.showToken {
		token = o.token
	}
	claude, codex, opencode := spawner.Snippets(o.url, token)
	switch o.kind {
	case model.KindClaudeCode:
		return claude + "\n"
	case model.KindCodex:
		return codex
	}
	return opencode + "\n"
}
