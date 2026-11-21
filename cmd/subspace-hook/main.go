package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
)

const (
	exitOK    = 0
	exitUsage = 2

	callTimeout  = 2 * time.Second
	maxLogBytes  = 32 << 10
	turnComplete = "agent-turn-complete"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stderr, os.Getenv))
}

type env struct {
	url   string
	token string
}

func run(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: subspace-hook heartbeat|log|status|note [flags] [text]")
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("subspace-hook "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	task := fs.String("task", getenv("SUBSPACE_TASK_ID"), "task id")
	var action func(context.Context, *client.MCP, string) error
	switch cmd {
	case "heartbeat":
		event := fs.String("event", "", "Codex notification JSON")
		note := fs.String("note", "", "heartbeat note")
		if err := fs.Parse(args); err != nil {
			return exitUsage
		}
		action = func(ctx context.Context, m *client.MCP, task string) error {
			return heartbeat(ctx, m, task, *note, *event)
		}
	case "status":
		status := fs.String("status", "in_progress", "in_progress or blocked")
		progress := fs.Int("progress", -1, "progress percent")
		note := fs.String("note", "", "status note")
		if err := fs.Parse(args); err != nil {
			return exitUsage
		}
		action = func(ctx context.Context, m *client.MCP, task string) error {
			if task == "" {
				return errors.New("task id required")
			}
			in := board.StatusInput{TaskID: task, Status: *status, Note: *note}
			if *progress >= 0 {
				in.ProgressPct = progress
			}
			_, err := m.UpdateStatus(ctx, in)
			return err
		}
	case "log":
		stream := fs.String("stream", "stdout", "stdout, stderr or note")
		seq := fs.Int64("seq", -1, "chunk sequence number")
		if err := fs.Parse(args); err != nil {
			return exitUsage
		}
		action = func(ctx context.Context, m *client.MCP, task string) error {
			text, err := textArg(fs.Args(), stdin)
			if err != nil || strings.TrimSpace(text) == "" {
				return err
			}
			in := agentout.Input{TaskID: task, Stream: *stream, Text: text}
			if *seq >= 0 {
				in.Seq = seq
			}
			_, err = m.LogOutput(ctx, in)
			return err
		}
	case "note":
		if err := fs.Parse(args); err != nil {
			return exitUsage
		}
		action = func(ctx context.Context, m *client.MCP, task string) error {
			body := strings.TrimSpace(strings.Join(fs.Args(), " "))
			if body == "" {
				return errors.New("note text required")
			}
			_, err := m.AddNote(ctx, task, body)
			return err
		}
	default:
		_, _ = fmt.Fprintf(stderr, "subspace-hook: unknown command %q\n", cmd)
		return exitUsage
	}

	e, err := loadEnv(getenv)
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		m := client.NewMCP(e.url+"/mcp", e.token,
			client.WithRetries(0),
			client.WithClientName("subspace-hook"),
			client.WithHTTPClient(&http.Client{Timeout: callTimeout}),
		)
		err = action(ctx, m, *task)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-hook:", redact(err.Error(), e.token))
	}
	return exitOK
}

func loadEnv(getenv func(string) string) (env, error) {
	e := env{url: strings.TrimRight(strings.TrimSpace(getenv("SUBSPACE_URL")), "/")}
	if e.url == "" {
		return e, errors.New("SUBSPACE_URL not set")
	}
	e.token = strings.TrimSpace(getenv("SUBSPACE_TOKEN"))
	if e.token == "" {
		if path := getenv("SUBSPACE_TOKEN_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				return e, fmt.Errorf("read token file: %w", err)
			}
			e.token = strings.TrimSpace(string(b))
		}
	}
	if e.token == "" {
		return e, errors.New("SUBSPACE_TOKEN or SUBSPACE_TOKEN_FILE not set")
	}
	return e, nil
}

func heartbeat(ctx context.Context, m *client.MCP, task, note, event string) error {
	args := map[string]any{}
	if task != "" {
		args["task_id"] = task
	}
	if note != "" {
		args["note"] = note
	}
	if err := m.Call(ctx, "heartbeat", args, nil); err != nil {
		return err
	}
	if event == "" || task == "" {
		return nil
	}
	var n struct {
		Type   string `json:"type"`
		TurnID string `json:"turn-id"`
	}
	if json.Unmarshal([]byte(event), &n) != nil || n.Type != turnComplete {
		return nil
	}
	in := board.StatusInput{TaskID: task, Status: "in_progress", Note: "codex turn complete " + n.TurnID}
	_, err := m.UpdateStatus(ctx, in)
	return err
}

func textArg(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, maxLogBytes))
	return string(b), err
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}
