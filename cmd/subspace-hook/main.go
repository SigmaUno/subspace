package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
)

const (
	exitOK    = 0
	exitUsage = 1

	budget   = 1700 * time.Millisecond
	backstop = 1950 * time.Millisecond
	redacted = "<redacted>"
	usage    = "usage: subspace-hook heartbeat|log|status|note|notify|instructions [flags] [text]"
)

type config struct {
	url   string
	token string
}

type hook struct {
	cfg    config
	lim    *limiter
	stdin  io.Reader
	stdout io.Writer
	task   string
}

func main() {
	time.AfterFunc(backstop, func() { os.Exit(exitOK) })
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr, time.Now))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "heartbeat", "log", "status", "note", "notify", "instructions":
	default:
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	h := &hook{stdin: stdin, stdout: stdout, task: getenv("SUBSPACE_TASK_ID")}
	cfg, err := loadConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-hook:", cmd+":", err)
		return exitOK
	}
	h.cfg = cfg

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if cmd != "instructions" {
		h.lim, err = newLimiter(stateDir(getenv), cfg.url, cfg.token, now)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "subspace-hook:", cmd+": state unavailable")
			return exitOK
		}
	}

	switch err = h.dispatch(ctx, cmd, args, stderr); {
	case errors.Is(err, flag.ErrHelp):
	case err != nil:
		_, _ = fmt.Fprintln(stderr, "subspace-hook:", cmd+":", strings.ReplaceAll(err.Error(), cfg.token, redacted))
		if h.lim != nil && severe(err) {
			h.lim.markDown()
		}
	}
	return exitOK
}

func loadConfig(getenv func(string) string) (config, error) {
	url := strings.TrimRight(strings.TrimSpace(getenv("SUBSPACE_URL")), "/")
	url = strings.TrimSuffix(url, "/mcp")
	if url == "" {
		return config{}, errors.New("SUBSPACE_URL is required")
	}
	token := getenv("SUBSPACE_TOKEN")
	if f := getenv("SUBSPACE_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return config{}, errors.New("cannot read SUBSPACE_TOKEN_FILE")
		}
		token = string(b)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return config{}, errors.New("SUBSPACE_TOKEN_FILE or SUBSPACE_TOKEN is required")
	}
	return config{url: url, token: token}, nil
}

func (h *hook) client() *client.MCP {
	return client.NewMCP(h.cfg.url+"/mcp", h.cfg.token,
		client.WithHTTPClient(&http.Client{}),
		client.WithRetries(0),
		client.WithClientName("subspace-hook"))
}

func (h *hook) dispatch(ctx context.Context, cmd string, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("subspace-hook "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	task := fs.String("task", h.task, "task id (default $SUBSPACE_TASK_ID)")
	stream := fs.String("stream", "stdout", "log stream: stdout, stderr or note")
	status := fs.String("status", "in_progress", "task status: in_progress or blocked")
	progress := fs.Int("progress", -1, "progress percent for status (-1 = unchanged)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))

	switch cmd {
	case "heartbeat":
		return h.heartbeat(ctx, *task)
	case "log":
		return h.log(ctx, *task, *stream, text)
	case "status":
		return h.status(ctx, *task, *status, *progress, text)
	case "note":
		return h.note(ctx, *task, text)
	case "notify":
		return h.notify(ctx, *task, text)
	}
	return h.instructions(ctx)
}

func (h *hook) heartbeat(ctx context.Context, task string) error {
	if _, ok := h.lim.reserve("heartbeat"); !ok {
		return nil
	}
	_, err := h.client().Heartbeat(ctx, task)
	return err
}

func (h *hook) log(ctx context.Context, task, stream, text string) error {
	if text == "" {
		text = readPayload(ctx, h.stdin).logText()
	}
	if text == "" {
		return nil
	}
	seq, ok := h.lim.reserve("log")
	if !ok {
		return nil
	}
	args := map[string]any{
		"stream": stream,
		"text":   strings.ReplaceAll(text, h.cfg.token, redacted),
		"seq":    seq,
	}
	if task != "" {
		args["task_id"] = task
	}
	return h.client().Call(ctx, "log_output", args, nil)
}

func (h *hook) status(ctx context.Context, task, status string, progress int, note string) error {
	if note == "" {
		note = readPayload(ctx, h.stdin).statusNote()
	}
	if _, ok := h.lim.reserve("status"); !ok {
		return nil
	}
	c := h.client()
	if task == "" {
		tasks, err := c.ListMyTasks(ctx, "in_progress")
		if err != nil || len(tasks) == 0 {
			return err
		}
		task = tasks[0].ID
	}
	args := map[string]any{
		"task_id": task,
		"status":  status,
		"note":    strings.ReplaceAll(clip(note, maxNote), h.cfg.token, redacted),
	}
	if progress >= 0 {
		args["progress_pct"] = progress
	}
	return c.Call(ctx, "update_status", args, nil)
}

func (h *hook) note(ctx context.Context, task, text string) error {
	if text == "" {
		text = readPayload(ctx, h.stdin).noteText()
	}
	if text == "" {
		return nil
	}
	if _, ok := h.lim.reserve("note"); !ok {
		return nil
	}
	_, err := h.client().AddNote(ctx, task, strings.ReplaceAll(clip(text, maxNote), h.cfg.token, redacted))
	return err
}

func (h *hook) notify(ctx context.Context, task, event string) error {
	if err := h.heartbeat(ctx, task); err != nil {
		return err
	}
	if parseCodexEvent(event).Type != codexTurnComplete {
		return nil
	}
	return h.status(ctx, task, "in_progress", -1, "Codex finished responding")
}

func (h *hook) instructions(ctx context.Context) error {
	ins, err := h.client().GetInstructions(ctx)
	if err != nil {
		return err
	}
	_, err = io.WriteString(h.stdout, strings.TrimRight(ins.Instructions, "\n")+"\n")
	return err
}

func severe(err error) bool {
	var te *client.TransportError
	if errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch client.StatusOf(err) {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	if client.StatusOf(err) >= http.StatusInternalServerError {
		return true
	}
	switch client.CodeOf(err) {
	case client.CodeRateLimited, client.CodeAgentPaused, client.CodeInternal:
		return true
	}
	return false
}
